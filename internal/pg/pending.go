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
