package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func prepareHandoffFixture(t *testing.T, policy string) (sessionFixtureState, string) {
	t.Helper()
	fixture := sessionFixtureWithHandoffPolicy(t, policy)
	runSessionGit(t, fixture.client, "switch", "-qc", "task/handoff")
	if err := os.WriteFile(filepath.Join(fixture.client, "fixture.txt"), []byte("checkpoint\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.client, "selected.txt"), []byte("selected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.client, "unrelated.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, fixture.client, "add", "unrelated.txt")
	statePath := sessionStatePath(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "session", "start", "--refresh", "origin",
		"--state-path", statePath,
	)
	if exitCode != 3 || !envelope.Mutation.Completed {
		t.Fatalf("refresh exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	return fixture, statePath
}

func planHandoffFixture(
	t *testing.T,
	fixture sessionFixtureState,
	statePath string,
	sessionID string,
) domainEnvelopeResult {
	t.Helper()
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "handoff", "--plan",
		"--file", "fixture.txt", "--file", "selected.txt",
		"--message", "chore: checkpoint unfinished work",
		"--author-name", "GDS Owner", "--author-email", "owner@example.invalid",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", sessionID,
	)
	return domainEnvelopeResult{exitCode: exitCode, envelope: envelope, stderr: stderr}
}

type domainEnvelopeResult struct {
	exitCode int
	envelope domain.Envelope
	stderr   string
}

func TestHandoffRequiresExactPlanApprovalAndVerify(t *testing.T) {
	fixture, statePath := prepareHandoffFixture(t, "never")
	planned := planHandoffFixture(t, fixture, statePath, "handoff-session")
	if planned.exitCode != 0 || planned.envelope.Mutation.Attempted ||
		!containsFinding(planned.envelope.Findings, "GDS_HANDOFF_CHECK_NOT_PROVEN") {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", planned.exitCode, planned.stderr, planned.envelope)
	}
	planID := syncPlanID(t, planned.envelope.Data)
	exitCode, unapproved, stderr := executeJSON(
		t, "--json", "handoff", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "handoff-session",
	)
	if exitCode != 6 || unapproved.Mutation.Attempted {
		t.Fatalf("unapproved exit=%d stderr=%q envelope=%#v", exitCode, stderr, unapproved)
	}
	if head := runSessionGit(t, fixture.client, "rev-parse", "HEAD"); head != fixture.firstOID {
		t.Fatalf("unapproved handoff changed HEAD: %s", head)
	}
	exitCode, applied, stderr := executeJSON(
		t, "--json", "handoff", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "handoff-session",
		"--approval-ref", "owner-approved:handoff-fixture",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	newHead := runSessionGit(t, fixture.client, "rev-parse", "HEAD")
	if newHead == fixture.firstOID {
		t.Fatal("handoff did not create a checkpoint commit")
	}
	remoteHead := runSessionGit(t, fixture.remote, "rev-parse", "refs/heads/task/handoff")
	if remoteHead != newHead {
		t.Fatalf("remote head=%s local=%s", remoteHead, newHead)
	}
	staged := strings.Fields(runSessionGit(t, fixture.client, "diff", "--cached", "--name-only"))
	if len(staged) != 1 || staged[0] != "unrelated.txt" {
		t.Fatalf("unrelated staged state changed: %v", staged)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "handoff", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "handoff-session",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func TestHandoffBlocksRequiredDraftPRAndLivePushBeforeCommit(t *testing.T) {
	requiredFixture, requiredState := prepareHandoffFixture(t, "required")
	planned := planHandoffFixture(t, requiredFixture, requiredState, "required-pr-session")
	if planned.exitCode != 3 || planned.envelope.Data == nil ||
		!containsFinding(planned.envelope.Findings, "GDS_HANDOFF_DRAFT_PR_REQUIRED") {
		t.Fatalf("required plan=%#v stderr=%q", planned.envelope, planned.stderr)
	}

	fixture, statePath := prepareHandoffFixture(t, "never")
	localPlan := planHandoffFixture(t, fixture, statePath, "network-block-session")
	if localPlan.exitCode != 0 {
		t.Fatalf("local plan=%#v stderr=%q", localPlan.envelope, localPlan.stderr)
	}
	planID := syncPlanID(t, localPlan.envelope.Data)
	runSessionGit(t, fixture.client, "remote", "set-url", "origin", "https://example.invalid/repository.git")
	exitCode, blocked, stderr := executeJSON(
		t, "--json", "handoff", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "network-block-session",
		"--approval-ref", "owner-approved:network-block-fixture",
	)
	if exitCode != 13 || blocked.Mutation.Attempted ||
		!containsFinding(blocked.Findings, "GDS_HANDOFF_LIVE_PUSH_DISABLED") {
		t.Fatalf("blocked exit=%d stderr=%q envelope=%#v", exitCode, stderr, blocked)
	}
	if head := runSessionGit(t, fixture.client, "rev-parse", "HEAD"); head != fixture.firstOID {
		t.Fatalf("blocked network handoff changed HEAD: %s", head)
	}
}
