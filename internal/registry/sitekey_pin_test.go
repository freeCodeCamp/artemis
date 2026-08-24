package registry

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

var (
	_ func(Snapshot) []sitekey.Slug                                               = Snapshot.Sites
	_ func(Snapshot, sitekey.Slug) []string                                       = Snapshot.TeamsForSite
	_ func(Writer, context.Context, sitekey.Slug) (Site, error)                   = Writer.GetSite
	_ func(Writer, context.Context, sitekey.Slug, []string, string) (Site, error) = Writer.Register
	_ func(Writer, context.Context, sitekey.Slug, []string) (Site, error)         = Writer.UpdateTeams
	_ func(Writer, context.Context, sitekey.Slug) error                           = Writer.Delete
	_ sitekey.Slug                                                                = Site{}.Slug
)

func TestSiteKeyTypesMarshalAsPlainJSONStrings(t *testing.T) {
	site := Site{
		Slug:      sitekey.Slug("palette-contrast-checker"),
		Teams:     []string{"staff", "universe-devs"},
		CreatedAt: time.Date(2026, time.April, 20, 14, 15, 22, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.August, 19, 9, 30, 0, 0, time.UTC),
		CreatedBy: "octocat",
	}
	const wantSite = `{"Slug":"palette-contrast-checker",` +
		`"Teams":["staff","universe-devs"],` +
		`"CreatedAt":"2026-04-20T14:15:22Z",` +
		`"UpdatedAt":"2026-08-19T09:30:00Z",` +
		`"CreatedBy":"octocat",` +
		`"State":"","ReservedUntil":"0001-01-01T00:00:00Z","ReservedBy":""}`

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"slug", sitekey.Slug("www"), `"www"`},
		{"dirname", sitekey.Dirname("www.freecode.camp"), `"www.freecode.camp"`},
		{"empty slug", sitekey.Slug(""), `""`},
		{"slug slice", []sitekey.Slug{"www", "sudoku"}, `["www","sudoku"]`},
		{"slug map key", map[sitekey.Slug][]string{"www": {"staff"}}, `{"www":["staff"]}`},
		{"site value", site, wantSite},
		{"site pointer", &site, wantSite},
		{"site slice", []Site{site}, "[" + wantSite + "]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
		})
	}
}
