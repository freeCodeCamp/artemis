package observability

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func bindRecordingHub(t *testing.T) *recordingTransport {
	t.Helper()
	rt := &recordingTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.test/1",
		Transport: rt,
	})
	require.NoError(t, err)

	hub := sentry.CurrentHub()
	prev := hub.Client()
	hub.BindClient(client)
	t.Cleanup(func() { hub.BindClient(prev) })
	return rt
}

func TestCaptureFatal_SetsFatalLevelAndBootTag(t *testing.T) {
	rt := bindRecordingHub(t)

	CaptureFatal(errString("boot boom"))

	require.Len(t, rt.events, 1)
	require.Equal(t, sentry.LevelFatal, rt.events[0].Level)
	require.Equal(t, "boot", rt.events[0].Tags["op"])
}

type bufferedTransport struct {
	pending []*sentry.Event
	events  []*sentry.Event
}

func (b *bufferedTransport) Configure(sentry.ClientOptions) {}
func (b *bufferedTransport) SendEvent(e *sentry.Event)      { b.pending = append(b.pending, e) }
func (b *bufferedTransport) Flush(time.Duration) bool {
	b.events = append(b.events, b.pending...)
	b.pending = nil
	return true
}
func (b *bufferedTransport) FlushWithContext(context.Context) bool { return b.Flush(0) }
func (b *bufferedTransport) Close()                                {}

func TestCaptureFatal_FlushesSynchronously(t *testing.T) {
	bt := &bufferedTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.test/1",
		Transport: bt,
	})
	require.NoError(t, err)
	hub := sentry.CurrentHub()
	prev := hub.Client()
	hub.BindClient(client)
	t.Cleanup(func() { hub.BindClient(prev) })

	CaptureFatal(errString("boot boom"))

	require.Empty(t, bt.pending, "CaptureFatal must flush before returning")
	require.Len(t, bt.events, 1, "event delivered via flush, not transport goodwill")
}

func TestCaptureBackground_FingerprintCarriesOpAndCause(t *testing.T) {
	rt := bindRecordingHub(t)

	CaptureBackground("registry.refresh", errString("x"))

	require.Len(t, rt.events, 1)
	require.Equal(t, "registry.refresh", rt.events[0].Tags["op"])
	require.Equal(t, "unclassified", rt.events[0].Tags["error_class"])
	require.Equal(t, []string{"registry.refresh", "unclassified"}, rt.events[0].Fingerprint)
}

func TestCaptureBackground_DistinctOpsGroupSeparately(t *testing.T) {
	rt := bindRecordingHub(t)

	CaptureBackground("registry.refresh", errString("a"))
	CaptureBackground("token.rotate", errString("b"))
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 2)
	require.Equal(t, []string{"registry.refresh", "unclassified"}, rt.events[0].Fingerprint)
	require.Equal(t, []string{"token.rotate", "unclassified"}, rt.events[1].Fingerprint)
}

func TestCaptureBackground_ShutdownCancelNeverEscalates(t *testing.T) {
	rt := bindRecordingHub(t)
	cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return cur })

	for range 5 {
		CaptureBackground("gc.site.run", fmt.Errorf("tombstone-move: %w", context.Canceled))
		cur = cur.Add(24 * time.Hour)
	}
	CaptureBackground("gc.site.run", fmt.Errorf("hatchet: publish x: %w", status.Error(codes.Canceled, "canceled")))
	sentry.CurrentHub().Flush(time.Second)

	require.Empty(t, rt.events, "SIGTERM cancellation is self-inflicted; pod-restart alerting covers a stuck context, and the Warn line still reaches Sentry Logs")
}

func TestCaptureBackground_FirstEnvironmentalTransientEscalates(t *testing.T) {
	rt := bindRecordingHub(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return base })

	CaptureBackground("relay.run", fmt.Errorf("outbox fetch: %w", &pgconn.PgError{Code: "57P03"}))
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 1, "a per-process streak cannot measure a fleet-wide rate: once per pod never reaches a threshold above 1")
	require.Equal(t, "true", rt.events[0].Tags["transient"])
	require.Equal(t, "pg.in_recovery", rt.events[0].Tags["error_class"])
	require.Equal(t, []string{"relay.run", "transient", "pg.in_recovery"}, rt.events[0].Fingerprint)
}

