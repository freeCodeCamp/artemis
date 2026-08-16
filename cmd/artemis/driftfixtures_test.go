package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
)

type msgCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (h *msgCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *msgCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}
func (h *msgCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *msgCapture) WithGroup(string) slog.Handler      { return h }

func (h *msgCapture) saw(msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs {
		if m == msg {
			return true
		}
	}
	return false
}

type driftingStore struct{ nopReconcileStore }

func (driftingStore) DeploysForSite(context.Context, string) ([]gc.Deploy, error) {
	return []gc.Deploy{{ID: "ghost", Mtime: time.Now().Add(-30 * 24 * time.Hour)}}, nil
}

func (driftingStore) AliasTargets(context.Context, string) (map[string]struct{}, time.Time, error) {
	return map[string]struct{}{"ghost": {}}, time.Time{}, nil
}

func (driftingStore) RecordTombstone(context.Context, string, string, int64) error {
	return errors.New("pg down")
}

type staticLister struct{ keys []string }

func (l staticLister) ListPrefix(context.Context, string) ([]string, error) { return l.keys, nil }

type passthroughSession struct{}

func (passthroughSession) WithSiteLock(_ context.Context, _ string, fn func() error) error {
	return fn()
}
func (passthroughSession) Close(context.Context) {}

type passthroughLocker struct{}

func (passthroughLocker) NewLockSession(context.Context) (gc.LockSession, error) {
	return passthroughSession{}, nil
}

type nopMover struct{}

func (nopMover) MovePrefix(context.Context, string, string) (int, error) { return 0, nil }
