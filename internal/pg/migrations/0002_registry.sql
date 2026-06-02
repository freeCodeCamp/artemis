CREATE TABLE IF NOT EXISTS sites (
    slug       TEXT        PRIMARY KEY,
    teams      TEXT[]      NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT        NOT NULL DEFAULT ''
);
