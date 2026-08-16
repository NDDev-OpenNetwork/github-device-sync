package state

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func remoteRefreshFixture(t *testing.T, observedAt time.Time) RemoteRefreshRecord {
	t.Helper()
	return RemoteRefreshRecord{
		RepositoryID: "repo_01JEXAMPZ0000000000000000J",
		WorktreeRoot: filepath.Join(t.TempDir(), "checkout"),
		Remote:       "origin",
		ObservedAt:   observedAt,
		HeadOID:      "1111111111111111111111111111111111111111",
		Refs: json.RawMessage(
			`[{"name":"refs/remotes/origin/main","oid":"2222222222222222222222222222222222222222"}]`,
		),
	}
}

func TestRemoteRefreshIsFreshnessOrderedAndIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	record := remoteRefreshFixture(t, testTime)
	first, err := store.PutRemoteRefresh(ctx, record)
	if err != nil || first.RefsDigest == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := store.PutRemoteRefresh(ctx, record)
	if err != nil || second.RefsDigest != first.RefsDigest {
		t.Fatalf("idempotent refresh=%#v err=%v", second, err)
	}

	conflict := record
	conflict.HeadOID = "3333333333333333333333333333333333333333"
	if _, err := store.PutRemoteRefresh(ctx, conflict); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("same-time conflict error=%v", err)
	}
	older := record
	older.ObservedAt = testTime.Add(-time.Second)
	if _, err := store.PutRemoteRefresh(ctx, older); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale refresh error=%v", err)
	}
	newer := record
	newer.ObservedAt = testTime.Add(time.Second)
	newer.ForcedUpdate = true
	newer.Refs = json.RawMessage(
		`[{"name":"refs/remotes/origin/main","oid":"4444444444444444444444444444444444444444"}]`,
	)
	loaded, err := store.PutRemoteRefresh(ctx, newer)
	if err != nil || !loaded.ObservedAt.Equal(newer.ObservedAt) || !loaded.ForcedUpdate ||
		loaded.RefsDigest == first.RefsDigest {
		t.Fatalf("newer refresh=%#v err=%v", loaded, err)
	}

	summary, err := store.Summary(ctx)
	if err != nil || summary.RemoteRefreshes != 1 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
}

func TestRemoteRefreshRejectsInvalidIdentityAndDetectsTampering(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	record := remoteRefreshFixture(t, testTime)
	relative := record
	relative.WorktreeRoot = "relative/checkout"
	if _, err := store.PutRemoteRefresh(ctx, relative); err == nil {
		t.Fatal("relative worktree root was accepted")
	}
	if _, err := store.PutRemoteRefresh(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`UPDATE remote_refreshes SET refs_json = ?
		 WHERE repository_id = ? AND worktree_root = ? AND remote = ?`,
		[]byte(`[]`), record.RepositoryID, record.WorktreeRoot, record.Remote,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRemoteRefresh(
		ctx, record.RepositoryID, record.WorktreeRoot, record.Remote,
	); err == nil {
		t.Fatal("tampered remote refresh digest was accepted")
	}
}
