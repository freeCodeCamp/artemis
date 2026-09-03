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
	require.Len(t, hazards, 1, "only a transactional file AFTER the no-tx file is a hazard; 0001 (before) and 0004 (no-tx itself) are not")
	assert.Contains(t, hazards[0], "0003_uses.sql")
	assert.Contains(t, hazards[0], "t_idx_b")
	assert.Contains(t, hazards[0], "0002_index.sql")
}

func TestIndexNames_ReadsRealDDLForms(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"audit_log_idx"}, indexNames(`DROP INDEX CONCURRENTLY IF EXISTS public.audit_log_idx;`), "schema qualifier stripped")
	assert.Equal(t, []string{"Idx"}, indexNames(`CREATE INDEX CONCURRENTLY "Idx" ON t (id);`), "quoted identifier kept verbatim")
	assert.Equal(t, []string{"audit log idx"}, indexNames(`DROP INDEX CONCURRENTLY IF EXISTS "audit log idx";`))
	assert.Empty(t, indexNames(`CREATE INDEX ON audit_log (occurred_at);`), "an unnamed index records nothing")
	assert.Equal(t, []string{"a_idx", "b_idx"}, indexNames(`DROP INDEX CONCURRENTLY IF EXISTS a_idx, b_idx;`))
	assert.Equal(t, []string{"foo_idx"}, indexNames(`CREATE UNIQUE INDEX CONCURRENTLY Foo_Idx ON t (id);`), "unquoted names fold to lower case")
	assert.Equal(t, []string{"x"}, indexNames(`REINDEX INDEX CONCURRENTLY x;`))
	assert.Equal(t, []string{"old_idx"}, indexNames(`ALTER INDEX old_idx RENAME TO new_idx;`))
}

func TestNoTxIndexHazards_IgnoresCommentsAndFoldsCase(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"0001_idx.sql": "-- migrate:no-transaction\n-- DROP INDEX CONCURRENTLY legacy_idx was here\nCREATE INDEX CONCURRENTLY Foo_Idx ON t (id);",
		"0002_a.sql":   "-- replaces foo_idx and legacy_idx\nCREATE TABLE u (id int);",
		"0003_b.sql":   "ALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING INDEX FOO_IDX;",
		"0004_c.sql":   "CREATE TABLE v (legacy_idx int);",
	}
	names := []string{"0001_idx.sql", "0002_a.sql", "0003_b.sql", "0004_c.sql"}
	hazards, err := noTxIndexHazards(names, func(n string) (string, error) { return files[n], nil })
	require.NoError(t, err)
	require.Len(t, hazards, 1, "a comment mention is not a reference; a commented-out DROP owns nothing; an unquoted name matches case-insensitively")
	assert.Contains(t, hazards[0], "0003_b.sql")
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
