package pg

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsLockTimeout(t *testing.T) {
	lockTO := &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}
	if !IsLockTimeout(lockTO) {
		t.Error("bare 55P03 must be a lock timeout")
	}
	if !IsLockTimeout(fmt.Errorf("site lock foo: %w", lockTO)) {
		t.Error("wrapped 55P03 must be a lock timeout")
	}
	if IsLockTimeout(&pgconn.PgError{Code: "57P03"}) {
		t.Error("57P03 is not a lock timeout")
	}
	if IsLockTimeout(errors.New("plain")) {
		t.Error("non-pg error is not a lock timeout")
	}
	if IsLockTimeout(nil) {
		t.Error("nil is not a lock timeout")
	}
}

func TestIsInRecovery(t *testing.T) {
	rec := &pgconn.PgError{Code: "57P03", Message: "the database system is in recovery mode"}
	if !IsInRecovery(rec) {
		t.Error("bare 57P03 must be in-recovery")
	}
	if !IsInRecovery(fmt.Errorf("relay: fetch: %w", rec)) {
		t.Error("wrapped 57P03 must be in-recovery")
	}
	if IsInRecovery(&pgconn.PgError{Code: "55P03"}) {
		t.Error("55P03 is not in-recovery")
	}
	if IsInRecovery(nil) {
		t.Error("nil is not in-recovery")
	}
}
