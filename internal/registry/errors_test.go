package registry

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type fakeSnapshot struct {
	bySite map[sitekey.Slug][]string
}

func (f fakeSnapshot) Sites() []sitekey.Slug {
	out := make([]sitekey.Slug, 0, len(f.bySite))
	for k := range f.bySite {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (f fakeSnapshot) TeamsForSite(slug sitekey.Slug) []string {
	teams, ok := f.bySite[slug]
	if !ok {
		return nil
	}
	return teams
}

type fakeReader struct {
	snap Snapshot
}

func (f fakeReader) Snapshot() Snapshot { return f.snap }

type fakeWriter struct{}

func (fakeWriter) Sites(context.Context) ([]Site, error) { return nil, nil }

func (fakeWriter) Register(context.Context, sitekey.Slug, []string, string) (Site, error) {
	return Site{}, nil
}

func (fakeWriter) UpdateTeams(context.Context, sitekey.Slug, []string) (Site, error) {
	return Site{}, nil
}

func (fakeWriter) Delete(context.Context, sitekey.Slug) error { return nil }

func (fakeWriter) GetSite(context.Context, sitekey.Slug) (Site, error) { return Site{}, nil }

var (
	_ Snapshot = fakeSnapshot{}
	_ Reader   = fakeReader{}
	_ Writer   = fakeWriter{}
)

func TestSentinelErrors_NonNilAndDistinct(t *testing.T) {
	t.Parallel()

	require.Error(t, ErrAlreadyExists)
	require.Error(t, ErrNotFound)
	require.Error(t, ErrReserved)
	require.NotErrorIs(t, ErrAlreadyExists, ErrNotFound)
	require.NotErrorIs(t, ErrNotFound, ErrAlreadyExists)
	require.NotErrorIs(t, ErrReserved, ErrAlreadyExists)
	require.NotErrorIs(t, ErrReserved, ErrNotFound)
	require.NotErrorIs(t, ErrAlreadyExists, ErrReserved)
}

func TestSiteIsReserved_AnUnsetStateReadsAsActive(t *testing.T) {
	t.Parallel()

	require.False(t, Site{}.IsReserved(),
		"a backend that stores no state — valkey — must not read as fenced")
	require.False(t, Site{State: StateActive}.IsReserved())
	require.True(t, Site{State: StateReserved}.IsReserved())
}

func TestSentinelErrors_MessageContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"already exists", ErrAlreadyExists, "registry: site already exists"},
		{"not found", ErrNotFound, "registry: site not found"},
		{"reserved", ErrReserved, "registry: site name is reserved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestSentinelErrors_WrapPreservesErrorsIs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sentinel error
		other    error
	}{
		{"already exists", ErrAlreadyExists, ErrNotFound},
		{"not found", ErrNotFound, ErrAlreadyExists},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wrapped := fmt.Errorf("registry: op %s: %w", "blog", tt.sentinel)
			require.ErrorIs(t, wrapped, tt.sentinel)
			require.NotErrorIs(t, wrapped, tt.other)
		})
	}
}

func TestSnapshotContract_TeamsForSiteGatesOnRegistration(t *testing.T) {
	t.Parallel()

	snap := fakeSnapshot{bySite: map[sitekey.Slug][]string{
		"blog":     {"news-editors", "platform"},
		"internal": {},
	}}

	tests := []struct {
		name string
		slug sitekey.Slug
		want []string
	}{
		{"registered site returns its teams", "blog", []string{"news-editors", "platform"}},
		{"registered site with no teams gates closed", "internal", []string{}},
		{"unregistered site returns nil", "absent", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := snap.TeamsForSite(tt.slug)
			require.Equal(t, tt.want, got)
			require.Len(t, got, len(tt.want))
		})
	}
}

func TestSnapshotContract_SitesReturnsSortedSlugs(t *testing.T) {
	t.Parallel()

	snap := fakeSnapshot{bySite: map[sitekey.Slug][]string{
		"charlie": {"staff"},
		"alpha":   {"staff"},
		"bravo":   {"staff"},
	}}

	require.Equal(t, []sitekey.Slug{"alpha", "bravo", "charlie"}, snap.Sites())
}
