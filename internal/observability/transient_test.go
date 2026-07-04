package observability

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransient(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"context canceled", context.Canceled, true},
		{"wrapped context canceled", fmt.Errorf("r2 put x: %w", context.Canceled), true},
		{"pg in recovery 57P03", &pgconn.PgError{Code: "57P03"}, true},
		{"wrapped 57P03", fmt.Errorf("relay: fetch: %w", &pgconn.PgError{Code: "57P03"}), true},
		{"deadline exceeded is not transient", context.DeadlineExceeded, false},
		{"lock timeout 55P03 is not transient", &pgconn.PgError{Code: "55P03"}, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransient(tc.err); got != tc.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
