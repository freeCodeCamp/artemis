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
			if got := transientClasses[errorClass(tc.err)]; got != tc.want {
				t.Errorf("transientClasses[errorClass(%v)] = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsTransient_UnexpectedEOFIsTransient(t *testing.T) {
	t.Parallel()

	require.True(t, transientClasses[errorClass(io.ErrUnexpectedEOF)])
	require.True(t, transientClasses[errorClass(fmt.Errorf("pg registry list: %w", io.ErrUnexpectedEOF))])
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

	require.True(t, transientClasses[errorClass(dnsErr)])
	require.True(t, transientClasses[errorClass(live)], "errors.As must reach the DNSError through fmt.wrapError and errors.joinError")
}

func TestIsTransient_DNSTimeoutIsTransient(t *testing.T) {
	t.Parallel()

	dnsErr := &net.DNSError{Err: "i/o timeout", IsTimeout: true, IsTemporary: false}

	require.True(t, transientClasses[errorClass(dnsErr)], "the gate is the Temporary() method (IsTimeout || IsTemporary), not the IsTemporary field")
}

func TestIsTransient_DNSNotFoundIsNotTransient(t *testing.T) {
	t.Parallel()

	dnsErr := &net.DNSError{Err: "no such host", Name: "artemis-postgresql", IsNotFound: true}

	require.False(t, transientClasses[errorClass(dnsErr)], "NXDOMAIN is permanent for a misconfigured hostname; it pages in its own bucket")
	require.False(t, transientClasses[errorClass(fmt.Errorf("failed to connect: %w", dnsErr))])
}

func TestIsTransient_PlainEOFIsNotTransient(t *testing.T) {
	t.Parallel()

	require.False(t, transientClasses[errorClass(io.EOF)], "a clean end-of-stream is a control value, not a fault")
	require.False(t, transientClasses[errorClass(fmt.Errorf("relay: %w", io.EOF))])
}

func TestIsTransient_Artemis8LiveChainIsTransient(t *testing.T) {
	t.Parallel()

	cfg, err := pgconn.ParseConfig("postgres://artemis:x@artemis-postgresql:5432/artemis?sslmode=disable")
	require.NoError(t, err)
	cfg.LookupFunc = func(context.Context, string) ([]string, error) {
		return nil, &net.DNSError{
			Err:         "server misbehaving",
			Name:        "artemis-postgresql",
			Server:      "10.11.0.10:53",
			IsTemporary: true,
		}
	}

	_, connErr := pgconn.ConnectConfig(context.Background(), cfg)
	require.Error(t, connErr)
	var connectErr *pgconn.ConnectError
	require.ErrorAs(t, connErr, &connectErr,
		"the chain must be the one pgconn really builds, not a hand-rolled lookalike")
	require.Contains(t, connErr.Error(), "hostname resolving error: lookup artemis-postgresql on 10.11.0.10:53: server misbehaving")

	live := fmt.Errorf("pg registry list: %w", connErr)
	require.True(t, transientClasses[errorClass(live)])
	require.Equal(t, classDNSTemporary, errorClass(live))
}

func TestIsTransient_DNSServerMisbehavingIsTransientWhicheverRcode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       *net.DNSError
		wantClass string
	}{
		{"SERVFAIL", &net.DNSError{Err: "server misbehaving", Name: "artemis-postgresql", Server: "10.11.0.10:53", IsTemporary: true}, classDNSTemporary},
		{"other bad rcode", &net.DNSError{Err: "server misbehaving", Name: "artemis-postgresql", Server: "10.11.0.10:53"}, classDNSResolver},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			live := fmt.Errorf("pg registry list: %w", errors.Join(fmt.Errorf("failed to connect: %w", tc.err)))
			require.True(t, transientClasses[errorClass(tc.err)],
				"Go stringifies SERVFAIL and REFUSED identically, so a classification that splits them splits on evidence the operator does not have")
			require.True(t, transientClasses[errorClass(live)])
			require.Equal(t, tc.wantClass, errorClass(live))
		})
	}
}

func TestErrorClass_DNSNotFoundKeepsItsOwnClass(t *testing.T) {
	t.Parallel()

	notFound := &net.DNSError{Err: "no such host", Name: "artemis-postgresql", IsNotFound: true}
	require.Equal(t, classDNSNotFound, errorClass(notFound))
	require.Equal(t, classDNSNotFound, errorClass(fmt.Errorf("failed to connect: %w", notFound)))
	require.False(t, transientClasses[errorClass(notFound)],
		"NXDOMAIN is the one DNS answer no retry can change; folding it into a transient token would silence a misconfigured hostname")

	alsoTemporary := &net.DNSError{Err: "no such host", Name: "artemis-postgresql", IsNotFound: true, IsTemporary: true}
	require.Equal(t, classDNSNotFound, errorClass(alsoTemporary),
		"IsNotFound is tested before Temporary(), so a future stdlib setting both cannot demote NXDOMAIN")
}
