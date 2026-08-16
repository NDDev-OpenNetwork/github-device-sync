CREATE TABLE IF NOT EXISTS accepted_bundles (
    trust_domain TEXT NOT NULL,
    release_sequence INTEGER NOT NULL CHECK (release_sequence > 0),
    bundle_version TEXT NOT NULL,
    artifact_digest TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    attestation_identity_digest TEXT NOT NULL,
    accepted_at TEXT NOT NULL,
    rollback_approval_ref TEXT,
    rollback_authorization_json BLOB,
    PRIMARY KEY (trust_domain, release_sequence)
) STRICT;

CREATE TRIGGER IF NOT EXISTS accepted_bundles_append_only_update
BEFORE UPDATE ON accepted_bundles
BEGIN
    SELECT RAISE(ABORT, 'accepted bundle ledger is append-only');
END;

CREATE TRIGGER IF NOT EXISTS accepted_bundles_append_only_delete
BEFORE DELETE ON accepted_bundles
BEGIN
    SELECT RAISE(ABORT, 'accepted bundle ledger is append-only');
END;

CREATE TABLE IF NOT EXISTS rollouts (
    rollout_id TEXT PRIMARY KEY,
    plan_json BLOB NOT NULL,
    plan_digest TEXT NOT NULL,
    target_set_digest TEXT NOT NULL,
    bundle_sequence INTEGER NOT NULL CHECK (bundle_sequence > 0),
    bundle_digest TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('planned', 'active', 'paused', 'completed', 'failed', 'rolling-back', 'rolled-back')
    ),
    current_wave INTEGER NOT NULL DEFAULT -1 CHECK (current_wave >= -1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER IF NOT EXISTS rollouts_immutable_plan
BEFORE UPDATE OF rollout_id, plan_json, plan_digest, target_set_digest,
    bundle_sequence, bundle_digest, created_at
ON rollouts
BEGIN
    SELECT RAISE(ABORT, 'immutable rollout plan content');
END;

CREATE TABLE IF NOT EXISTS rollout_targets (
    rollout_id TEXT NOT NULL REFERENCES rollouts(rollout_id) ON DELETE RESTRICT,
    wave_ordinal INTEGER NOT NULL CHECK (wave_ordinal >= 0),
    repository_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'active', 'pull-request-open', 'checks-pending',
                   'succeeded', 'failed', 'quarantined', 'rolled-back')
    ),
    result_json BLOB,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (rollout_id, repository_id)
) STRICT;

CREATE INDEX IF NOT EXISTS rollout_targets_wave
    ON rollout_targets(rollout_id, wave_ordinal, status, repository_id);

CREATE TABLE IF NOT EXISTS rollout_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    rollout_id TEXT NOT NULL REFERENCES rollouts(rollout_id) ON DELETE RESTRICT,
    wave_ordinal INTEGER,
    repository_id TEXT,
    event_type TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    body_json BLOB NOT NULL
) STRICT;

CREATE TRIGGER IF NOT EXISTS rollout_events_append_only_update
BEFORE UPDATE ON rollout_events
BEGIN
    SELECT RAISE(ABORT, 'rollout journal is append-only');
END;

CREATE TRIGGER IF NOT EXISTS rollout_events_append_only_delete
BEFORE DELETE ON rollout_events
BEGIN
    SELECT RAISE(ABORT, 'rollout journal is append-only');
END;
