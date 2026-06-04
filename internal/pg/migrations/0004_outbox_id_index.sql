DROP INDEX IF EXISTS outbox_unpublished_idx;

CREATE INDEX IF NOT EXISTS outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;
