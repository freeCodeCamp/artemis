package main

import (
	"log/slog"

	"github.com/freeCodeCamp/artemis/internal/cloudflare"
	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/handler"
)

func edgePurger(cfg *config.Config) handler.EdgePurger {
	if cfg.EdgeCache.ZoneID == "" || cfg.EdgeCache.APIToken == "" {
		slog.Warn("edge.purge.disabled",
			"hasZoneID", cfg.EdgeCache.ZoneID != "", "hasAPIToken", cfg.EdgeCache.APIToken != "",
			"detail", "a takedown will remove the alias but leave cached assets serving from the Cloudflare edge")
		return nil
	}
	return &cloudflare.PurgeClient{ZoneID: cfg.EdgeCache.ZoneID, Token: cfg.EdgeCache.APIToken}
}
