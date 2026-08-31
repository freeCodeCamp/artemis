package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const (
	opEdgePurge     = "edge.purge"
	edgePurgeBudget = 20 * time.Second
)

type EdgePurger interface {
	PurgeHosts(ctx context.Context, hosts []string) error
}

func (h *Handlers) publicHost(site sitekey.Slug, mode string) string {
	u, err := url.Parse(h.publicURL(site, mode))
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" || !strings.HasPrefix(host, string(site)+".") {
		return ""
	}
	return host
}

func (h *Handlers) purgeEdge(ctx context.Context, site sitekey.Slug, modes []string) {
	if len(modes) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), edgePurgeBudget)
	defer cancel()
	if h.EdgePurge == nil {
		slog.WarnContext(ctx, "edge.purge.unwired", "site", site,
			"detail", "the aliases are gone but Cloudflare keeps serving cached assets until its own TTL expires")
		return
	}

	var hosts []string
	for _, mode := range modes {
		host := h.publicHost(site, mode)
		if host == "" {
			err := fmt.Errorf("edge purge %s: no site-specific public host for mode %q; check the public URL format", site, mode)
			slog.ErrorContext(ctx, "edge.purge.no_host", "site", site, "mode", mode, "err", err)
			observability.CaptureBackground(opEdgePurge, err)
			continue
		}
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		return
	}

	if err := h.EdgePurge.PurgeHosts(ctx, hosts); err != nil {
		slog.ErrorContext(ctx, "edge.purge.failed", "site", site, "hosts", hosts, "err", err,
			"detail", "the takedown stands, but cached assets keep serving from the edge until the browser TTL expires")
		observability.CaptureBackground(opEdgePurge, err)
		return
	}
	slog.InfoContext(ctx, "edge.purge.done", "site", site, "hosts", hosts,
		"browserCacheStillWarm", true,
		"detail", "the edge is clear; a browser that already fetched an asset keeps it until its own TTL expires")
}
