package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const StatePending = "pending"

func (r *Repo) BeginDeploy(ctx context.Context, site sitekey.Dirname, id string, mtime time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO deploys (site, id, mtime, bytes, has_marker, state)
		VALUES ($1, $2, $3, 0, false, $4)
		ON CONFLICT (site, id) DO NOTHING`,
		site, id, mtime, StatePending)
	if err != nil {
		return fmt.Errorf("pg begin deploy %s/%s: %w", site, id, err)
	}
	return nil
}

func (r *Repo) ExpiredPendingDeploys(ctx context.Context, site sitekey.Dirname, before time.Time) ([]gc.Deploy, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, mtime, bytes FROM deploys
		WHERE site = $1 AND state = $2 AND mtime < $3
		ORDER BY mtime`, site, StatePending, before)
	if err != nil {
		return nil, fmt.Errorf("pg expired pending deploys %s: %w", site, err)
	}
	defer rows.Close()

	var out []gc.Deploy
	for rows.Next() {
		var d gc.Deploy
		if err := rows.Scan(&d.ID, &d.Mtime, &d.Bytes); err != nil {
			return nil, fmt.Errorf("pg scan pending deploy %s: %w", site, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repo) PendingDeployIDs(ctx context.Context, site sitekey.Dirname) (map[string]struct{}, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id FROM deploys WHERE site = $1 AND state = $2`, site, StatePending)
	if err != nil {
		return nil, fmt.Errorf("pg pending deploy ids %s: %w", site, err)
	}
	defer rows.Close()

	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pg scan pending deploy id %s: %w", site, err)
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func (r *Repo) SitesWithExpiredPending(ctx context.Context, before time.Time, limit int) ([]sitekey.Dirname, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT site FROM deploys
		WHERE state = $1 AND mtime < $2
		ORDER BY site
		LIMIT $3`, StatePending, before, limit)
	if err != nil {
		return nil, fmt.Errorf("pg sites with expired pending: %w", err)
	}
	defer rows.Close()

	var out []sitekey.Dirname
	for rows.Next() {
		var site sitekey.Dirname
		if err := rows.Scan(&site); err != nil {
			return nil, fmt.Errorf("pg scan expired pending site: %w", err)
		}
		out = append(out, site)
	}
	return out, rows.Err()
}
