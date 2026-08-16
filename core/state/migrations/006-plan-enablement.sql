CREATE TABLE plan_enablements (
    enablement_id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL UNIQUE REFERENCES plans(plan_id),
    plan_digest TEXT NOT NULL,
    approval_id TEXT NOT NULL,
    approval_digest TEXT NOT NULL,
    device_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    maximum_starts INTEGER NOT NULL CHECK (maximum_starts = 1),
    starts INTEGER NOT NULL DEFAULT 0 CHECK (starts IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('active', 'consumed', 'expired')),
    consumed_at TEXT,
    operation_id TEXT UNIQUE,
    CHECK (
        (status = 'active' AND starts = 0 AND consumed_at IS NULL AND operation_id IS NULL) OR
        (status = 'consumed' AND starts = 1 AND consumed_at IS NOT NULL AND operation_id IS NOT NULL) OR
        (status = 'expired' AND starts = 0 AND consumed_at IS NULL AND operation_id IS NULL)
    )
) STRICT;

CREATE TRIGGER plan_enablements_immutable_identity
BEFORE UPDATE OF enablement_id, plan_id, plan_digest, approval_id, approval_digest,
    device_id, session_id, created_at, expires_at, maximum_starts
ON plan_enablements
BEGIN
    SELECT RAISE(ABORT, 'immutable plan enablement identity');
END;

CREATE INDEX plan_enablements_status_expiry
    ON plan_enablements(status, expires_at);
