package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/pg"
)

const (
	opDriftLedger    = "drift.ledger"
	ledgerListCap    = 10
	ledgerOverdueGap = time.Hour
)

var ledgerOverdue = 24*time.Hour + reclaimBatchWorstCase + ledgerOverdueGap

type ledgerAuditor interface {
	LedgerAudit(ctx context.Context, now time.Time, runBudget, overdue time.Duration) (pg.LedgerDrift, error)
}

type stuckOnlyLedger struct{ store *pg.RegistryStore }

func (l stuckOnlyLedger) LedgerAudit(ctx context.Context, now time.Time, runBudget, _ time.Duration) (pg.LedgerDrift, error) {
	return l.store.LedgerAudit(ctx, now, runBudget, 0)
}

func ledgerFor(store *pg.RegistryStore, dryRun bool) ledgerAuditor {
	if dryRun {
		return stuckOnlyLedger{store}
	}
	return store
}

func ledgerMessage(d pg.LedgerDrift, now time.Time) error {
	if d.Empty() {
		return nil
	}
	var parts []error
	if n := len(d.Stuck); n > 0 {
		items := make([]string, 0, min(n, ledgerListCap))
		for _, s := range d.Stuck[:min(n, ledgerListCap)] {
			items = append(items, fmt.Sprintf("%s (claimed %s ago)", s.Slug, now.Sub(s.ClaimedAt).Round(time.Minute)))
		}
		parts = append(parts, fmt.Errorf(
			"%d reserved names hold a reclaim claim older than the %s run budget: %s. The run that claimed each "+
				"never released the row; the bytes are tombstoned and the 03:00 sweep re-emits once the claim is older "+
				"than %s. Escalate when a slug repeats on a second night",
			n, gcRunBudget, listWithTail(items, n), reclaimClaimTTL))
	}
	if n := len(d.Overdue); n > 0 {
		items := make([]string, 0, min(n, ledgerListCap))
		for _, o := range d.Overdue[:min(n, ledgerListCap)] {
			items = append(items, fmt.Sprintf("%s (expired %s ago)", o.Slug, now.Sub(o.ReservedUntil).Round(time.Hour)))
		}
		parts = append(parts, fmt.Errorf(
			"%d expired reservations have waited more than %s with no reclaim claim: %s. The sweep never emitted "+
				"them or the engine never ran the event; check the sweep cap (%d per night), CLEANUP_DRY_RUN and the relay",
			n, ledgerOverdue, listWithTail(items, n), reservationSweepLimit))
	}
	return errors.Join(parts...)
}

func listWithTail(items []string, total int) string {
	s := strings.Join(items, ", ")
	if total > len(items) {
		s += fmt.Sprintf(" and %d more", total-len(items))
	}
	return s
}
