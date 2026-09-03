package pg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoTxIndexHazards_FlagsALaterTransactionalReference(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"0001_tables.sql": "CREATE TABLE t (id int);\nCREATE INDEX t_idx_a ON t (id);",
		"0002_index.sql":  "-- migrate:no-transaction\nDROP INDEX CONCURRENTLY IF EXISTS t_idx_a;\nCREATE INDEX CONCURRENTLY IF NOT EXISTS t_idx_a ON t (id DESC);\nCREATE UNIQUE INDEX CONCURRENTLY t_idx_b ON t (id);",
		"0003_uses.sql":   "ALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING INDEX t_idx_b;",
		"0004_more.sql":   "-- migrate:no-transaction\nDROP INDEX CONCURRENTLY IF EXISTS t_idx_a;",
		"0005_clean.sql":  "CREATE TABLE u (id int);",
	}
	names := []string{"0001_tables.sql", "0002_index.sql", "0003_uses.sql", "0004_more.sql", "0005_clean.sql"}
	read := func(n string) (string, error) { return files[n], nil }

	hazards, err := noTxIndexHazards(names, read)
	require.NoError(t, err)
	assert.Equal(t, []string{"0003_uses.sql references t_idx_b, which no-transaction 0002_index.sql creates or drops"}, hazards,
		"only a transactional file AFTER the no-tx file is a hazard; 0001 (before) and 0004 (no-tx itself) are not")
}

func TestMigrations_NoTxIndexHazardIsCaughtAtBuild(t *testing.T) {
	t.Parallel()
	names, err := migrationFiles()
	require.NoError(t, err)
	hazards, err := noTxIndexHazards(names, func(n string) (string, error) {
		b, err := migrationsFS.ReadFile("migrations/" + n)
		return string(b), err
	})
	require.NoError(t, err)
	assert.Empty(t, hazards, "a transactional migration must not depend on an index a no-transaction migration builds after boot")
}
