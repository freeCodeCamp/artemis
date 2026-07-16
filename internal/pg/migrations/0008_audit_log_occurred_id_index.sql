-- migrate:no-transaction
DROP INDEX CONCURRENTLY IF EXISTS audit_log_occurred_id_idx;
CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_log_occurred_id_idx ON audit_log (occurred_at DESC, id DESC);
DROP INDEX CONCURRENTLY IF EXISTS audit_log_occurred_at_idx;
