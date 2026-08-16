CREATE TABLE remote_refreshes (
    repository_id TEXT NOT NULL,
    worktree_root TEXT NOT NULL,
    remote TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    head_oid TEXT NOT NULL,
    refs_json BLOB NOT NULL,
    refs_digest TEXT NOT NULL,
    forced_update INTEGER NOT NULL CHECK (forced_update IN (0, 1)),
    PRIMARY KEY (repository_id, worktree_root, remote)
) STRICT;

CREATE INDEX remote_refreshes_observed_at
    ON remote_refreshes(observed_at);
