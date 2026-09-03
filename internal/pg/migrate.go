package pg

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrateAdvisoryLockKey = 8472013

const migrateConcurrentLockKey = 8472015

const noTxDirective = "-- migrate:no-transaction"

func releaseAdvisoryLock(conn *pgxpool.Conn, key int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", key)
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateAdvisoryLockKey); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer releaseAdvisoryLock(conn, migrateAdvisoryLockKey)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT        PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrate: ensure ledger: %w", err)
	}

	names, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, name := range names {
		applied, err := migrationApplied(ctx, conn, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		if isNoTxMigration(string(body)) {
			if next := pendingTransactionalAfter(ctx, conn, names, name); next != "" {
				slog.WarnContext(ctx, "migrate.notx.deferred_ahead_of", "version", name, "next", next)
			}
			continue
		}
		if err := applyMigration(ctx, conn, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func pendingTransactionalAfter(ctx context.Context, conn *pgxpool.Conn, names []string, after string) string {
	past := false
	for _, n := range names {
		if n == after {
			past = true
			continue
		}
		if !past {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + n)
		if err != nil {
			return ""
		}
		if isNoTxMigration(string(body)) {
			continue
		}
		applied, err := migrationApplied(ctx, conn, n)
		if err != nil {
			return ""
		}
		if !applied {
			return n
		}
	}
	return ""
}

var (
	indexDDL      = regexp.MustCompile(`(?i)\b(?:CREATE\s+(?:UNIQUE\s+)?INDEX|DROP\s+INDEX|ALTER\s+INDEX|REINDEX\s+(?:CONCURRENTLY\s+)?INDEX)\s+(?:CONCURRENTLY\s+)?(?:IF\s+(?:NOT\s+)?EXISTS\s+)?((?:(?:[A-Za-z_][A-Za-z0-9_]*\.)?(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_]*))(?:\s*,\s*(?:[A-Za-z_][A-Za-z0-9_]*\.)?(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_]*))*)`)
	indexKeywords = map[string]bool{"on": true, "if": true, "exists": true, "concurrently": true, "unique": true}
)

func indexNames(stmt string) []string {
	var names []string
	for _, m := range indexDDL.FindAllStringSubmatch(stmt, -1) {
		for _, raw := range strings.Split(m[1], ",") {
			name := strings.TrimSpace(raw)
			if i := strings.LastIndex(name, "."); i >= 0 && !strings.HasPrefix(name, `"`) {
				name = name[i+1:]
			}
			if strings.HasPrefix(name, `"`) {
				name = strings.Trim(name, `"`)
			} else {
				name = strings.ToLower(name)
			}
			if name == "" || indexKeywords[name] {
				continue
			}
			names = append(names, name)
		}
	}
	return names
}

func noTxIndexHazards(names []string, read func(string) (string, error)) ([]string, error) {
	type owned struct{ index, file string }
	seen := map[owned]bool{}
	var built []owned
	var hazards []string
	for _, name := range names {
		body, err := read(name)
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", name, err)
		}
		sql := strings.Join(splitStatements(body), ";\n")
		if isNoTxMigration(body) {
			for _, idx := range indexNames(sql) {
				o := owned{index: idx, file: name}
				if !seen[o] {
					seen[o] = true
					built = append(built, o)
				}
			}
			continue
		}
		lower := strings.ToLower(sql)
		for _, o := range built {
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(strings.ToLower(o.index)) + `\b`).MatchString(lower) {
				hazards = append(hazards, fmt.Sprintf("%s references %s, which no-transaction %s creates or drops", name, o.index, o.file))
			}
		}
	}
	return hazards, nil
}

func MigrateConcurrent(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate(concurrent): acquire: %w", err)
	}
	defer conn.Release()

	var locked bool
	if err := conn.QueryRow(ctx,
		"SELECT pg_try_advisory_lock($1)", migrateConcurrentLockKey).Scan(&locked); err != nil {
		return fmt.Errorf("migrate(concurrent): try-lock: %w", err)
	}
	if !locked {
		slog.InfoContext(ctx, "migrate.concurrent.skipped", "reason", "another replica holds the build lock")
		return nil
	}
	defer releaseAdvisoryLock(conn, migrateConcurrentLockKey)

	names, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, name := range names {
		applied, err := migrationApplied(ctx, conn, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("migrate(concurrent): read %s: %w", name, err)
		}
		if !isNoTxMigration(string(body)) {
			continue
		}
		if err := applyMigrationNoTx(ctx, conn, name, string(body)); err != nil {
			return err
		}
		slog.InfoContext(ctx, "migrate.concurrent.applied", "version", name)
	}
	return nil
}

func isNoTxMigration(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.EqualFold(t, noTxDirective) {
			return true
		}
		if !strings.HasPrefix(t, "--") {
			return false
		}
	}
	return false
}

func splitStatements(body string) []string {
	out := make([]string, 0, 4)
	for _, raw := range strings.Split(body, ";") {
		kept := make([]string, 0, 4)
		for _, line := range strings.Split(raw, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			kept = append(kept, line)
		}
		if stmt := strings.TrimSpace(strings.Join(kept, "\n")); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

func applyMigrationNoTx(ctx context.Context, conn *pgxpool.Conn, version, body string) error {
	for _, stmt := range splitStatements(body) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: apply(no-tx) %s: %w", version, err)
		}
	}
	if _, err := conn.Exec(ctx,
		"INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
		return fmt.Errorf("migrate: record %s: %w", version, err)
	}
	return nil
}

func migrationApplied(ctx context.Context, conn *pgxpool.Conn, version string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("migrate: check %s: %w", version, err)
	}
	return exists, nil
}

func applyMigration(ctx context.Context, conn *pgxpool.Conn, version, body string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrate: begin %s: %w", version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, body); err != nil {
		return fmt.Errorf("migrate: apply %s: %w", version, err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
		return fmt.Errorf("migrate: record %s: %w", version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate: commit %s: %w", version, err)
	}
	return nil
}

func migrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: list: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
