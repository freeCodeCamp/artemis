package observability

import (
	"context"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"
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
