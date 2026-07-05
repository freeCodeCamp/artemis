package registry

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeSnapshot struct {
	bySite map[string][]string
}

func (f fakeSnapshot) Sites() []string {
	out := make([]string, 0, len(f.bySite))
	for k := range f.bySite {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f fakeSnapshot) TeamsForSite(slug string) []string {
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

func (fakeWriter) Register(context.Context, string, []string, string) (Site, error) {
	return Site{}, nil
}

func (fakeWriter) UpdateTeams(context.Context, string, []string) (Site, error) {
	return Site{}, nil
}

func (fakeWriter) Delete(context.Context, string) error { return nil }

var (
	_ Snapshot = fakeSnapshot{}
	_ Reader   = fakeReader{}
	_ Writer   = fakeWriter{}
)

func TestSentinelErrors_NonNilAndDistinct(t *testing.T) {
	t.Parallel()

	require.Error(t, ErrAlreadyExists)
	require.Error(t, ErrNotFound)
	require.NotErrorIs(t, ErrAlreadyExists, ErrNotFound)
	require.NotErrorIs(t, ErrNotFound, ErrAlreadyExists)
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

	snap := fakeSnapshot{bySite: map[string][]string{
		"blog":     {"news-editors", "platform"},
		"internal": {},
	}}

	tests := []struct {
		name string
		slug string
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

	snap := fakeSnapshot{bySite: map[string][]string{
		"charlie": {"staff"},
		"alpha":   {"staff"},
		"bravo":   {"staff"},
	}}

	require.Equal(t, []string{"alpha", "bravo", "charlie"}, snap.Sites())
}
