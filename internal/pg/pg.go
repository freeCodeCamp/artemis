package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const ConnectTimeout = 5 * time.Second

type Config struct {
	DatabaseURL string
}

type DB struct {
	Pool *pgxpool.Pool
}

func poolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parse dsn: %w", err)
	}
	if cfg.ConnConfig.ConnectTimeout <= 0 {
		cfg.ConnConfig.ConnectTimeout = ConnectTimeout
	}
	return cfg, nil
}

func New(ctx context.Context, cfg Config) (*DB, error) {
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("pg: empty DatabaseURL")
	}
	poolCfg, err := poolConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	return &DB{Pool: pool}, nil
}

func (db *DB) Ping(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}

func (db *DB) Close() {
	db.Pool.Close()
}
