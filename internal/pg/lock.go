package pg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repo) WithSiteLock(ctx context.Context, site string, fn func() error) error {
	conn, err := pgx.ConnectConfig(ctx, r.pool.Config().ConnConfig.Copy())
	if err != nil {
		return fmt.Errorf("site lock %s: connect: %w", site, err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := conn.Close(closeCtx); err != nil {
			slog.Error("site lock: close failed; server reaps the session lock", "site", site, "err", err)
		}
	}()

	if _, err := conn.Exec(ctx, `SET lock_timeout = '30s'`); err != nil {
		return fmt.Errorf("site lock %s: set lock_timeout: %w", site, err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, site); err != nil {
		return fmt.Errorf("site lock %s: %w", site, err)
	}
	return fn()
}
