package valkey_test

import (
	"context"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkDeployFinalized_MakesTheDeployReadAsFinalized(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()

	finalized, err := s.IsDeployFinalized(ctx, "www", "20260420-141522-abc1234")
	require.NoError(t, err)
	assert.False(t, finalized, "a deploy nobody finalized must accept uploads")

	require.NoError(t, s.MarkDeployFinalized(ctx, "www", "20260420-141522-abc1234", 15*time.Minute))

	finalized, err = s.IsDeployFinalized(ctx, "www", "20260420-141522-abc1234")
	require.NoError(t, err)
	assert.True(t, finalized,
		"the alias now points at this prefix, so a later write with the same permit would edit a live "+
			"deploy in place; tenet 5 says a deploy is immutable")
}

func TestMarkDeployFinalized_ScopesToTheSiteAndTheDeploy(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	require.NoError(t, s.MarkDeployFinalized(ctx, "www", "d1", time.Minute))

	for _, tc := range []struct {
		site sitekey.Slug
		id   string
	}{{"learn", "d1"}, {"www", "d2"}} {
		finalized, err := s.IsDeployFinalized(ctx, tc.site, tc.id)
		require.NoError(t, err)
		assert.False(t, finalized, "marking %s/%s must not fence %s/%s", "www", "d1", tc.site, tc.id)
	}
}

func TestMarkDeployFinalized_ExpiresWithThePermitThatCouldAbuseIt(t *testing.T) {
	s, mr, _ := newStore(t)
	ctx := context.Background()
	require.NoError(t, s.MarkDeployFinalized(ctx, "www", "d1", 15*time.Minute))

	mr.FastForward(15*time.Minute + time.Second)

	finalized, err := s.IsDeployFinalized(ctx, "www", "d1")
	require.NoError(t, err)
	assert.False(t, finalized,
		"the marker exists only to outlive the deploy-session jwt; keeping it after the token expires "+
			"would grow without bound and fence nothing")
}

func TestIsDeployFinalized_ReportsTheFaultRatherThanAnswerNo(t *testing.T) {
	s, mr, _ := newStore(t)
	mr.Close()

	_, err := s.IsDeployFinalized(context.Background(), "www", "d1")

	require.Error(t, err,
		"answering false on a cache fault would silently reinstate the overwrite this marker prevents; "+
			"the caller decides what to do, and it cannot decide if the fault is hidden")
}

func TestMarkDeployFinalized_RefusesANonPositiveTTL(t *testing.T) {
	s, _, _ := newStore(t)

	err := s.MarkDeployFinalized(context.Background(), "www", "d1", 0)

	require.Error(t, err,
		"go-redis treats a zero expiration as no expiration, so a miswired ttl would write a key that "+
			"never dies and leaks for every deploy artemis ever finalizes")

	finalized, readErr := s.IsDeployFinalized(context.Background(), "www", "d1")
	require.NoError(t, readErr)
	assert.False(t, finalized, "the refused write must leave nothing behind")
}
