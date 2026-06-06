package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repo) FinalizeAtomic(ctx context.Context, site, deployID, mode string, mtime time.Time, bytes int64) error {
	return r.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO deploys (site, id, mtime, bytes, has_marker, state)
			 VALUES ($1, $2, $3, $4, true, 'active')
			 ON CONFLICT (site, id) DO UPDATE SET
				mtime = EXCLUDED.mtime, bytes = EXCLUDED.bytes, has_marker = true, state = 'active'`,
			site, deployID, mtime, bytes); err != nil {
			return fmt.Errorf("finalize deploy %s/%s: %w", site, deployID, err)
		}
		if err := upsertAliasStampingRelease(ctx, tx, site, mode, deployID, mtime); err != nil {
			return fmt.Errorf("finalize alias %s/%s: %w", site, mode, err)
		}
		return Enqueue(ctx, tx, TopicSiteChanged, map[string]string{"site": site})
	})
}

func (r *Repo) AliasAtomic(ctx context.Context, site, name, deployID string, at time.Time) error {
	return r.WithTx(ctx, func(tx pgx.Tx) error {
		if err := upsertAliasStampingRelease(ctx, tx, site, name, deployID, at); err != nil {
			return fmt.Errorf("alias %s/%s: %w", site, name, err)
		}
		return Enqueue(ctx, tx, TopicSiteChanged, map[string]string{"site": site})
	})
}

func upsertAliasStampingRelease(ctx context.Context, tx pgx.Tx, site, name, deployID string, at time.Time) error {
	if _, err := tx.Exec(ctx,
		`UPDATE deploys d SET alias_released_at = $4
		 FROM aliases a
		 WHERE a.site = $1 AND a.name = $2 AND a.deploy_id <> $3
		   AND d.site = a.site AND d.id = a.deploy_id`,
		site, name, deployID, at); err != nil {
		return fmt.Errorf("stamp released %s/%s: %w", site, name, err)
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO aliases (site, name, deploy_id, updated_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (site, name) DO UPDATE SET deploy_id = EXCLUDED.deploy_id, updated_at = EXCLUDED.updated_at`,
		site, name, deployID, at)
	return err
}
