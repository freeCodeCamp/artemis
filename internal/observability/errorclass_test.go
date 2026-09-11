package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestErrorClass_Artemis7ShapesGetDistinctClasses(t *testing.T) {
	t.Parallel()

	shapes := []error{
		fmt.Errorf("outbox fetch: %w", &pgconn.PgError{Code: "57P03"}),
		fmt.Errorf("pg registry list: %w", io.ErrUnexpectedEOF),
		fmt.Errorf("relay: %w", pgconn.ErrConnClosed),
		fmt.Errorf("pg registry list: %w", errors.Join(fmt.Errorf("failed to connect: %w", &net.DNSError{
			Err:         "server misbehaving",
			Name:        "artemis-postgresql",
			Server:      "10.11.0.10:53",
			IsTemporary: true,
		}))),
	}

	seen := make(map[string]bool, len(shapes))
	for _, err := range shapes {
		class := errorClass(err)
		require.NotEmpty(t, class)
		seen[class] = true
	}

	require.Len(t, seen, len(shapes), "the four shapes merged into ARTEMIS-7 must classify apart")
}

func TestErrorClass_SameFaultDifferentHostsShareOneClass(t *testing.T) {
	t.Parallel()

	first := &net.DNSError{Err: "server misbehaving", Name: "artemis-postgresql", Server: "10.11.0.10:53", IsTemporary: true}
	second := &net.DNSError{Err: "server misbehaving", Name: "artemis-postgresql-1", Server: "10.11.0.11:53", IsTemporary: true}

	require.Equal(t, errorClass(first), errorClass(second),
		"the discriminator is the class, never the message, which embeds hosts, ports, ids and durations")
}

func TestErrorClass_DNSShapesGetDistinctClasses(t *testing.T) {
	t.Parallel()

	shapes := []error{
		fmt.Errorf("pg registry list: %w", &net.DNSError{Err: "server misbehaving", Name: "artemis-postgresql", Server: "10.11.0.10:53", IsTemporary: true}),
		fmt.Errorf("pg registry list: %w", &net.DNSError{Err: "server misbehaving", Name: "artemis-postgresql", Server: "10.11.0.10:53"}),
		fmt.Errorf("pg registry list: %w", &net.DNSError{Err: "no such host", Name: "artemis-postgresql", IsNotFound: true}),
	}

	seen := make(map[string]bool, len(shapes))
	for _, err := range shapes {
		class := errorClass(err)
		require.NotEmpty(t, class)
		seen[class] = true
	}

	require.Len(t, seen, len(shapes), "three DNS faults with three different remedies must not share one Sentry issue")
}

func TestErrorClass_PlainErrorIsNotAGRPCStatus(t *testing.T) {
	t.Parallel()

	require.Equal(t, classUnclassified, errorClass(errors.New("genuine gc failure")),
		"status.Code returns codes.Unknown for every non-gRPC error; the ok gate keeps the tail out of grpc.*")
}

func TestErrorClass_ContextDeadlineBeatsDNS(t *testing.T) {
	t.Parallel()

	dnsErr := &net.DNSError{Err: "i/o timeout", UnwrapErr: context.DeadlineExceeded, IsTimeout: true}

	require.Equal(t, classCtxDeadline, errorClass(dnsErr), "classification order is behaviour: context is tested first")
}

func TestErrorClass_DialFaultsAreOneTransientClass(t *testing.T) {
	t.Parallel()

	addr := &net.TCPAddr{IP: net.ParseIP("10.11.196.31"), Port: 5432}
	shapes := []error{
		&net.OpError{Op: "dial", Net: "tcp", Addr: addr, Err: os.NewSyscallError("connect", syscall.EPERM)},
		&net.OpError{Op: "dial", Net: "tcp", Addr: addr, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)},
		&net.OpError{Op: "dial", Net: "tcp", Addr: addr, Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)},
	}

	for _, err := range shapes {
		wrapped := fmt.Errorf("relay outbox fetch: %w", err)
		require.Equal(t, classNetDial, errorClass(wrapped),
			"a CloudNativePG instance roll moves the artemis-pg-rw endpoint and every dial fault in that window has one remedy: wait")
		require.True(t, transientClasses[errorClass(wrapped)],
			"an unclassified dial fault pages on every occurrence; the roll produced nine events in one window on 2026-09-11")
	}
}

func TestErrorClass_DNSFaultOutranksTheDialWrapper(t *testing.T) {
	t.Parallel()

	err := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: &net.DNSError{Err: "no such host", Name: "artemis-pg-rw", IsNotFound: true},
	}

	require.Equal(t, classDNSNotFound, errorClass(err),
		"NXDOMAIN names a configuration fault and keeps its own page; the dial wrapper must not swallow it")
}

func TestErrorClass_NonDialOpErrorStaysUnclassified(t *testing.T) {
	t.Parallel()

	err := &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}

	require.Equal(t, classUnclassified, errorClass(err),
		"only the dial phase is covered; a mid-stream fault has a different remedy")
}
