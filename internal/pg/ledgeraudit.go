package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type StuckClaim struct {
	Slug      sitekey.Slug
	ClaimedAt time.Time
}

type OverdueReservation struct {
	Slug          sitekey.Slug
	ReservedUntil time.Time
}

type LedgerDrift struct {
	Stuck   []StuckClaim
	Overdue []OverdueReservation
}

func (d LedgerDrift) Empty() bool { return len(d.Stuck) == 0 && len(d.Overdue) == 0 }

func (s *RegistryStore) LedgerAudit(ctx context.Context, now time.Time, runBudget, overdue time.Duration) (LedgerDrift, error) {
	var d LedgerDrift
	now = now.UTC()
	rows, err := s.pool.Query(ctx,
		`SELECT slug, reclaim_started_at FROM sites
		 WHERE state = $1 AND reclaim_started_at IS NOT NULL
		   AND reclaim_started_at < $2::timestamptz - make_interval(secs => $3)
		 ORDER BY reclaim_started_at`,
		registry.StateReserved, now, runBudget.Seconds())
	if err != nil {
		return d, fmt.Errorf("pg ledger audit stuck claims: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c StuckClaim
		if err := rows.Scan(&c.Slug, &c.ClaimedAt); err != nil {
			return d, fmt.Errorf("pg ledger audit stuck scan: %w", err)
		}
		d.Stuck = append(d.Stuck, c)
	}
	if err := rows.Err(); err != nil {
		return d, fmt.Errorf("pg ledger audit stuck rows: %w", err)
	}
	rows.Close()
	if overdue <= 0 {
		return d, nil
	}

	rows, err = s.pool.Query(ctx,
		`SELECT slug, reserved_until FROM sites
		 WHERE state = $1 AND reclaim_started_at IS NULL
		   AND reserved_until < $2::timestamptz - make_interval(secs => $3)
		 ORDER BY reserved_until`,
		registry.StateReserved, now, overdue.Seconds())
	if err != nil {
		return d, fmt.Errorf("pg ledger audit overdue reservations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var o OverdueReservation
		if err := rows.Scan(&o.Slug, &o.ReservedUntil); err != nil {
			return d, fmt.Errorf("pg ledger audit overdue scan: %w", err)
		}
		d.Overdue = append(d.Overdue, o)
	}
	return d, rows.Err()
}
