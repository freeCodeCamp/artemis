ALTER TABLE audit_log ADD COLUMN request_id TEXT NOT NULL DEFAULT '';

CREATE INDEX audit_log_actor_idx ON audit_log (actor);
