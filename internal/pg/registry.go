package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freeCodeCamp/artemis/internal/registry"
)

type RegistryStore struct {
	pool     *pgxpool.Pool
	now      func() time.Time
	onChange func(slug string)
}

func NewRegistryStore(db *DB) *RegistryStore {
	return &RegistryStore{pool: db.Pool, now: time.Now}
}

func (s *RegistryStore) WithClock(now func() time.Time) *RegistryStore {
	s.now = now
	return s
}

func (s *RegistryStore) WithOnChange(fn func(slug string)) *RegistryStore {
	s.onChange = fn
	return s
}

func (s *RegistryStore) changed(slug string) {
	if s.onChange != nil {
		s.onChange(slug)
	}
}

func (s *RegistryStore) Register(ctx context.Context, slug string, teams []string, createdBy string) (registry.Site, error) {
	now := s.now().UTC()
	teams = append([]string(nil), teams...)
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO sites (slug, teams, created_at, updated_at, created_by)
		 VALUES ($1, $2, $3, $3, $4)
		 ON CONFLICT (slug) DO NOTHING`,
		slug, teams, now, createdBy)
	if err != nil {
		return registry.Site{}, fmt.Errorf("pg registry register %s: %w", slug, err)
	}
	if tag.RowsAffected() == 0 {
		return registry.Site{}, registry.ErrAlreadyExists
	}
	s.changed(slug)
	return registry.Site{Slug: slug, Teams: teams, CreatedAt: now, UpdatedAt: now, CreatedBy: createdBy}, nil
}

func (s *RegistryStore) UpdateTeams(ctx context.Context, slug string, teams []string) (registry.Site, error) {
	now := s.now().UTC()
	teams = append([]string(nil), teams...)
	var site registry.Site
	err := s.pool.QueryRow(ctx,
		`UPDATE sites SET teams = $2, updated_at = $3 WHERE slug = $1
		 RETURNING slug, teams, created_at, updated_at, created_by`,
		slug, teams, now).Scan(&site.Slug, &site.Teams, &site.CreatedAt, &site.UpdatedAt, &site.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return registry.Site{}, registry.ErrNotFound
	}
	if err != nil {
		return registry.Site{}, fmt.Errorf("pg registry update %s: %w", slug, err)
	}
	s.changed(slug)
	return site, nil
}

func (s *RegistryStore) Delete(ctx context.Context, slug string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sites WHERE slug = $1`, slug)
	if err != nil {
		return fmt.Errorf("pg registry delete %s: %w", slug, err)
	}
	if tag.RowsAffected() == 0 {
		return registry.ErrNotFound
	}
	s.changed(slug)
	return nil
}

type SitesSource interface {
	Sites(ctx context.Context) ([]registry.Site, error)
}

const importAdvisoryLockKey = 8472014

func (s *RegistryStore) Import(ctx context.Context, src SitesSource) (int, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("pg registry import: acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", importAdvisoryLockKey); err != nil {
		return 0, fmt.Errorf("pg registry import: lock: %w", err)
	}
	defer releaseAdvisoryLock(conn, importAdvisoryLockKey)

	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM sites").Scan(&count); err != nil {
		return 0, fmt.Errorf("pg registry import: count: %w", err)
	}
	if count > 0 {
		return 0, nil
	}

	sites, err := src.Sites(ctx)
	if err != nil {
		return 0, fmt.Errorf("pg registry import: source sites: %w", err)
	}

	imported := 0
	for _, site := range sites {
		teams := append([]string(nil), site.Teams...)
		tag, err := conn.Exec(ctx,
			`INSERT INTO sites (slug, teams, created_at, updated_at, created_by)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (slug) DO NOTHING`,
			site.Slug, teams, site.CreatedAt, site.UpdatedAt, site.CreatedBy)
		if err != nil {
			return imported, fmt.Errorf("pg registry import %s: %w", site.Slug, err)
		}
		imported += int(tag.RowsAffected())
	}
	return imported, nil
}

func (s *RegistryStore) Sites(ctx context.Context) ([]registry.Site, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT slug, teams, created_at, updated_at, created_by FROM sites ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("pg registry list: %w", err)
	}
	defer rows.Close()

	var out []registry.Site
	for rows.Next() {
		var site registry.Site
		if err := rows.Scan(&site.Slug, &site.Teams, &site.CreatedAt, &site.UpdatedAt, &site.CreatedBy); err != nil {
			return nil, fmt.Errorf("pg registry scan: %w", err)
		}
		out = append(out, site)
	}
	return out, rows.Err()
}
