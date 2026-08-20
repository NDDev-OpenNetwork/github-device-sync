package cli

import (
	"os"
	"path/filepath"
	"testing"
)

const syncTestDeviceID = "device_01JEXAMPZ00000000000000000"

func prepareSyncFixture(t *testing.T) (sessionFixtureState, string, string) {
	t.Helper()
	fixture := sessionFixture(t)
	fixture.pushSecond(t)
	statePath := sessionStatePath(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "session", "start", "--refresh", "origin",
		"--state-path", statePath,
	)
	if exitCode != 3 || !envelope.Mutation.Completed {
		t.Fatalf("refresh exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	target := runSessionGit(t, fixture.client, "rev-parse", "refs/remotes/origin/main")
	return fixture, statePath, target
}

func syncPlanID(t *testing.T, envelopeData any) string {
	t.Helper()
	data, ok := envelopeData.(map[string]any)
	if !ok {
		t.Fatalf("sync data=%#v", envelopeData)
	}
	plan, ok := data["plan"].(map[string]any)
	if !ok {
		t.Fatalf("sync plan=%#v", data["plan"])
	}
	planID, _ := plan["plan_id"].(string)
	if planID == "" {
		t.Fatalf("sync plan id=%#v", plan)
	}
	return planID
}

func TestSyncCheckoutsRequiresExactPlanApprovalAndVerify(t *testing.T) {
	fixture, statePath, target := prepareSyncFixture(t)
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "sync", "checkouts", "--plan",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "sync-session",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)

	exitCode, unapproved, stderr := executeJSON(
		t, "--json", "sync", "checkouts", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "sync-session",
	)
	if exitCode != 6 || unapproved.Mutation.Attempted {
		t.Fatalf("unapproved exit=%d stderr=%q envelope=%#v", exitCode, stderr, unapproved)
	}
	if head := runSessionGit(t, fixture.client, "rev-parse", "HEAD"); head != fixture.firstOID {
		t.Fatalf("unapproved sync changed HEAD: %s", head)
	}

	exitCode, applied, stderr := executeJSON(
		t, "--json", "sync", "checkouts", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "sync-session", "--approval-ref", "owner-approved:sync-fixture",
	)
	if exitCode != 0 || !applied.Mutation.Attempted || !applied.Mutation.Completed ||
		applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	if head := runSessionGit(t, fixture.client, "rev-parse", "HEAD"); head != target {
		t.Fatalf("sync HEAD=%s want=%s", head, target)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "sync", "checkouts", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "sync-session",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func TestSyncCheckoutsPreservesDirtyAndRejectsStalePlan(t *testing.T) {
	fixture, statePath, _ := prepareSyncFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.client, "untracked.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exitCode, skipped, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "sync", "checkouts", "--plan",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "dirty-session",
	)
	if exitCode != 3 || !containsFinding(skipped.Findings, "GDS_SYNC_NO_ELIGIBLE_CHECKOUT") {
		t.Fatalf("dirty exit=%d stderr=%q envelope=%#v", exitCode, stderr, skipped)
	}
	if err := os.Remove(filepath.Join(fixture.client, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "sync", "checkouts", "--plan",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "stale-session",
	)
	if exitCode != 0 {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	if err := os.WriteFile(filepath.Join(fixture.client, "untracked.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exitCode, stale, stderr := executeJSON(
		t, "--json", "sync", "checkouts", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "stale-session", "--approval-ref", "owner-approved:stale-fixture",
	)
	if exitCode != 5 || stale.Mutation.Attempted || !containsFinding(stale.Findings, "GDS_STALE_PLAN") {
		t.Fatalf("stale exit=%d stderr=%q envelope=%#v", exitCode, stderr, stale)
	}
	if head := runSessionGit(t, fixture.client, "rev-parse", "HEAD"); head != fixture.firstOID {
		t.Fatalf("stale sync changed HEAD: %s", head)
	}
	if content, err := os.ReadFile(filepath.Join(fixture.client, "untracked.txt")); err != nil ||
		string(content) != "preserve\n" {
		t.Fatalf("stale sync changed dirty file: content=%q err=%v", content, err)
	}
}

func TestSyncCheckoutsRejectsRemoteAdvanceAfterPlanning(t *testing.T) {
	fixture, statePath, _ := prepareSyncFixture(t)
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "sync", "checkouts", "--plan",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "remote-stale-session",
	)
	if exitCode != 0 {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	if err := os.WriteFile(filepath.Join(fixture.writer, "fixture.txt"), []byte("third\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, fixture.writer, "commit", "-qam", "third")
	runSessionGit(t, fixture.writer, "push", "-q", "origin", "main")
	exitCode, stale, stderr := executeJSON(
		t, "--json", "sync", "checkouts", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "remote-stale-session",
		"--approval-ref", "owner-approved:remote-stale-fixture",
	)
	if exitCode != 5 || stale.Mutation.Attempted || !containsFinding(stale.Findings, "GDS_STALE_PLAN") {
		t.Fatalf("stale exit=%d stderr=%q envelope=%#v", exitCode, stderr, stale)
	}
	if head := runSessionGit(t, fixture.client, "rev-parse", "HEAD"); head != fixture.firstOID {
		t.Fatalf("remote-stale sync changed HEAD: %s", head)
	}
}
