package handler

import (
	"context"
	"log/slog"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const opDeployFence = "deploy.fence"

func (h *Handlers) fenceFinalizedDeploy(ctx context.Context, site sitekey.Slug, deployID string) {
	if h.DeployFence == nil {
		slog.WarnContext(ctx, "deploy.fence.unwired", "site", site, "deployId", deployID,
			"detail", "the alias now points at this prefix and the deploy permit stays valid, so a "+
				"later upload with the same token can overwrite what is live")
		return
	}
	if err := h.DeployFence.MarkDeployFinalized(ctx, site, deployID, h.DeployJWTTTL); err != nil {
		slog.ErrorContext(ctx, "deploy.fence.failed", "site", site, "deployId", deployID, "err", err,
			"detail", "the deploy is live and unfenced until the permit expires")
		observability.CaptureBackground(opDeployFence, err)
	}
}
