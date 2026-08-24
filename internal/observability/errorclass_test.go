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
