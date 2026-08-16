package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const maxRemoteRefreshRefsBytes = 4 << 20

type RemoteRefreshRecord struct {
	RepositoryID string          `json:"repository_id"`
	WorktreeRoot string          `json:"worktree_root"`
	Remote       string          `json:"remote"`
	ObservedAt   time.Time       `json:"observed_at"`
	HeadOID      string          `json:"head_oid"`
	Refs         json.RawMessage `json:"refs"`
	RefsDigest   string          `json:"refs_digest"`
	ForcedUpdate bool            `json:"forced_update"`
}

func (store *Store) PutRemoteRefresh(
	ctx context.Context,
	record RemoteRefreshRecord,
) (RemoteRefreshRecord, error) {
	if store.readOnly {
		return RemoteRefreshRecord{}, ErrReadOnly
	}
	root, err := validateRemoteRefreshIdentity(
		record.RepositoryID, record.WorktreeRoot, record.Remote,
	)
	if err != nil || record.HeadOID == "" || record.ObservedAt.IsZero() ||
		len(record.Refs) == 0 || len(record.Refs) > maxRemoteRefreshRefsBytes ||
		!json.Valid(record.Refs) {
		return RemoteRefreshRecord{}, fmt.Errorf("invalid remote refresh record")
	}
	compact, digest, err := normalizeCursor(record.Refs)
	if err != nil {
		return RemoteRefreshRecord{}, err
	}
	record.WorktreeRoot = root
	record.Refs = compact
	record.RefsDigest = digest
	forced := 0
	if record.ForcedUpdate {
		forced = 1
	}
	result, err := store.db.ExecContext(
		ctx,
		`INSERT INTO remote_refreshes(
		    repository_id, worktree_root, remote, observed_at, head_oid,
		    refs_json, refs_digest, forced_update
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repository_id, worktree_root, remote) DO UPDATE SET
		    observed_at = excluded.observed_at,
		    head_oid = excluded.head_oid,
		    refs_json = excluded.refs_json,
		    refs_digest = excluded.refs_digest,
		    forced_update = excluded.forced_update
		 WHERE excluded.observed_at > remote_refreshes.observed_at
		    OR (
		      excluded.observed_at = remote_refreshes.observed_at
		      AND excluded.refs_digest = remote_refreshes.refs_digest
		      AND excluded.head_oid = remote_refreshes.head_oid
		      AND excluded.forced_update = remote_refreshes.forced_update
		    )`,
		record.RepositoryID, record.WorktreeRoot, record.Remote,
		formatTime(record.ObservedAt), record.HeadOID, []byte(record.Refs),
		record.RefsDigest, forced,
	)
	if err != nil {
		return RemoteRefreshRecord{}, fmt.Errorf("store remote refresh: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return RemoteRefreshRecord{}, err
	}
	if changed != 1 {
		return RemoteRefreshRecord{}, ErrStateConflict
	}
	loaded, err := store.GetRemoteRefresh(
		ctx, record.RepositoryID, record.WorktreeRoot, record.Remote,
	)
	if err != nil {
		return RemoteRefreshRecord{}, err
	}
	if loaded.ObservedAt.Equal(record.ObservedAt) &&
		(loaded.RefsDigest != record.RefsDigest || loaded.HeadOID != record.HeadOID ||
			loaded.ForcedUpdate != record.ForcedUpdate) {
		return RemoteRefreshRecord{}, ErrStateConflict
	}
	return loaded, nil
}

func (store *Store) GetRemoteRefresh(
	ctx context.Context,
	repositoryID string,
	worktreeRoot string,
	remote string,
) (RemoteRefreshRecord, error) {
	root, err := validateRemoteRefreshIdentity(repositoryID, worktreeRoot, remote)
	if err != nil {
		return RemoteRefreshRecord{}, fmt.Errorf("invalid remote refresh identity")
	}
	var record RemoteRefreshRecord
	var observedAt string
	var refs []byte
	var forced int
	err = store.db.QueryRowContext(
		ctx,
		`SELECT repository_id, worktree_root, remote, observed_at, head_oid,
		        refs_json, refs_digest, forced_update
		 FROM remote_refreshes
		 WHERE repository_id = ? AND worktree_root = ? AND remote = ?`,
		repositoryID, root, remote,
	).Scan(
		&record.RepositoryID, &record.WorktreeRoot, &record.Remote, &observedAt,
		&record.HeadOID, &refs, &record.RefsDigest, &forced,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteRefreshRecord{}, ErrNotFound
	}
	if err != nil {
		return RemoteRefreshRecord{}, fmt.Errorf("load remote refresh: %w", err)
	}
	record.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return RemoteRefreshRecord{}, fmt.Errorf("decode remote refresh time: %w", err)
	}
	if len(refs) == 0 || len(refs) > maxRemoteRefreshRefsBytes || !json.Valid(refs) {
		return RemoteRefreshRecord{}, fmt.Errorf("stored remote refresh refs are invalid")
	}
	record.Refs = append(json.RawMessage(nil), refs...)
	record.ForcedUpdate = forced == 1
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(refs))
	if digest != record.RefsDigest {
		return RemoteRefreshRecord{}, fmt.Errorf("stored remote refresh digest mismatch")
	}
	return record, nil
}

func validateRemoteRefreshIdentity(
	repositoryID string,
	worktreeRoot string,
	remote string,
) (string, error) {
	if repositoryID == "" || remote == "" || worktreeRoot == "" ||
		strings.ContainsAny(repositoryID+remote+worktreeRoot, "\x00\r\n") ||
		len(repositoryID) > 256 || len(remote) > 256 || len(worktreeRoot) > 4096 ||
		!filepath.IsAbs(worktreeRoot) || filepath.Clean(worktreeRoot) != worktreeRoot {
		return "", fmt.Errorf("invalid remote refresh identity")
	}
	return worktreeRoot, nil
}
