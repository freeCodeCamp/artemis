package pg

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNoTxMigration_ScansPastAHeaderComment(t *testing.T) {
	t.Parallel()
	assert.True(t, isNoTxMigration("-- 0012: rebuild the audit index\n-- migrate:no-transaction\nCREATE INDEX CONCURRENTLY x ON t (id);"),
		"a header comment above the directive must not disable it")
	assert.True(t, isNoTxMigration("\n\n-- migrate:no-transaction\nSELECT 1;"))
	assert.True(t, isNoTxMigration("-- MIGRATE:NO-TRANSACTION\nSELECT 1;"))
	assert.False(t, isNoTxMigration("CREATE TABLE t (id int);\n-- migrate:no-transaction\n"),
		"a directive after the first statement is not a directive")
	assert.False(t, isNoTxMigration("-- plain comment\nCREATE TABLE t (id int);"))
	assert.False(t, isNoTxMigration(""))
	assert.False(t, isNoTxMigration("/* block header */\n-- migrate:no-transaction\nSELECT 1;"),
		"a block comment stops the scan; only -- comments may precede the directive")
}
