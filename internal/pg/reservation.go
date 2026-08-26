package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func (s *RegistryStore) Reserve(ctx context.Context, slug sitekey.Slug, site sitekey.Dirname,
	until time.Time, by string) (registry.Reservation, error) {
	now := s.now().UTC()
	var res registry.Reservation
	var flipped bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var state string
		err := tx.QueryRow(ctx,
			`SELECT state FROM sites WHERE slug = $1 FOR UPDATE`, slug).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return registry.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("pg reserve lock %s: %w", slug, err)
		}
		if state == registry.StateReserved {
			return scanReservation(tx.QueryRow(ctx,
				`SELECT slug, reserved_at, reserved_until, reserved_by, prev_production, prev_preview
				 FROM sites WHERE slug = $1`, slug), &res)
		}

		prevProduction, prevPreview, err := aliasPointers(ctx, tx, site)
		if err != nil {
			return err
		}
		if err := scanReservation(tx.QueryRow(ctx,
			`UPDATE sites SET state = $2, reserved_at = $3, reserved_until = $4, reserved_by = $5,
			        prev_production = $6, prev_preview = $7, updated_at = $3
			 WHERE slug = $1 AND state = $8
			 RETURNING slug, reserved_at, reserved_until, reserved_by, prev_production, prev_preview`,
			slug, registry.StateReserved, now, until.UTC(), by,
			prevProduction, prevPreview, registry.StateActive), &res); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM aliases WHERE site = $1`, site); err != nil {
			return fmt.Errorf("pg reserve clear aliases %s: %w", site, err)
		}
		flipped = true
		return nil
	})
	if err != nil {
		return registry.Reservation{}, err
	}
	if flipped {
		s.changed(slug)
	}
	return res, nil
}

func (s *RegistryStore) IsHeld(ctx context.Context, slug sitekey.Slug) (bool, error) {
	var held bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM sites WHERE slug = $1 AND state = $2 AND reserved_until > now())`,
		slug, registry.StateReserved).Scan(&held); err != nil {
		return false, fmt.Errorf("pg site held %s: %w", slug, err)
	}
	return held, nil
}

func (s *RegistryStore) Reservation(ctx context.Context, slug sitekey.Slug) (registry.Reservation, error) {
	var res registry.Reservation
	if err := scanReservation(s.pool.QueryRow(ctx,
		`SELECT slug, reserved_at, reserved_until, reserved_by, prev_production, prev_preview
		 FROM sites WHERE slug = $1 AND state = $2 AND reserved_until > now()`,
		slug, registry.StateReserved), &res); err != nil {
		return registry.Reservation{}, err
	}
	return res, nil
}

func (s *RegistryStore) Undelete(ctx context.Context, slug sitekey.Slug) (registry.Reservation, error) {
	now := s.now().UTC()
	var res registry.Reservation
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := scanReservation(tx.QueryRow(ctx,
			`SELECT slug, reserved_at, reserved_until, reserved_by, prev_production, prev_preview
			 FROM sites WHERE slug = $1 AND state = $2 AND reserved_until > now() FOR UPDATE`,
			slug, registry.StateReserved), &res); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE sites SET state = $2, reserved_at = NULL, reserved_until = NULL, reserved_by = '',
			        prev_production = '', prev_preview = '', updated_at = $3
			 WHERE slug = $1`,
			slug, registry.StateActive, now); err != nil {
			return fmt.Errorf("pg undelete %s: %w", slug, err)
		}
		return nil
	})
	if err != nil {
		return registry.Reservation{}, err
	}
	s.changed(slug)
	return res, nil
}

func (s *RegistryStore) ReleaseReservation(ctx context.Context, slug sitekey.Slug) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM sites WHERE slug = $1 AND state = $2 AND reserved_until < now()`,
		slug, registry.StateReserved)
	if err != nil {
		return fmt.Errorf("pg release reservation %s: %w", slug, err)
	}
	if tag.RowsAffected() == 0 {
		return registry.ErrNotFound
	}
	s.changed(slug)
	return nil
}

func (s *RegistryStore) ReleaseReservationNow(ctx context.Context, slug sitekey.Slug) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM sites WHERE slug = $1 AND state = $2`,
		slug, registry.StateReserved)
	if err != nil {
		return fmt.Errorf("pg release reservation now %s: %w", slug, err)
	}
	if tag.RowsAffected() == 0 {
		return registry.ErrNotFound
	}
	s.changed(slug)
	return nil
}

func (s *RegistryStore) ExpiredReservations(ctx context.Context, before time.Time, limit int) ([]registry.Reservation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT slug, reserved_at, reserved_until, reserved_by, prev_production, prev_preview
		 FROM sites WHERE state = $1 AND reserved_until < $2
		 ORDER BY reserved_until LIMIT $3`,
		registry.StateReserved, before.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("pg expired reservations: %w", err)
	}
	defer rows.Close()

	var out []registry.Reservation
	for rows.Next() {
		var r registry.Reservation
		if err := scanReservationRow(rows, &r); err != nil {
			return nil, fmt.Errorf("pg expired reservations scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanReservation(row rowScanner, out *registry.Reservation) error {
	if err := scanReservationRow(row, out); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return registry.ErrNotFound
		}
		return fmt.Errorf("pg reservation scan: %w", err)
	}
	return nil
}

func scanReservationRow(row rowScanner, out *registry.Reservation) error {
	var reservedAt, reservedUntil *time.Time
	if err := row.Scan(&out.Slug, &reservedAt, &reservedUntil,
		&out.ReservedBy, &out.PrevProduction, &out.PrevPreview); err != nil {
		return err
	}
	if reservedAt != nil {
		out.ReservedAt = reservedAt.UTC()
	}
	if reservedUntil != nil {
		out.ReservedUntil = reservedUntil.UTC()
	}
	return nil
}

func aliasPointers(ctx context.Context, tx pgx.Tx, site sitekey.Dirname) (production, preview string, err error) {
	rows, err := tx.Query(ctx, `SELECT name, deploy_id FROM aliases WHERE site = $1`, site)
	if err != nil {
		return "", "", fmt.Errorf("pg reserve read aliases %s: %w", site, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, deployID string
		if err := rows.Scan(&name, &deployID); err != nil {
			return "", "", fmt.Errorf("pg reserve scan alias %s: %w", site, err)
		}
		switch name {
		case "production":
			production = deployID
		case "preview":
			preview = deployID
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("pg reserve read aliases %s: %w", site, err)
	}
	return production, preview, nil
}
