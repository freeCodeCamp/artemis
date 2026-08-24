package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
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
		{"bare conn closed", pgconn.ErrConnClosed, true},
		{"wrapped conn closed", fmt.Errorf("relay: %w", pgconn.ErrConnClosed), true},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransient(tc.err); got != tc.want {
				t.Errorf("isTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsTransient_UnexpectedEOFIsTransient(t *testing.T) {
	t.Parallel()

	require.True(t, isTransient(io.ErrUnexpectedEOF))
	require.True(t, isTransient(fmt.Errorf("pg registry list: %w", io.ErrUnexpectedEOF)))
}

func TestIsTransient_DNSTemporaryIsTransient(t *testing.T) {
	t.Parallel()

	dnsErr := &net.DNSError{
		Err:         "server misbehaving",
		Name:        "artemis-postgresql",
		Server:      "10.11.0.10:53",
		IsTemporary: true,
	}
	live := fmt.Errorf("pg registry list: %w", errors.Join(fmt.Errorf("failed to connect: %w", dnsErr)))

	require.True(t, isTransient(dnsErr))
	require.True(t, isTransient(live), "errors.As must reach the DNSError through fmt.wrapError and errors.joinError")
}

func TestIsTransient_DNSTimeoutIsTransient(t *testing.T) {
	t.Parallel()

	dnsErr := &net.DNSError{Err: "i/o timeout", IsTimeout: true, IsTemporary: false}

	require.True(t, isTransient(dnsErr), "the gate is the Temporary() method (IsTimeout || IsTemporary), not the IsTemporary field")
}

func TestIsTransient_DNSNotFoundIsNotTransient(t *testing.T) {
	t.Parallel()

	dnsErr := &net.DNSError{Err: "no such host", Name: "artemis-postgresql", IsNotFound: true}

	require.False(t, isTransient(dnsErr), "NXDOMAIN is permanent for a misconfigured hostname; it pages in its own bucket")
	require.False(t, isTransient(fmt.Errorf("failed to connect: %w", dnsErr)))
}

func TestIsTransient_PlainEOFIsNotTransient(t *testing.T) {
	t.Parallel()

	require.False(t, isTransient(io.EOF), "a clean end-of-stream is a control value, not a fault")
	require.False(t, isTransient(fmt.Errorf("relay: %w", io.EOF)))
}
