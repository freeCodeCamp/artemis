CREATE TABLE IF NOT EXISTS repo_requests (
    id            TEXT        PRIMARY KEY,
    name          TEXT        NOT NULL,
    owner         TEXT        NOT NULL DEFAULT '',
    visibility    TEXT        NOT NULL DEFAULT 'private',
    description   TEXT        NOT NULL DEFAULT '',
    template      TEXT        NOT NULL DEFAULT '',
    status        TEXT        NOT NULL,
    url           TEXT        NOT NULL DEFAULT '',
    error         TEXT        NOT NULL DEFAULT '',
    requested_by  TEXT        NOT NULL DEFAULT '',
    approver      TEXT        NOT NULL DEFAULT '',
    reject_reason TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS repo_requests_name_claim
    ON repo_requests (lower(name))
    WHERE status IN ('pending', 'approved', 'active');

CREATE INDEX IF NOT EXISTS repo_requests_created_idx ON repo_requests (created_at, id);
