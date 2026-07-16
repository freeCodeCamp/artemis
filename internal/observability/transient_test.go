package observability

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
		{"deadline exceeded is transient", context.DeadlineExceeded, true},
		{"wrapped deadline exceeded", fmt.Errorf("hatchet publish: %w", context.DeadlineExceeded), true},
		{"grpc deadline exceeded (real hatchet publish shape)", fmt.Errorf("hatchet: publish site.reconcile: %w", status.Error(codes.DeadlineExceeded, "context deadline exceeded")), true},
		{"grpc canceled", status.Error(codes.Canceled, "canceled"), true},
		{"grpc unavailable is not transient", status.Error(codes.Unavailable, "backend down"), false},
		{"lock timeout 55P03 is transient", &pgconn.PgError{Code: "55P03"}, true},
		{"wrapped 55P03", fmt.Errorf("site lock x: %w", &pgconn.PgError{Code: "55P03"}), true},
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
