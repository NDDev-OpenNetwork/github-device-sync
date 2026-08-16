package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestInitializeCreatesPrivateWALDatabaseAndReadOnlyReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gds-state", "state.db")
	store, err := Initialize(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := store.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.SchemaVersion != schemaVersion || info.JournalMode != "wal" || !info.ForeignKeys || info.QueryOnly {
		t.Fatalf("unexpected state info: %+v", info)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", fileInfo.Mode().Perm())
	}

	readOnly, err := OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	readInfo, err := readOnly.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !readInfo.QueryOnly || readInfo.SchemaVersion != schemaVersion {
		t.Fatalf("unexpected read-only info: %+v", readInfo)
	}
}

func TestOpenNeverCreatesOrMigratesStateImplicitly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.db")
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("Open created a missing state database")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing state path changed: %v", err)
	}
}

func TestOpenRejectsSymlinkDatabase(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), link); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestOpenRejectsPermissiveDatabase(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("expected permissive mode rejection")
	}
}

func TestOpenRejectsStateDatabaseInPermissiveDirectory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "shared")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "state.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("state database in a permissive directory was accepted")
	}
}

func TestStateAuthorityRejectsDatabaseIdentityReplacement(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "state.db")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	authority, err := openStateAuthority(path)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if err := os.Rename(path, path+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authority.verify(); err == nil {
		t.Fatal("state database identity replacement was accepted")
	}
}

func TestStateAuthorityRejectsParentDirectoryReplacement(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "state.db")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	authority, err := openStateAuthority(path)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if err := os.Rename(directory, directory+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authority.verify(); err == nil {
		t.Fatal("state parent directory replacement was accepted")
	}
}

