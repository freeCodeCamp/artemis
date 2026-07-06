package pg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/freeCodeCamp/artemis/internal/gc"
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
			slog.WarnContext(ctx, "lock.site.close_failed", "site", site, "err", err)
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

func (r *Repo) NewLockSession(ctx context.Context) (gc.LockSession, error) {
	conn, err := pgx.ConnectConfig(ctx, r.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("lock session: connect: %w", err)
	}
	if _, err := conn.Exec(ctx, `SET lock_timeout = '30s'`); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if cerr := conn.Close(closeCtx); cerr != nil {
			slog.WarnContext(ctx, "lock.session.settimeout_close_failed", "err", cerr)
		}
		return nil, fmt.Errorf("lock session: set lock_timeout: %w", err)
	}
	return &lockSession{conn: conn}, nil
}

type lockSession struct {
	conn *pgx.Conn
}

func (s *lockSession) WithSiteLock(ctx context.Context, site string, fn func() error) error {
	if _, err := s.conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, site); err != nil {
		return fmt.Errorf("site lock %s: %w", site, err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := s.conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, site); err != nil {
			slog.WarnContext(ctx, "lock.site.unlock_failed", "site", site, "err", err)
		}
	}()
	return fn()
}

func (s *lockSession) Close(ctx context.Context) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.conn.Close(closeCtx); err != nil {
		slog.WarnContext(ctx, "lock.session.close_failed", "err", err)
	}
}
