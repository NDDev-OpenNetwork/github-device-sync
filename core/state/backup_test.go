package state

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupCreatesPrivateVerifiedSQLiteSnapshot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backupDirectory := filepath.Join(root, "backups")
	if err := os.Mkdir(backupDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(backupDirectory, "state-20260711T000000Z.db")
	backup, err := store.Backup(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode().Perm() != 0o600 || backup.Size < 1 || backup.Digest == "" ||
		backup.SchemaVersion != schemaVersion || backup.Integrity != "pass" ||
		backup.LogicalDigest == "" {
		t.Fatalf("backup=%+v info=%+v err=%v", backup, info, err)
	}
	if _, err := store.Backup(context.Background(), target); err == nil {
		t.Fatal("existing backup target was overwritten")
	}
}

func TestBackupRejectsAndRemovesLogicallyInvalidSnapshot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`INSERT INTO operations(
            operation_id, plan_id, operation, status, actor_json, started_at
		 ) VALUES ('op_orphan', 'plan_missing', 'fixture', 'applying', ?, ?)`,
		[]byte(`{}`), formatTime(testTime),
	); err != nil {
		t.Fatal(err)
	}
	backupDirectory := filepath.Join(root, "backups")
	if err := os.Mkdir(backupDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(backupDirectory, "invalid.db")
	if _, err := store.Backup(ctx, target); err == nil {
		t.Fatal("foreign-key-invalid backup passed verification")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("invalid backup candidate was not removed: %v", err)
	}
}

func TestSnapshotRejectsCorruptBtreePage(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state.db")
	store, err := Initialize(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutPlan(context.Background(), testPlanRecord()); err != nil {
		t.Fatal(err)
	}
	var rootPage int64
	if err := store.db.QueryRowContext(
		context.Background(), `SELECT rootpage FROM sqlite_schema WHERE name = 'plans'`,
	).Scan(&rootPage); err != nil {
		t.Fatal(err)
	}
	if rootPage <= 1 {
		t.Fatalf("unexpected plans root page: %d", rootPage)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 100)
	if _, err := file.ReadAt(header, 0); err != nil {
		file.Close()
		t.Fatal(err)
	}
	pageSize := int64(binary.BigEndian.Uint16(header[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 {
		file.Close()
		t.Fatalf("invalid SQLite page size: %d", pageSize)
	}
	if _, err := file.WriteAt([]byte{0xff}, (rootPage-1)*pageSize); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Snapshot(context.Background(), path); err == nil {
		t.Fatal("corrupt SQLite b-tree passed snapshot verification")
	}
}

func TestBackupPublicationNeverOverwritesOrRemovesExistingTarget(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate.db")
	target := filepath.Join(root, "target.db")
	if err := os.WriteFile(candidate, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("winner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishBackupCandidate(candidate, target); err == nil {
		t.Fatal("backup publication overwrote an existing target")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "winner" {
		t.Fatalf("existing backup changed: %q err=%v", content, err)
	}
	candidateInfo, err := os.Lstat(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeBackupIfSame(target, candidateInfo); err == nil {
		t.Fatal("identity-mismatched backup target was removed")
	}
	content, err = os.ReadFile(target)
	if err != nil || string(content) != "winner" {
		t.Fatalf("identity-mismatched backup changed: %q err=%v", content, err)
	}
}
