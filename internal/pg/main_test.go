package pg

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	testPostgresImage = "postgres:16-alpine"
	testAdminDB       = "artemis_test"
	testTemplateDB    = "artemis_tmpl"
)

var (
	sharedOnce      sync.Once
	sharedContainer *postgres.PostgresContainer
	sharedAdmin     *pgxpool.Pool
	sharedBaseURL   string
	sharedErr       error
	testDBSeq       atomic.Uint64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedAdmin != nil {
		sharedAdmin.Close()
	}
	if sharedContainer != nil {
		_ = sharedContainer.Terminate(context.Background())
	}
	os.Exit(code)
}

func startSharedPostgres() {
	ctx := context.Background()
	container, err := postgres.Run(ctx, testPostgresImage,
		postgres.WithDatabase(testAdminDB),
		postgres.WithUsername("artemis"),
		postgres.WithPassword("artemis"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		sharedErr = fmt.Errorf("start postgres: %w", err)
		return
	}
	sharedContainer = container

	sharedBaseURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		sharedErr = fmt.Errorf("connection string: %w", err)
		return
	}
	if sharedAdmin, err = pgxpool.New(ctx, sharedBaseURL); err != nil {
		sharedErr = fmt.Errorf("admin pool: %w", err)
		return
	}
	if _, err = sharedAdmin.Exec(ctx, "CREATE DATABASE "+testTemplateDB); err != nil {
		sharedErr = fmt.Errorf("create template: %w", err)
		return
	}
	tmpl, err := pgxpool.New(ctx, dsnFor(testTemplateDB))
	if err != nil {
		sharedErr = fmt.Errorf("template pool: %w", err)
		return
	}
	err = Migrate(ctx, tmpl)
	tmpl.Close()
	if err != nil {
		sharedErr = fmt.Errorf("migrate template: %w", err)
	}
}

func dsnFor(name string) string {
	return fmt.Sprintf("postgres://artemis:artemis@%s/%s?sslmode=disable", hostPortOf(sharedBaseURL), name)
}

func hostPortOf(url string) string {
	rest := url[len("postgres://artemis:artemis@"):]
	for i := range rest {
		if rest[i] == '/' {
			return rest[:i]
		}
	}
	return rest
}

// newTestDatabase returns a DSN for a database of its own, cloned from the
// migrated template. One container serves the package; the clone costs a
// CREATE DATABASE rather than a container start.
func newTestDatabase(t *testing.T, template string) string {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	sharedOnce.Do(startSharedPostgres)
	require.NoError(t, sharedErr)

	name := fmt.Sprintf("t%d", testDBSeq.Add(1))
	stmt := "CREATE DATABASE " + name
	if template != "" {
		stmt += " TEMPLATE " + template
	}
	_, err := sharedAdmin.Exec(context.Background(), stmt)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sharedAdmin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return dsnFor(name)
}

func migratedTestDSN(t *testing.T) string { return newTestDatabase(t, testTemplateDB) }

func blankTestDSN(t *testing.T) string { return newTestDatabase(t, "") }
