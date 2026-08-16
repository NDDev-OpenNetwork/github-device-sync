CREATE TABLE IF NOT EXISTS webhook_deliveries (
    delivery_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    payload_json BLOB NOT NULL,
    payload_digest TEXT NOT NULL,
    received_at TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('queued', 'processing', 'succeeded', 'failed', 'dead-letter')
    ),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    available_at TEXT NOT NULL,
    claimed_at TEXT,
    finished_at TEXT,
    last_error TEXT
) STRICT;

CREATE TRIGGER IF NOT EXISTS webhook_deliveries_immutable_identity
BEFORE UPDATE OF delivery_id, event_type, payload_json, payload_digest, received_at
ON webhook_deliveries
BEGIN
    SELECT RAISE(ABORT, 'immutable webhook delivery content');
END;

CREATE INDEX IF NOT EXISTS webhook_deliveries_queue
    ON webhook_deliveries(status, available_at, received_at, delivery_id);

CREATE TABLE IF NOT EXISTS repository_observations (
    installation_id TEXT NOT NULL,
    provider_repository_id INTEGER NOT NULL CHECK (provider_repository_id > 0),
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    access_state TEXT NOT NULL CHECK (
        access_state IN ('available', 'inaccessible', 'auth-failed', 'not-found', 'unknown')
    ),
    observed_at TEXT NOT NULL,
    etag TEXT,
    body_json BLOB,
    body_digest TEXT,
    request_id TEXT,
    PRIMARY KEY (installation_id, provider_repository_id)
) STRICT;

CREATE TABLE IF NOT EXISTS reconciliation_runs (
    reconciliation_id TEXT PRIMARY KEY,
    scope TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('running', 'succeeded', 'failed', 'partial', 'blocked')
    ),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    cursor_json BLOB,
    result_json BLOB,
    last_error TEXT
) STRICT;
