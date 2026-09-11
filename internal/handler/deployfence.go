package handler

import (
	"context"
	"log/slog"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const opDeployFence = "deploy.fence"

func (h *Handlers) fenceFinalizedDeploy(ctx context.Context, site sitekey.Slug, deployID, mode string) {
	if h.DeployFence == nil {
		slog.WarnContext(ctx, "deploy.fence.unwired", "site", site, "deployId", deployID,
			"detail", "the alias now points at this prefix and the deploy permit stays valid, so a "+
				"later upload with the same token can overwrite what is live")
		return
	}
	err := retryIdempotentCommit(ctx, func(ctx context.Context) error {
		return h.DeployFence.MarkDeployFinalized(ctx, site, deployID, mode, h.DeployJWTTTL)
	})
	if err != nil {
		slog.ErrorContext(ctx, "deploy.fence.failed", "site", site, "deployId", deployID, "mode", mode, "err", err,
			"detail", "neither the upload fence nor the mode fence was written, so the permit can still "+
				"overwrite this deploy or repoint its alias until it expires")
		observability.CaptureBackground(opDeployFence, err)
	}
}
