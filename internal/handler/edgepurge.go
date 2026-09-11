package handler

import (
	"context"
	"log/slog"
	"net/url"
	"time"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const (
	opEdgePurge      = "edge.purge"
	edgePurgeTimeout = 20 * time.Second
)

type EdgePurger interface {
	PurgeHosts(ctx context.Context, hosts []string) error
}

func (h *Handlers) purgeEdge(site sitekey.Slug, modes ...string) {
	if h.EdgePurge == nil {
		return
	}
	hosts := make([]string, 0, len(modes))
	for _, mode := range modes {
		u, err := url.Parse(h.publicURL(site, mode))
		if err != nil || u.Host == "" {
			continue
		}
		hosts = append(hosts, u.Host)
	}
	if len(hosts) == 0 {
		return
	}
	slog.Info("edge.purge.scheduled", "site", site, "hosts", hosts, "delay", h.EdgePurgeDelay)
	time.AfterFunc(h.EdgePurgeDelay, func() {
		ctx, cancel := context.WithTimeout(context.Background(), edgePurgeTimeout)
		defer cancel()
		if err := h.EdgePurge.PurgeHosts(ctx, hosts); err != nil {
			slog.Warn("edge.purge.failed", "site", site, "hosts", hosts, "err", err,
				"detail", "the edge keeps the previous deploy until s-maxage expires")
			observability.CaptureBackground(opEdgePurge, err)
			return
		}
		slog.Info("edge.purge.ok", "site", site, "hosts", hosts)
	})
}
