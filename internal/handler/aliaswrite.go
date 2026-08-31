package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type aliasTouch struct {
	modes []string
}

func (t *aliasTouch) record(mode string) {
	for _, m := range t.modes {
		if m == mode {
			return
		}
	}
	t.modes = append(t.modes, mode)
}

func (h *Handlers) putAliasTouched(ctx context.Context, t *aliasTouch, site sitekey.Slug, mode, deployID string) error {
	if err := h.R2.PutAlias(ctx, h.aliasKey(site, mode), deployID); err != nil {
		return err
	}
	t.record(mode)
	return nil
}

func (h *Handlers) deleteAliasTouched(ctx context.Context, t *aliasTouch, site sitekey.Slug, mode string) error {
	if err := h.R2.DeleteAlias(ctx, h.aliasKey(site, mode)); err != nil {
		return err
	}
	t.record(mode)
	return nil
}

func (h *Handlers) flushThenPurge(ctx context.Context, w http.ResponseWriter, site sitekey.Slug, t *aliasTouch) {
	if len(t.modes) == 0 {
		return
	}
	if err := http.NewResponseController(w).Flush(); err != nil {
		slog.WarnContext(ctx, "edge.purge.flush_failed", "site", site, "err", err)
	}
	h.purgeEdge(ctx, site, t.modes)
}
