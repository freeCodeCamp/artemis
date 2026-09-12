package handler

import (
	"context"
	"log/slog"
	"net/url"
	"slices"
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
		public := h.publicURL(site, mode)
		u, err := url.Parse(public)
		if err != nil || u.Host == "" {
			slog.Warn("edge.purge.skipped", "site", site, "mode", mode, "url", public,
				"detail", "the public url format yields no host; set a scheme in PUBLIC_URL_*_FORMAT")
			continue
		}
		hosts = append(hosts, u.Host)
	}
	if len(hosts) == 0 {
		return
	}
	h.edgePurgeMu.Lock()
	defer h.edgePurgeMu.Unlock()
	if h.edgePurgePending == nil {
		h.edgePurgePending = map[sitekey.Slug]*edgePurgeBatch{}
	}
	if batch, ok := h.edgePurgePending[site]; ok && batch.timer.Stop() {
		batch.add(hosts)
		batch.timer.Reset(h.EdgePurgeDelay)
		slog.Info("edge.purge.coalesced", "site", site, "hosts", batch.hosts, "delay", h.EdgePurgeDelay)
		return
	}
	batch := &edgePurgeBatch{}
	batch.add(hosts)
	h.edgePurgePending[site] = batch
	slog.Info("edge.purge.scheduled", "site", site, "hosts", hosts, "delay", h.EdgePurgeDelay)
	batch.timer = time.AfterFunc(h.EdgePurgeDelay, func() {
		h.edgePurgeMu.Lock()
		if h.edgePurgePending[site] == batch {
			delete(h.edgePurgePending, site)
		}
		h.edgePurgeMu.Unlock()
		h.purgeHostsNow(site, batch.hosts)
	})
}

type edgePurgeBatch struct {
	timer *time.Timer
	hosts []string
}

func (b *edgePurgeBatch) add(hosts []string) {
	for _, host := range hosts {
		if !slices.Contains(b.hosts, host) {
			b.hosts = append(b.hosts, host)
		}
	}
}

func (h *Handlers) purgeHostsNow(site sitekey.Slug, hosts []string) {
	ctx, cancel := context.WithTimeout(context.Background(), edgePurgeTimeout)
	defer cancel()
	if err := h.EdgePurge.PurgeHosts(ctx, hosts); err != nil {
		slog.Warn("edge.purge.failed", "site", site, "hosts", hosts, "err", err,
			"detail", "the edge keeps the previous deploy until s-maxage expires")
		observability.CaptureBackground(opEdgePurge, err)
		return
	}
	slog.Info("edge.purge.ok", "site", site, "hosts", hosts)
}