func TestNormalStoreAPIsRejectPostOpenDatabaseReplacement(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(directory, "state.db")
	store, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	plan := testPlanRecord()
	if err := store.PutPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	detached := path + ".detached"
	if err := os.Rename(path, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	operation := OperationRecord{
		OperationID: "operation-authority", PlanID: plan.PlanID, Operation: "authority-test",
		Status: "applying", Actor: []byte(`{"kind":"test"}`), StartedAt: testTime,
	}
	step := StepRecord{
		OperationID: operation.OperationID, StepID: "step-authority", RepositoryID: "repo-authority",
		Action: "observe", IdempotencyKey: "authority-key", Sequence: 0, Status: "pending",
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{"info", func() error { _, err := store.Info(ctx); return err }},
		{"summary", func() error { _, err := store.Summary(ctx); return err }},
		{"plan-write", func() error { return store.PutPlan(ctx, plan) }},
		{"operation-start", func() error { return store.StartOperation(ctx, operation, []StepRecord{step}) }},
		{"webhook-write", func() error { _, err := store.EnqueueWebhook(ctx, webhookFixture("authority")); return err }},
		{"observation-write", func() error {
			return store.PutRepositoryObservation(ctx, repositoryObservation(testTime, `{"id":123}`))
		}},
		{"accepted-bundle-write", func() error {
			return store.PutAcceptedBundle(ctx, acceptedBundle(1, 'a'), nil, testTime)
		}},
	}
	for _, check := range checks {
		if err := check.run(); err == nil {
			t.Fatalf("%s succeeded against replaced state authority", check.name)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(detached, path); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	summary, err := reopened.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Plans != 1 || summary.Operations != 0 || summary.Webhooks != 0 ||
		summary.Observations != 0 || summary.AcceptedBundles != 0 {
		t.Fatalf("failed authority operations became durable: %+v", summary)
	}
}

func TestQueryRowsRejectAuthorityDriftDuringIteration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "state.db")
	store, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.QueryContext(ctx, `SELECT name FROM sqlite_master ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	detached := path + ".detached"
	if err := os.Rename(path, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("query rows continued after state authority replacement")
	}
	if err := rows.Err(); err == nil {
		t.Fatal("query rows did not report state authority replacement")
	}
}

func TestTransactionCommitRejectsUnsafeWALSidecar(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "state.db")
	store, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	plan := testPlanRecord()
	if err := store.PutPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	wal := path + "-wal"
	if _, err := os.Lstat(wal); err != nil {
		t.Fatalf("WAL sidecar unavailable: %v", err)
	}
	store.beforeAuthorityCommit = func() {
		if err := os.Chmod(wal, 0o644); err != nil {
			t.Errorf("make WAL unsafe: %v", err)
		}
	}
	operation := OperationRecord{
		OperationID: "operation-wal-authority", PlanID: plan.PlanID, Operation: "authority-test",
		Status: "applying", Actor: []byte(`{"kind":"test"}`), StartedAt: testTime,
	}
	step := StepRecord{
		OperationID: operation.OperationID, StepID: "step-wal-authority", RepositoryID: "repo-authority",
		Action: "observe", IdempotencyKey: "wal-authority-key", Sequence: 0, Status: "pending",
	}
	if err := store.StartOperation(ctx, operation, []StepRecord{step}); err == nil {
		t.Fatal("transaction committed after its WAL authority became unsafe")
	}
	store.beforeAuthorityCommit = nil
	if err := os.Chmod(wal, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.GetOperation(ctx, operation.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected transaction became durable: %v", err)
	}
}

func TestClosedStoreReturnsDatabaseErrorsWithoutPanicking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.db")
	store, err := Initialize(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Info(context.Background()); err == nil {
		t.Fatal("closed store Info succeeded")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close returned %v", err)
	}
}

func TestDefaultPathUsesAbsoluteXDGStateHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state-root")
	t.Setenv("XDG_STATE_HOME", root)
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "github-device-sync", "state.db")
	if path != want {
		t.Fatalf("default state path = %s, want %s", path, want)
	}

	if runtime.GOOS != "windows" {
		t.Setenv("XDG_STATE_HOME", "relative-state")
		if _, err := DefaultPath(); err == nil {
			t.Fatal("expected relative XDG_STATE_HOME rejection")
		}
	}
}

func TestLifecycleSnapshotIsDeterministicAndContentSensitive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.db")
	store, err := Initialize(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Snapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Snapshot(context.Background(), path)
	if err != nil || second.LogicalDigest != first.LogicalDigest {
		t.Fatalf("snapshot is not deterministic: first=%#v second=%#v err=%v", first, second, err)
	}
	now := time.Now().UTC()
	if err := store.PutPlan(context.Background(), PlanRecord{
		PlanID: "plan_snapshot", Operation: "snapshot-fixture",
		PlanDigest: "sha256:snapshot", Body: []byte(`{}`), Status: "planned",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute), InsertedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	changed, err := Snapshot(context.Background(), path)
	if err != nil || changed.LogicalDigest == first.LogicalDigest {
		t.Fatalf("logical mutation was not reflected: first=%#v changed=%#v err=%v", first, changed, err)
	}
}

func TestMigrationFourBackfillsLegacyStepIdempotency(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state.db")
	database, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 3; version++ {
		name := fmt.Sprintf("migrations/%03d-", version)
		entries, err := migrationFiles.ReadDir("migrations")
		if err != nil {
			t.Fatal(err)
		}
		var selected string
		for _, entry := range entries {
			if len(entry.Name()) >= 4 && "migrations/"+entry.Name()[:4] == name {
				selected = "migrations/" + entry.Name()
				break
			}
		}
		if selected == "" {
			t.Fatalf("migration %d not found", version)
		}
		raw, err := migrationFiles.ReadFile(selected)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			t.Fatal(err)
		}
	}
	now := "2026-07-11T09:00:00Z"
	if _, err := database.Exec(
		`INSERT INTO plans(plan_id, operation, plan_digest, body_json, status, created_at, expires_at, inserted_at)
         VALUES ('plan_legacy', 'fixture', 'sha256:legacy', ?, 'planned', ?, ?, ?)`,
		[]byte(`{}`), now, "2026-07-11T09:15:00Z", now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`INSERT INTO operations(operation_id, plan_id, operation, status, actor_json, started_at)
         VALUES ('op_legacy', 'plan_legacy', 'fixture', 'applying', ?, ?)`, []byte(`{}`), now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`INSERT INTO operation_steps(operation_id, step_id, repository_id, action, sequence, status)
         VALUES ('op_legacy', 'step-1', 'repo_legacy', 'fixture', 0, 'pending')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Snapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Migrate(
		context.Background(), path, snapshot, DefaultBackupPath(snapshot), LifecycleEvidence{
			Action: "migrate-state", PlanDigest: "sha256:plan",
			ApprovalDigest: "sha256:approval", AppliedAt: time.Now().UTC(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.SchemaVersion != 3 || report.After.SchemaVersion != schemaVersion ||
		report.Backup.SchemaVersion != 3 || report.Backup.LogicalDigest != report.Before.LogicalDigest ||
		report.BackupRawDigest == "" {
		t.Fatalf("unexpected migration report: %#v", report)
	}
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	evidence, err := store.LifecycleEvidence(context.Background())
	if err != nil || evidence.PlanDigest != "sha256:plan" || evidence.BackupPath == "" {
		t.Fatalf("lifecycle evidence=%#v err=%v", evidence, err)
	}
	steps, err := store.ListSteps(context.Background(), "op_legacy")
	if err != nil || len(steps) != 1 || steps[0].IdempotencyKey != "legacy:op_legacy:step-1" {
		t.Fatalf("legacy idempotency migration failed: %+v %v", steps, err)
	}
	summary, err := store.Summary(context.Background())
	if err != nil || summary.RemoteRefreshes != 0 {
		t.Fatalf("session-refresh migration missing: summary=%#v err=%v", summary, err)
	}
}
