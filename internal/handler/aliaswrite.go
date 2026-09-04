package handler

import (
	"context"

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

func (h *Handlers) purgeTouched(ctx context.Context, site sitekey.Slug, t *aliasTouch) {
	h.purgeEdge(ctx, site, t.modes)
}