func TestCaptureBackground_CooldownIsPerCauseNotPerOp(t *testing.T) {
	rt := bindRecordingHub(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return base })

	CaptureBackground("relay.run", fmt.Errorf("outbox fetch: %w", &pgconn.PgError{Code: "57P03"}))
	CaptureBackground("relay.run", fmt.Errorf("relay: %w", pgconn.ErrConnClosed))
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 2, "one cause's cooldown must not swallow a different cause on the same op")
	require.Len(t, fingerprintSet(rt.events), 2)
}

func TestCaptureBackground_Artemis7ShapesDoNotShareABucket(t *testing.T) {
	rt := bindRecordingHub(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return base })

	const op = "pg.registry.list"
	CaptureBackground(op, fmt.Errorf("outbox fetch: %w", &pgconn.PgError{Code: "57P03"}))
	CaptureBackground(op, fmt.Errorf("pg registry list: %w", io.ErrUnexpectedEOF))
	CaptureBackground(op, fmt.Errorf("relay: %w", pgconn.ErrConnClosed))
	CaptureBackground(op, fmt.Errorf("pg registry list: %w", errors.Join(fmt.Errorf("failed to connect: %w", &net.DNSError{
		Err:         "server misbehaving",
		Name:        "artemis-postgresql",
		Server:      "10.11.0.10:53",
		IsTemporary: true,
	}))))
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 4)
	require.Len(t, fingerprintSet(rt.events), 4, "the four shapes merged into ARTEMIS-7 must each get their own issue")
	classes := make(map[string]bool, len(rt.events))
	for _, ev := range rt.events {
		classes[ev.Tags["error_class"]] = true
	}
	require.Len(t, classes, 4)
}

func withTransientClock(t *testing.T, now func() time.Time) {
	t.Helper()
	backgroundTransientRate.mu.Lock()
	prevClock := backgroundTransientRate.clock
	backgroundTransientRate.clock = now
	backgroundTransientRate.escalated = make(map[string]time.Time)
	backgroundTransientRate.mu.Unlock()
	t.Cleanup(func() {
		backgroundTransientRate.mu.Lock()
		backgroundTransientRate.clock = prevClock
		backgroundTransientRate.escalated = make(map[string]time.Time)
		backgroundTransientRate.mu.Unlock()
	})
}

func TestCaptureBackground_SustainedTransientEscalatesOnce(t *testing.T) {
	rt := bindRecordingHub(t)
	cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return cur })

	transientErr := fmt.Errorf("outbox fetch: %w", &pgconn.PgError{Code: "57P03"})
	CaptureBackground("relay.run", transientErr)
	cur = cur.Add(time.Hour)
	CaptureBackground("relay.run", transientErr)
	cur = cur.Add(time.Hour)
	CaptureBackground("relay.run", transientErr)
	cur = cur.Add(time.Hour)
	CaptureBackground("relay.run", transientErr)
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 1,
		"the first transient escalates; the next three fall inside the 24h cooldown")
	require.Equal(t, "relay.run", rt.events[0].Tags["op"])
	require.Equal(t, "true", rt.events[0].Tags["transient"])
	require.Equal(t, "pg.in_recovery", rt.events[0].Tags["error_class"])
	require.Equal(t, []string{"relay.run", "transient", "pg.in_recovery"}, rt.events[0].Fingerprint)
}

func TestCaptureBackground_LowCadenceTransientStillEscalates(t *testing.T) {
	rt := bindRecordingHub(t)
	cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return cur })

	transientErr := fmt.Errorf("drift sweep list r2: %w", context.DeadlineExceeded)
	CaptureBackground("drift.sweep", transientErr)
	cur = cur.Add(24 * time.Hour)
	CaptureBackground("drift.sweep", transientErr)
	cur = cur.Add(24 * time.Hour)
	CaptureBackground("drift.sweep", transientErr)
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 3, "a cron-shaped op escalates every occurrence; the cron cadence is the rate limit")
	require.Equal(t, []string{"drift.sweep", "transient", "ctx.deadline"}, rt.events[0].Fingerprint)
}

func TestCaptureBackground_CapturesRealError(t *testing.T) {
	rt := bindRecordingHub(t)

	CaptureBackground("gc.site.run", errors.New("genuine gc failure"))
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 1, "a non-transient error must still page Sentry")
	require.Equal(t, "gc.site.run", rt.events[0].Tags["op"])
}

