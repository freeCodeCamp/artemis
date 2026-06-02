CREATE TABLE IF NOT EXISTS deploys (
    site       TEXT        NOT NULL,
    id         TEXT        NOT NULL,
    mtime      TIMESTAMPTZ NOT NULL,
    bytes      BIGINT      NOT NULL DEFAULT 0,
    has_marker BOOLEAN     NOT NULL DEFAULT FALSE,
    state      TEXT        NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (site, id)
);

CREATE INDEX IF NOT EXISTS deploys_site_mtime_idx ON deploys (site, mtime DESC);

CREATE TABLE IF NOT EXISTS aliases (
    site       TEXT        NOT NULL,
    name       TEXT        NOT NULL CHECK (name IN ('production', 'preview')),
    deploy_id  TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (site, name)
);

CREATE TABLE IF NOT EXISTS tombstones (
    site       TEXT        NOT NULL,
    id         TEXT        NOT NULL,
    trashed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    bytes      BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (site, id)
);

CREATE TABLE IF NOT EXISTS outbox (
    id           BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic        TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS outbox_unpublished_idx ON outbox (created_at) WHERE published_at IS NULL;
