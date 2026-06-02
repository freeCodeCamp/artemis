package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repo) SetAliasCAS(ctx context.Context, site, name, expected, next string, at time.Time) (current string, ok bool, err error) {
	err = r.WithTx(ctx, func(tx pgx.Tx) error {
		var cur string
		scanErr := tx.QueryRow(ctx,
			`SELECT deploy_id FROM aliases WHERE site = $1 AND name = $2 FOR UPDATE`, site, name).Scan(&cur)
		if scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows) {
			return fmt.Errorf("alias cas read %s/%s: %w", site, name, scanErr)
		}
		current = cur
		if cur != expected {
			ok = false
			return nil
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO aliases (site, name, deploy_id, updated_at)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (site, name) DO UPDATE SET deploy_id = EXCLUDED.deploy_id, updated_at = EXCLUDED.updated_at`,
			site, name, next, at); err != nil {
			return fmt.Errorf("alias cas write %s/%s: %w", site, name, err)
		}
		if err := Enqueue(ctx, tx, TopicSiteChanged, map[string]string{"site": site}); err != nil {
			return err
		}
		current = next
		ok = true
		return nil
	})
	return current, ok, err
}
