package observability

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

type errHandler struct{ err error }

func (h errHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (h errHandler) Handle(context.Context, slog.Record) error { return h.err }
func (h errHandler) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h errHandler) WithGroup(string) slog.Handler             { return h }

type enabledStub struct{ enabled bool }

func (h enabledStub) Enabled(context.Context, slog.Level) bool  { return h.enabled }
func (h enabledStub) Handle(context.Context, slog.Record) error { return nil }
func (h enabledStub) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h enabledStub) WithGroup(string) slog.Handler             { return h }

type recordingAttrHandler struct {
	attrs  *[][]slog.Attr
	groups *[]string
}

func (h recordingAttrHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (h recordingAttrHandler) Handle(context.Context, slog.Record) error { return nil }
func (h recordingAttrHandler) WithAttrs(a []slog.Attr) slog.Handler {
	*h.attrs = append(*h.attrs, a)
	return h
}
func (h recordingAttrHandler) WithGroup(name string) slog.Handler {
	*h.groups = append(*h.groups, name)
	return h
}

func TestMultiHandler_HandleAggregatesErrorsAndStillFansOut(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("child handler boom")
	var msgs []string

	multi := NewMultiHandler(errHandler{err: sentinel}, recordingHandler{&msgs})
	rec := slog.Record{Message: "still-delivered"}

	err := multi.Handle(context.Background(), rec)

	require.ErrorIs(t, err, sentinel)
	require.Equal(t, []string{"still-delivered"}, msgs, "second handler must run despite first erroring")
}

func TestMultiHandler_Enabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		stubs []slog.Handler
		want  bool
	}{
		{
			name:  "allEnabled",
			stubs: []slog.Handler{enabledStub{true}, enabledStub{true}},
			want:  true,
		},
		{
			name:  "oneEnabled",
			stubs: []slog.Handler{enabledStub{false}, enabledStub{true}},
			want:  true,
		},
		{
			name:  "noneEnabled",
			stubs: []slog.Handler{enabledStub{false}, enabledStub{false}},
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewMultiHandler(tc.stubs...)
			require.Equal(t, tc.want, m.Enabled(context.Background(), slog.LevelInfo))
		})
	}
}

func TestMultiHandler_WithAttrsPropagatesToEveryChild(t *testing.T) {
	t.Parallel()
	var attrs1, attrs2 [][]slog.Attr
	var groups1, groups2 []string
	rec1 := recordingAttrHandler{attrs: &attrs1, groups: &groups1}
	rec2 := recordingAttrHandler{attrs: &attrs2, groups: &groups2}

	want := []slog.Attr{slog.String("site", "x")}
	got := NewMultiHandler(rec1, rec2).WithAttrs(want)

	require.IsType(t, multiHandler{}, got)
	require.Len(t, got.(multiHandler).handlers, 2, "no child dropped")
	require.Equal(t, [][]slog.Attr{want}, attrs1, "child 1 saw the attr")
	require.Equal(t, [][]slog.Attr{want}, attrs2, "child 2 saw the attr")
}

func TestMultiHandler_WithGroupPropagatesToEveryChild(t *testing.T) {
	t.Parallel()
	var attrs1, attrs2 [][]slog.Attr
	var groups1, groups2 []string
	rec1 := recordingAttrHandler{attrs: &attrs1, groups: &groups1}
	rec2 := recordingAttrHandler{attrs: &attrs2, groups: &groups2}

	got := NewMultiHandler(rec1, rec2).WithGroup("g")

	require.IsType(t, multiHandler{}, got)
	require.Len(t, got.(multiHandler).handlers, 2, "no child dropped")
	require.Equal(t, []string{"g"}, groups1, "child 1 saw the group")
	require.Equal(t, []string{"g"}, groups2, "child 2 saw the group")
}
