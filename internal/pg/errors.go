package pg

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

func pgCode(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, true
	}
	return "", false
}

func IsLockTimeout(err error) bool {
	code, ok := pgCode(err)
	return ok && code == "55P03"
}

func IsInRecovery(err error) bool {
	code, ok := pgCode(err)
	return ok && code == "57P03"
}