func TestCaptureBackground_GRPCUnavailablePages(t *testing.T) {
	rt := bindRecordingHub(t)

	CaptureBackground("relay.run", fmt.Errorf("hatchet: publish x: %w", status.Error(codes.Unavailable, "backend down")))
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 1, "a non-deadline/cancel gRPC error is a real outage and must page")
	require.Equal(t, "relay.run", rt.events[0].Tags["op"])
}

func TestWorkflowPanic_SlogAndSentry(t *testing.T) {
	rt := bindRecordingHub(t)

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	CaptureWorkflowPanic("boom in task")
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 1, "panic still pages Sentry")
	assert.Equal(t, sentry.LevelFatal, rt.events[0].Level)

	out := buf.String()
	assert.Contains(t, out, `"msg":"workflow.panic"`, "panic also emitted to stdout slog")
	assert.Contains(t, out, `"level":"ERROR"`)
	assert.Contains(t, out, "boom in task")
}

func TestCaptureWorkflowPanic_CapturesFatalWithTag(t *testing.T) {
	rt := bindRecordingHub(t)

	CaptureWorkflowPanic("boom in task")
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 1, "a workflow-task panic pages Sentry")
	require.Equal(t, sentry.LevelFatal, rt.events[0].Level)
	require.Equal(t, "hatchet.task", rt.events[0].Tags["op"])
	require.Equal(t, []string{"hatchet.panic"}, rt.events[0].Fingerprint)
}

func TestCaptureBackground_CronOpEscalatesEveryOccurrence(t *testing.T) {
	rt := bindRecordingHub(t)
	cur := time.Date(2026, 7, 15, 4, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return cur })

	transientErr := fmt.Errorf("drift sweep list r2: %w", context.DeadlineExceeded)
	const days = 30
	for range days {
		CaptureBackground("drift.sweep", transientErr)
		cur = cur.Add(24 * time.Hour)
	}
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, days,
		"a daily cron failing every day must escalate every day — the cron cadence IS the rate limit")
	for _, ev := range rt.events {
		require.Equal(t, []string{"drift.sweep", "transient", "ctx.deadline"}, ev.Fingerprint,
			"all occurrences of one cause must group into one Sentry issue")
	}
}

func fingerprintSet(events []*sentry.Event) map[string]bool {
	set := make(map[string]bool, len(events))
	for _, ev := range events {
		set[strings.Join(ev.Fingerprint, "\x00")] = true
	}
	return set
}

func TestCaptureBackground_UnlikeCausesGetDistinctFingerprints(t *testing.T) {
	rt := bindRecordingHub(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return base })

	const op = "tombstone.purge"
	CaptureBackground(op, fmt.Errorf("outbox fetch: %w", &pgconn.PgError{Code: "57P03"}))
	CaptureBackground(op, fmt.Errorf("site lock x: %w", &pgconn.PgError{Code: "55P03"}))
	CaptureBackground(op, fmt.Errorf("hatchet: publish x: %w", status.Error(codes.Unavailable, "backend down")))
	CaptureBackground(op, errors.New("genuine purge failure"))
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 4)
	for _, ev := range rt.events {
		require.Contains(t, []int{2, 3}, len(ev.Fingerprint))
		require.Equal(t, op, ev.Fingerprint[0])
	}
	require.Len(t, fingerprintSet(rt.events), 4, "four unlike causes on one op must not share a Sentry issue")
}

func TestCaptureBackground_SameClassDifferentHostsShareOneBucket(t *testing.T) {
	rt := bindRecordingHub(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return base })

	first := &net.DNSError{Err: "server misbehaving", Name: "artemis-postgresql", Server: "10.11.0.10:53", IsTemporary: true}
	second := &net.DNSError{Err: "server misbehaving", Name: "artemis-postgresql-1", Server: "10.11.0.11:53", IsTemporary: true}
	CaptureBackground("drift.sweep", fmt.Errorf("failed to connect: %w", first))
	CaptureBackground("drift.sweep", fmt.Errorf("failed to connect: %w", second))
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 2)
	require.Len(t, fingerprintSet(rt.events), 1, "the discriminator is the class; hosts, ports and ids must not multiply buckets")
	require.Equal(t, []string{"drift.sweep", "transient", "net.dns_temporary"}, rt.events[0].Fingerprint)
}
