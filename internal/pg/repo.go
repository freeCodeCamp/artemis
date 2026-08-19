package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/registry"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(db *DB) *Repo {
	return &Repo{pool: db.Pool}
}

func (r *Repo) UpsertDeploy(ctx context.Context, site sitekey.Dirname, id string, mtime time.Time, bytes int64, hasMarker bool, state string) error {
	if state == "" {
		state = "active"
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO deploys (site, id, mtime, bytes, has_marker, state)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (site, id) DO UPDATE SET
			mtime = EXCLUDED.mtime,
			bytes = CASE WHEN EXCLUDED.bytes > 0 THEN EXCLUDED.bytes ELSE deploys.bytes END,
			has_marker = EXCLUDED.has_marker, state = EXCLUDED.state`,
		site, id, mtime, bytes, hasMarker, state)
	if err != nil {
		return fmt.Errorf("pg upsert deploy %s/%s: %w", site, id, err)
	}
	return nil
}

func (r *Repo) ReindexDeploy(ctx context.Context, site sitekey.Dirname, id string, mtime time.Time, hasMarker bool) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO deploys (site, id, mtime, bytes, has_marker, state)
		SELECT $1, $2, $3, 0, $4, 'active'
		WHERE NOT EXISTS (SELECT 1 FROM tombstones WHERE site = $1 AND id IN ($2, ''))
		ON CONFLICT (site, id) DO UPDATE SET
			has_marker = EXCLUDED.has_marker,
			state = 'active'`,
		site, id, mtime, hasMarker)
	if err != nil {
		return false, fmt.Errorf("pg reindex deploy %s/%s: %w", site, id, err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repo) UpsertAlias(ctx context.Context, site sitekey.Dirname, name, deployID string, updatedAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO aliases (site, name, deploy_id, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (site, name) DO UPDATE SET
			deploy_id = EXCLUDED.deploy_id, updated_at = EXCLUDED.updated_at`,
		site, name, deployID, updatedAt)
	if err != nil {
		return fmt.Errorf("pg upsert alias %s/%s: %w", site, name, err)
	}
	return nil
}

func (r *Repo) DeploysForSite(ctx context.Context, site sitekey.Dirname) ([]gc.Deploy, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, mtime, bytes, has_marker, alias_released_at FROM deploys WHERE site = $1 AND state = 'active'`, site)
	if err != nil {
		return nil, fmt.Errorf("pg deploys %s: %w", site, err)
	}
	defer rows.Close()

	var out []gc.Deploy
	for rows.Next() {
		var d gc.Deploy
		var released *time.Time
		if err := rows.Scan(&d.ID, &d.Mtime, &d.Bytes, &d.HasMarker, &released); err != nil {
			return nil, fmt.Errorf("pg scan deploy %s: %w", site, err)
		}
		if released != nil {
			d.AliasReleasedAt = *released
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repo) CountDeploys(ctx context.Context) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM deploys WHERE state = 'active'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("pg count deploys: %w", err)
	}
	return n, nil
}

func (r *Repo) KnownSiteDirnames(ctx context.Context) ([]sitekey.Dirname, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT site FROM deploys
		UNION SELECT site FROM aliases
		UNION SELECT site FROM tombstones
		ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("pg known site dirnames: %w", err)
	}
	defer rows.Close()

	var out []sitekey.Dirname
	for rows.Next() {
		var site sitekey.Dirname
		if err := rows.Scan(&site); err != nil {
			return nil, fmt.Errorf("pg scan site dirname: %w", err)
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (r *Repo) AliasTargets(ctx context.Context, site sitekey.Dirname) (map[string]struct{}, time.Time, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT deploy_id, updated_at FROM aliases WHERE site = $1`, site)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("pg aliases %s: %w", site, err)
	}
	defer rows.Close()

	targets := map[string]struct{}{}
	var last time.Time
	for rows.Next() {
		var id string
		var updated time.Time
		if err := rows.Scan(&id, &updated); err != nil {
			return nil, time.Time{}, fmt.Errorf("pg scan alias %s: %w", site, err)
		}
		targets[id] = struct{}{}
		if updated.After(last) {
			last = updated
		}
	}
	return targets, last, rows.Err()
}

func (r *Repo) Tombstone(ctx context.Context, site sitekey.Dirname, d gc.Deploy) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tombstones (site, id, bytes) VALUES ($1, $2, $3)
			 ON CONFLICT (site, id) DO NOTHING`, site, d.ID, d.Bytes); err != nil {
			return fmt.Errorf("pg tombstone insert %s/%s: %w", site, d.ID, err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM deploys WHERE site = $1 AND id = $2`, site, d.ID); err != nil {
			return fmt.Errorf("pg tombstone delete deploy %s/%s: %w", site, d.ID, err)
		}
		return nil
	})
}

func (r *Repo) RecordTombstone(ctx context.Context, site sitekey.Dirname, id string, bytes int64) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tombstones (site, id, bytes) VALUES ($1, $2, $3)
			 ON CONFLICT (site, id) DO NOTHING`, site, id, bytes); err != nil {
			return fmt.Errorf("pg record tombstone %s/%s: %w", site, id, err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM deploys WHERE site = $1 AND id = $2`, site, id); err != nil {
			return fmt.Errorf("pg record tombstone delete deploy %s/%s: %w", site, id, err)
		}
		return nil
	})
}

func (r *Repo) RecordSitePurge(ctx context.Context, site sitekey.Dirname) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tombstones (site, id, bytes) VALUES ($1, '', 0)
			 ON CONFLICT (site, id) DO UPDATE SET trashed_at = now()`, site); err != nil {
			return fmt.Errorf("pg site purge tombstone %s: %w", site, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM aliases WHERE site = $1`, site); err != nil {
			return fmt.Errorf("pg site purge aliases %s: %w", site, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM deploys WHERE site = $1`, site); err != nil {
			return fmt.Errorf("pg site purge deploys %s: %w", site, err)
		}
		return nil
	})
}

func (r *Repo) ExpiredTombstones(ctx context.Context, before time.Time) ([]gc.Tombstone, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT site, id, trashed_at, bytes FROM tombstones WHERE trashed_at < $1 ORDER BY site, id`, before)
	if err != nil {
		return nil, fmt.Errorf("pg expired tombstones: %w", err)
	}
	defer rows.Close()

	var out []gc.Tombstone
	for rows.Next() {
		var t gc.Tombstone
		if err := rows.Scan(&t.Site, &t.ID, &t.TrashedAt, &t.Bytes); err != nil {
			return nil, fmt.Errorf("pg scan tombstone: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repo) PruneDeploy(ctx context.Context, site sitekey.Dirname, id string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM deploys WHERE site = $1 AND id = $2`, site, id); err != nil {
		return fmt.Errorf("pg prune deploy %s/%s: %w", site, id, err)
	}
	return nil
}

func (r *Repo) ClearTombstone(ctx context.Context, site sitekey.Dirname, id string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM tombstones WHERE site = $1 AND id = $2`, site, id)
	if err != nil {
		return false, fmt.Errorf("pg clear tombstone %s/%s: %w", site, id, err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repo) TombstonesForSite(ctx context.Context, site sitekey.Dirname) ([]gc.Tombstone, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT site, id, trashed_at, bytes FROM tombstones WHERE site = $1 ORDER BY trashed_at DESC`, site)
	if err != nil {
		return nil, fmt.Errorf("pg tombstones for site %s: %w", site, err)
	}
	defer rows.Close()

	var out []gc.Tombstone
	for rows.Next() {
		var t gc.Tombstone
		if err := rows.Scan(&t.Site, &t.ID, &t.TrashedAt, &t.Bytes); err != nil {
			return nil, fmt.Errorf("pg scan tombstone %s: %w", site, err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repo) RestoreDeploy(ctx context.Context, site sitekey.Dirname, id string, restoredMtime time.Time, bytes int64) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var existed string
		scanErr := tx.QueryRow(ctx,
			`DELETE FROM tombstones WHERE site = $1 AND id = $2 RETURNING id`, site, id).Scan(&existed)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return registry.ErrNotFound
		}
		if scanErr != nil {
			return fmt.Errorf("pg restore deploy delete tombstone %s/%s: %w", site, id, scanErr)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO deploys (site, id, mtime, bytes, has_marker, state)
			 VALUES ($1, $2, $3, $4, true, 'active')
			 ON CONFLICT (site, id) DO UPDATE SET
			 	state = 'active', mtime = EXCLUDED.mtime, bytes = EXCLUDED.bytes, has_marker = true`,
			site, id, restoredMtime, bytes); err != nil {
			return fmt.Errorf("pg restore deploy insert %s/%s: %w", site, id, err)
		}
		return nil
	})
}
