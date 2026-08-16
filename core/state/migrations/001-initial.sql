CREATE TABLE IF NOT EXISTS plans (
    plan_id TEXT PRIMARY KEY,
    operation TEXT NOT NULL,
    plan_digest TEXT NOT NULL,
    body_json BLOB NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('planned', 'approved', 'applying', 'succeeded', 'failed', 'partial', 'stale', 'canceled')
    ),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    inserted_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER IF NOT EXISTS plans_immutable_identity
BEFORE UPDATE OF operation, plan_digest, body_json, created_at, expires_at ON plans
BEGIN
    SELECT RAISE(ABORT, 'immutable plan content');
END;

CREATE TABLE IF NOT EXISTS operations (
    operation_id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL UNIQUE REFERENCES plans(plan_id),
    operation TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('applying', 'succeeded', 'failed', 'partial', 'blocked')
    ),
    actor_json BLOB NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    result_json BLOB
) STRICT;

CREATE TABLE IF NOT EXISTS operation_steps (
    operation_id TEXT NOT NULL REFERENCES operations(operation_id),
    step_id TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    action TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence >= 0),
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'applying', 'succeeded', 'failed', 'blocked', 'compensating', 'compensated')
    ),
    before_json BLOB,
    after_json BLOB,
    last_error TEXT,
    started_at TEXT,
    finished_at TEXT,
    PRIMARY KEY (operation_id, step_id),
    UNIQUE (operation_id, sequence)
) STRICT;

CREATE TABLE IF NOT EXISTS operation_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id TEXT NOT NULL REFERENCES operations(operation_id),
    plan_id TEXT NOT NULL REFERENCES plans(plan_id),
    step_id TEXT,
    event_type TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    payload_json BLOB NOT NULL,
    payload_digest TEXT NOT NULL
) STRICT;

CREATE TRIGGER IF NOT EXISTS operation_events_no_update
BEFORE UPDATE ON operation_events
BEGIN
    SELECT RAISE(ABORT, 'operation events are append-only');
END;

CREATE TRIGGER IF NOT EXISTS operation_events_plan_match
BEFORE INSERT ON operation_events
WHEN NOT EXISTS (
    SELECT 1 FROM operations
    WHERE operation_id = NEW.operation_id AND plan_id = NEW.plan_id
)
BEGIN
    SELECT RAISE(ABORT, 'event plan does not match operation');
END;

CREATE TRIGGER IF NOT EXISTS operation_events_no_delete
BEFORE DELETE ON operation_events
BEGIN
    SELECT RAISE(ABORT, 'operation events are append-only');
END;

CREATE TABLE IF NOT EXISTS locks (
    scope TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    lock_id TEXT NOT NULL UNIQUE,
    operation_id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    pid INTEGER NOT NULL CHECK (pid > 0),
    fencing_token INTEGER NOT NULL CHECK (fencing_token > 0),
    acquired_at TEXT NOT NULL,
    lease_expires_at TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL,
    PRIMARY KEY (scope, scope_id)
) STRICT;

CREATE TABLE IF NOT EXISTS counters (
    name TEXT PRIMARY KEY,
    value INTEGER NOT NULL CHECK (value >= 0)
) STRICT;

INSERT OR IGNORE INTO counters(name, value) VALUES ('lock-fencing-token', 0);

CREATE TABLE IF NOT EXISTS metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS operation_events_operation_sequence
    ON operation_events(operation_id, sequence);

CREATE INDEX IF NOT EXISTS operation_steps_status
    ON operation_steps(operation_id, status, sequence);
