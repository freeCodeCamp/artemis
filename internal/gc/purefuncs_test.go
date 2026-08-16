package gc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseDeployTime(t *testing.T) {
	t.Parallel()

	fallback := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		id   string
		want time.Time
	}{
		{"valid stamp", "20260420-141522-abc1234", time.Date(2026, 4, 20, 14, 15, 22, 0, time.UTC)},
		{"valid stamp no suffix", "20260420-141522", time.Date(2026, 4, 20, 14, 15, 22, 0, time.UTC)},
		{"too short", "2026042", fallback},
		{"empty", "", fallback},
		{"right length wrong shape", "not-a-timestamp-x", fallback},
		{"month out of range", "20261320-141522-a", fallback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, parseDeployTime(tc.id, fallback))
		})
	}
}

func TestTombstonePurge_TrashPrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		base string
		tomb Tombstone
		want string
	}{
		{"deploy scoped", "_trash/", Tombstone{Site: "www", ID: "d1"}, "_trash/www/d1/"},
		{"whole site", "_trash/", Tombstone{Site: "www"}, "_trash/www/"},
		{"empty base defaults", "", Tombstone{Site: "www", ID: "d1"}, "_trash/www/d1/"},
		{"empty base whole site", "", Tombstone{Site: "www"}, "_trash/www/"},
		{"custom base", "graveyard/", Tombstone{Site: "www", ID: "d1"}, "graveyard/www/d1/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &TombstonePurge{TrashBase: tc.base}
			require.Equal(t, tc.want, p.trashPrefix(tc.tomb))
		})
	}
}
