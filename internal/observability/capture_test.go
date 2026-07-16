package observability

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
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

func TestCaptureBackground_TagsAndFingerprintGroupOnOp(t *testing.T) {
	rt := bindRecordingHub(t)

	CaptureBackground("registry.refresh", errString("x"))

	require.Len(t, rt.events, 1)
	require.Equal(t, "registry.refresh", rt.events[0].Tags["op"])
	require.Equal(t, []string{"registry.refresh"}, rt.events[0].Fingerprint)
}

func TestCaptureBackground_DistinctOpsGroupSeparately(t *testing.T) {
	rt := bindRecordingHub(t)

	CaptureBackground("registry.refresh", errString("a"))
	CaptureBackground("token.rotate", errString("b"))
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 2)
	require.Equal(t, []string{"registry.refresh"}, rt.events[0].Fingerprint)
	require.Equal(t, []string{"token.rotate"}, rt.events[1].Fingerprint)
}

func TestCaptureBackground_SuppressesTransient(t *testing.T) {
	rt := bindRecordingHub(t)

	CaptureBackground("gc.site.run", fmt.Errorf("tombstone-move: %w", context.Canceled))
	CaptureBackground("relay.run", fmt.Errorf("outbox fetch: %w", &pgconn.PgError{Code: "57P03"}))
	CaptureBackground("reconcile.schedule", fmt.Errorf("hatchet: publish site.reconcile: %w", status.Error(codes.DeadlineExceeded, "context deadline exceeded")))
	CaptureBackground("gc.site.run", fmt.Errorf("site lock x: %w", &pgconn.PgError{Code: "55P03"}))
	sentry.CurrentHub().Flush(time.Second)

	require.Empty(t, rt.events, "canceled, 57P03, gRPC DeadlineExceeded, and 55P03 must not create Sentry issues")
}

func withTransientClock(t *testing.T, now func() time.Time) {
	t.Helper()
	backgroundTransientRate.mu.Lock()
	prevClock := backgroundTransientRate.clock
	backgroundTransientRate.clock = now
	backgroundTransientRate.states = make(map[string]*transientOpState)
	backgroundTransientRate.mu.Unlock()
	t.Cleanup(func() {
		backgroundTransientRate.mu.Lock()
		backgroundTransientRate.clock = prevClock
		backgroundTransientRate.states = make(map[string]*transientOpState)
		backgroundTransientRate.mu.Unlock()
	})
}

func TestCaptureBackground_SingleTransientStaysSuppressed(t *testing.T) {
	rt := bindRecordingHub(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return base })

	CaptureBackground("gc.site.run", fmt.Errorf("tombstone-move: %w", context.Canceled))
	sentry.CurrentHub().Flush(time.Second)

	require.Empty(t, rt.events, "a single transient blip must stay suppressed")
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

	require.Len(t, rt.events, 1, "3rd consecutive transient must escalate exactly once")
	require.Equal(t, "relay.run", rt.events[0].Tags["op"])
	require.Equal(t, "true", rt.events[0].Tags["transient_sustained"])
	require.Equal(t, []string{"relay.run", "sustained"}, rt.events[0].Fingerprint)
}

func TestCaptureBackground_LowCadenceTransientStillEscalates(t *testing.T) {
	rt := bindRecordingHub(t)
	cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return cur })

	transientErr := fmt.Errorf("hatchet: publish site.reconcile: %w", context.DeadlineExceeded)
	CaptureBackground("reconcile.schedule", transientErr)
	cur = cur.Add(24 * time.Hour)
	CaptureBackground("reconcile.schedule", transientErr)
	cur = cur.Add(24 * time.Hour)
	CaptureBackground("reconcile.schedule", transientErr)
	sentry.CurrentHub().Flush(time.Second)

	require.Len(t, rt.events, 1, "3 daily-cadence failures 24h apart must still escalate")
	require.Equal(t, []string{"reconcile.schedule", "sustained"}, rt.events[0].Fingerprint)
}

func TestCaptureBackground_GapBeyondResetWindowRearms(t *testing.T) {
	rt := bindRecordingHub(t)
	cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withTransientClock(t, func() time.Time { return cur })

	transientErr := fmt.Errorf("tombstone-move: %w", context.Canceled)
	CaptureBackground("tombstone.purge", transientErr)
	cur = cur.Add(time.Hour)
	CaptureBackground("tombstone.purge", transientErr)
	cur = cur.Add(27 * time.Hour)
	CaptureBackground("tombstone.purge", transientErr)
	sentry.CurrentHub().Flush(time.Second)

	require.Empty(t, rt.events, "a gap beyond resetWindow must reset the streak, not escalate")
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
