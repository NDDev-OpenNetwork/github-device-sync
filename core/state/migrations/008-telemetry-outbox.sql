CREATE TABLE IF NOT EXISTS telemetry_outbox (
    event_id TEXT PRIMARY KEY,
    signal_type TEXT NOT NULL,
    body_json TEXT NOT NULL,
    body_bytes INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_error_class TEXT NOT NULL DEFAULT '',
    CHECK (signal_type IN ('log', 'metric', 'trace')),
    CHECK (json_valid(body_json)),
    CHECK (body_bytes BETWEEN 2 AND 1048576),
    CHECK (status IN ('pending', 'sent', 'dropped')),
    CHECK (attempts BETWEEN 0 AND 100)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_telemetry_outbox_delivery
ON telemetry_outbox(status, next_attempt_at, created_at);
