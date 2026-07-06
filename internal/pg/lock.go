package pg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
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
			slog.Error("site lock: close failed; server reaps the session lock", "site", site, "err", err,
				"reqID", telemetry.FromContext(ctx).ReqID)
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
			slog.Error("lock session: close after set-timeout failure", "err", cerr,
				"reqID", telemetry.FromContext(ctx).ReqID)
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
			slog.Error("site lock: unlock failed; released on session close", "site", site, "err", err,
				"reqID", telemetry.FromContext(ctx).ReqID)
		}
	}()
	return fn()
}

func (s *lockSession) Close(ctx context.Context) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.conn.Close(closeCtx); err != nil {
		slog.Error("lock session: close failed; server reaps the session locks", "err", err,
			"reqID", telemetry.FromContext(ctx).ReqID)
	}
}
