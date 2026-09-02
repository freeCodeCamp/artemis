package pg

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func TestRegistryStore_LedgerAuditReportsStuckClaimsAndOverdueReservations(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)
	now := time.Now().UTC()
	rows := []struct {
		slug    sitekey.Slug
		expired time.Duration
		claimed time.Duration
	}{
		{"stuck", time.Hour, 2 * time.Hour},
		{"fresh", time.Hour, 10 * time.Minute},
		{"ancient", 50 * time.Hour, 40 * time.Hour},
		{"overdue", 40 * time.Hour, 0},
		{"recent", time.Hour, 0},
		{"claimed-overdue", 40 * time.Hour, 3 * time.Hour},
	}
	for _, r := range rows {
		_, err := store.Register(ctx, r.slug, []string{"staff"}, "alice")
		require.NoError(t, err)
		_, err = store.Reserve(ctx, r.slug, sitekey.Dirname(string(r.slug)+".freecode.camp"), now.Add(-r.expired), "bob", registry.ObservedAliases{})
		require.NoError(t, err)
		if r.claimed > 0 {
			setClaim(t, repo, ctx, r.slug, now.Add(-r.claimed))
		}
	}

	d, err := store.LedgerAudit(ctx, now, 30*time.Minute, 31*time.Hour)
	require.NoError(t, err)

	var stuck []sitekey.Slug
	for _, s := range d.Stuck {
		stuck = append(stuck, s.Slug)
		assert.True(t, s.ClaimedAt.Before(now.Add(-30*time.Minute)), "%s: ClaimedAt must be the stored claim time", s.Slug)
	}
	assert.Equal(t, []sitekey.Slug{"ancient", "claimed-overdue", "stuck"}, stuck,
		"oldest claim first; a claim past the TTL is still reported; a claim inside the run budget is a live run")
	var overdue []sitekey.Slug
	for _, o := range d.Overdue {
		overdue = append(overdue, o.Slug)
		assert.True(t, o.ReservedUntil.Before(now.Add(-31*time.Hour)))
	}
	assert.Equal(t, []sitekey.Slug{"overdue"}, overdue,
		"only an unclaimed reservation past the overdue horizon; a claimed one is stuck, not overdue; an active row is neither")

	d, err = store.LedgerAudit(ctx, now, 30*time.Minute, 0)
	require.NoError(t, err)
	assert.Len(t, d.Stuck, 3, "no overdue window still audits the claims")
	assert.Empty(t, d.Overdue, "no overdue window means no overdue audit")
}

func TestRegistryStore_LedgerAuditIsEmptyOnACleanLedger(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	d, err := store.LedgerAudit(ctx, time.Now().UTC(), 30*time.Minute, 31*time.Hour)
	require.NoError(t, err)
	assert.Empty(t, d.Stuck)
	assert.Empty(t, d.Overdue)
	assert.True(t, d.Empty())
}
