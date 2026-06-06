package pg

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

func (r *Repo) WithSiteLock(ctx context.Context, site string, fn func() error) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("site lock %s: acquire conn: %w", site, err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, site); err != nil {
		return fmt.Errorf("site lock %s: %w", site, err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, site); err != nil {
			slog.Error("site lock: unlock failed; session release reaps it", "site", site, "err", err)
		}
	}()
	return fn()
}
