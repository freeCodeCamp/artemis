package backfill

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/r2"
)

type Lister interface {
	ListSites(ctx context.Context) ([]string, error)
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
	PrefixBytes(ctx context.Context, prefix string) (int64, error)
	GetAlias(ctx context.Context, key string) (string, error)
}

type Indexer interface {
	UpsertDeploy(ctx context.Context, site, id string, mtime time.Time, bytes int64, hasMarker bool, state string) error
	UpsertAlias(ctx context.Context, site, name, deployID string, updatedAt time.Time) error
}

type Backfill struct {
	Lister  Lister
	Indexer Indexer
	Now     func() time.Time
}

type Result struct {
	Sites   int
	Deploys int
	Aliases int
}

func (b *Backfill) Run(ctx context.Context) (Result, error) {
	var res Result
	sites, err := b.Lister.ListSites(ctx)
	if err != nil {
		return res, fmt.Errorf("backfill: list sites: %w", err)
	}

	for _, site := range sites {
		res.Sites++
		deploysPrefix := site + "/deploys/"
		keys, err := b.Lister.ListPrefix(ctx, deploysPrefix)
		if err != nil {
			return res, fmt.Errorf("backfill: list %s: %w", site, err)
		}

		markers := map[string]bool{}
		seen := map[string]struct{}{}
		var order []string
		for _, k := range keys {
			rest := strings.TrimPrefix(k, deploysPrefix)
			seg := rest
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				seg = rest[:i]
			}
			if seg == "" {
				continue
			}
			if _, ok := seen[seg]; !ok {
				seen[seg] = struct{}{}
				order = append(order, seg)
			}
			if rest == seg+"/"+gc.MarkerObjectName {
				markers[seg] = true
			}
		}

		for _, id := range order {
			deployBytes, err := b.Lister.PrefixBytes(ctx, deploysPrefix+id+"/")
			if err != nil {
				slog.Warn("backfill.bytes_unavailable", "site", site, "deployId", id, "err", err)
				deployBytes = 0
			}
			if err := b.Indexer.UpsertDeploy(ctx, site, id, parseDeployMtime(id, b.Now()), deployBytes, markers[id], "active"); err != nil {
				return res, fmt.Errorf("backfill: index deploy %s/%s: %w", site, id, err)
			}
			res.Deploys++
		}

		for _, mode := range []string{"production", "preview"} {
			v, err := b.Lister.GetAlias(ctx, site+"/"+mode)
			if err != nil {
				if r2.IsNotFound(err) {
					continue
				}
				return res, fmt.Errorf("backfill: alias %s/%s: %w", site, mode, err)
			}
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if err := b.Indexer.UpsertAlias(ctx, site, mode, v, b.Now()); err != nil {
				return res, fmt.Errorf("backfill: index alias %s/%s: %w", site, mode, err)
			}
			res.Aliases++
		}
	}
	return res, nil
}

func parseDeployMtime(id string, fallback time.Time) time.Time {
	if len(id) >= 15 {
		if t, err := time.Parse("20060102-150405", id[:15]); err == nil {
			return t.UTC()
		}
	}
	return fallback
}
