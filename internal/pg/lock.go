package pg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/freeCodeCamp/artemis/internal/gc"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func (r *Repo) WithSiteLock(ctx context.Context, site sitekey.Dirname, fn func() error) error {
	sess, err := r.NewLockSession(ctx)
	if err != nil {
		return fmt.Errorf("site lock %s: %w", site, err)
	}
	defer sess.Close(ctx)
	return sess.WithSiteLock(ctx, site, fn)
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
	return &lockSession{conn: conn, onLost: r.lockSessionLost}, nil
}

func (r *Repo) OnLockSessionLost(fn func()) { r.lockSessionLost = fn }

type sessionConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Ping(ctx context.Context) error
	Close(ctx context.Context) error
}

type lockSession struct {
	conn      sessionConn
	onLost    func()
	heartbeat time.Duration
}

func (s *lockSession) beat() time.Duration {
	if s.heartbeat > 0 {
		return s.heartbeat
	}
	return defaultLockHeartbeat
}

func (s *lockSession) WithSiteLock(ctx context.Context, site sitekey.Dirname, fn func() error) error {
	if _, lockErr := s.conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, site); lockErr != nil {
		return fmt.Errorf("site lock %s: %w", site, lockErr)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, unlockErr := s.conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, site); unlockErr != nil {
			slog.WarnContext(ctx, "lock.site.unlock_failed", "site", site, "err", unlockErr)
			closeCtx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = s.conn.Close(closeCtx)
			ccancel()
		}
	}()

	stop := s.watchLiveness(ctx, site)
	defer stop()
	return fn()
}

const defaultLockHeartbeat = 5 * time.Second

func (s *lockSession) watchLiveness(ctx context.Context, site sitekey.Dirname) func() {
	if s.onLost == nil {
		return func() {}
	}
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		beat := s.beat()
		t := time.NewTicker(beat)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				pingCtx, cancel := context.WithTimeout(ctx, beat)
				err := s.conn.Ping(pingCtx)
				cancel()
				if err != nil && ctx.Err() == nil {
					slog.WarnContext(ctx, "lock.site.session_lost", "site", site, "err", err)
					s.onLost()
					return
				}
			}
		}
	}()
	// pgx v5 conn.go: "Conn is a PostgreSQL connection handle. It is not
	// safe for concurrent usage" — stop() must join, not merely signal.
	return func() {
		close(done)
		<-exited
	}
}

func (s *lockSession) Close(ctx context.Context) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.conn.Close(closeCtx); err != nil {
		slog.WarnContext(ctx, "lock.session.close_failed", "err", err)
	}
}
