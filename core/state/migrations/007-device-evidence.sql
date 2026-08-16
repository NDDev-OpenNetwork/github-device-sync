CREATE TABLE IF NOT EXISTS device_evidence (
    evidence_id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    evidence_digest TEXT NOT NULL UNIQUE,
    body_json TEXT NOT NULL,
    inserted_at TEXT NOT NULL,
    CHECK (length(evidence_id) BETWEEN 1 AND 256),
    CHECK (length(device_id) BETWEEN 1 AND 256),
    CHECK (evidence_digest LIKE 'sha256:%'),
    CHECK (json_valid(body_json))
) STRICT;

CREATE INDEX IF NOT EXISTS idx_device_evidence_latest
ON device_evidence(device_id, observed_at DESC);
