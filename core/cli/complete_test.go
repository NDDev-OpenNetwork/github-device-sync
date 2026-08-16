package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const completeTestTaskID = "task_01KX817TGDBHTE66KZS0AT8BEY"

func prepareCompleteFixture(
	t *testing.T,
	integration string,
	requiredChecks bool,
) (sessionFixtureState, string, string) {
	t.Helper()
	fixture := sessionFixtureWithPolicies(t, "never", integration, requiredChecks)
	runSessionGit(t, fixture.client, "switch", "-qc", "task/complete")
	if err := os.WriteFile(filepath.Join(fixture.client, "fixture.txt"), []byte("complete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, fixture.client, "commit", "-qam", "complete")
	runSessionGit(t, fixture.client, "push", "-qu", "origin", "task/complete")
	taskOID := runSessionGit(t, fixture.client, "rev-parse", "HEAD")
	statePath := sessionStatePath(t)
	t.Setenv("GDS_ESTATE_ROOT", repositoryRoot(t))
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "session", "start", "--refresh", "origin",
		"--state-path", statePath,
	)
	if exitCode != 3 || !envelope.Mutation.Completed {
		t.Fatalf("refresh exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	return fixture, statePath, taskOID
}

func planCompleteFixture(
	t *testing.T,
	fixture sessionFixtureState,
	statePath string,
	sessionID string,
) domainEnvelopeResult {
	t.Helper()
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "complete", "--plan",
		"--task-id", completeTestTaskID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", sessionID,
	)
	return domainEnvelopeResult{exitCode: exitCode, envelope: envelope, stderr: stderr}
}

func TestCompleteRequiresExactPlanApprovalAndVerify(t *testing.T) {
	fixture, statePath, taskOID := prepareCompleteFixture(t, "direct", false)
	planned := planCompleteFixture(t, fixture, statePath, "complete-session")
	if planned.exitCode != 0 || planned.envelope.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", planned.exitCode, planned.stderr, planned.envelope)
	}
	planID := syncPlanID(t, planned.envelope.Data)
	exitCode, unapproved, stderr := executeJSON(
		t, "--json", "complete", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "complete-session",
	)
	if exitCode != 6 || unapproved.Mutation.Attempted {
		t.Fatalf("unapproved exit=%d stderr=%q envelope=%#v", exitCode, stderr, unapproved)
	}
	exitCode, applied, stderr := executeJSON(
		t, "--json", "complete", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "complete-session",
		"--approval-ref", "owner-approved:complete-fixture",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	if branch := runSessionGit(t, fixture.client, "branch", "--show-current"); branch != "main" {
		t.Fatalf("current branch=%q", branch)
	}
	if head := runSessionGit(t, fixture.client, "rev-parse", "HEAD"); head != taskOID {
		t.Fatalf("completed HEAD=%s want=%s", head, taskOID)
	}
	if remoteMain := runSessionGit(t, fixture.remote, "rev-parse", "refs/heads/main"); remoteMain != taskOID {
		t.Fatalf("remote main=%s want=%s", remoteMain, taskOID)
	}
	if branches := strings.TrimSpace(runSessionGit(t, fixture.client, "branch", "--list", "task/complete")); branches != "" {
		t.Fatalf("local task branch remains: %q", branches)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "complete", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "complete-session",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func TestCompleteBlocksUnprovenChecksPRPolicyAndNetworkApply(t *testing.T) {
	checksFixture, checksState, _ := prepareCompleteFixture(t, "direct", true)
	checksPlan := planCompleteFixture(t, checksFixture, checksState, "checks-session")
	if checksPlan.exitCode != 3 || !containsFinding(checksPlan.envelope.Findings, "GDS_COMPLETE_CHECK_NOT_PROVEN") {
		t.Fatalf("checks plan=%#v stderr=%q", checksPlan.envelope, checksPlan.stderr)
	}

	prFixture, prState, _ := prepareCompleteFixture(t, "pull-request", false)
	prPlan := planCompleteFixture(t, prFixture, prState, "pr-session")
	if prPlan.exitCode != 3 || !containsFinding(prPlan.envelope.Findings, "GDS_COMPLETE_PR_INTEGRATION_REQUIRED") {
		t.Fatalf("PR plan=%#v stderr=%q", prPlan.envelope, prPlan.stderr)
	}

	fixture, statePath, taskOID := prepareCompleteFixture(t, "direct", false)
	localPlan := planCompleteFixture(t, fixture, statePath, "network-complete-session")
	if localPlan.exitCode != 0 {
		t.Fatalf("local plan=%#v stderr=%q", localPlan.envelope, localPlan.stderr)
	}
	planID := syncPlanID(t, localPlan.envelope.Data)
	runSessionGit(t, fixture.client, "remote", "set-url", "origin", "https://example.invalid/repository.git")
	exitCode, blocked, stderr := executeJSON(
		t, "--json", "complete", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "network-complete-session",
		"--approval-ref", "owner-approved:network-complete-fixture",
	)
	if exitCode != 13 || blocked.Mutation.Attempted ||
		!containsFinding(blocked.Findings, "GDS_COMPLETE_LIVE_INTEGRATION_DISABLED") {
		t.Fatalf("blocked exit=%d stderr=%q envelope=%#v", exitCode, stderr, blocked)
	}
	if head := runSessionGit(t, fixture.client, "rev-parse", "HEAD"); head == fixture.firstOID {
		t.Fatal("fixture task commit unexpectedly missing")
	}
	if remoteMain := runSessionGit(t, fixture.remote, "rev-parse", "refs/heads/main"); remoteMain != fixture.firstOID {
		t.Fatalf("blocked network apply changed remote main: %s", remoteMain)
	}
	if remoteTask := runSessionGit(t, fixture.remote, "rev-parse", "refs/heads/task/complete"); remoteTask != taskOID {
		t.Fatalf("blocked network apply changed remote task: %s", remoteTask)
	}
	if _, err := os.Stat(filepath.Join(fixture.client, "fixture.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteFinalizesModuleBeforeConsumerAndLeavesFinalGitlink(t *testing.T) {
	moduleID := "repo_01JEXAMPZ0000000000000000C"
	consumerID := "repo_01JEXAMPZ0000000000000000D"
	module := sessionFixtureWithPolicies(t, "never", "direct", false)
	runSessionGit(t, module.client, "switch", "-qc", "task/module-complete")
	if err := os.WriteFile(filepath.Join(module.client, "fixture.txt"), []byte("module complete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, module.client, "commit", "-qam", "complete module")
	runSessionGit(t, module.client, "push", "-qu", "origin", "task/module-complete")
	moduleTaskOID := runSessionGit(t, module.client, "rev-parse", "HEAD")

	consumer := sessionFixtureWithPolicies(t, "never", "direct", false)
	anchorPath := filepath.Join(consumer.client, ".gds", "repository.yaml")
	anchor, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	updatedAnchor := strings.Replace(string(anchor), moduleID, consumerID, 1)
	updatedAnchor = strings.Replace(
		updatedAnchor,
		"\nrelease:\n",
		"\nrelationships:\n  - type: \"git-submodule-consumer\"\n    target: \""+moduleID+"\"\n    gitmodules_name: \"module\"\n\nrelease:\n",
		1,
	)
	if err := os.WriteFile(anchorPath, []byte(updatedAnchor), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, consumer.client, "add", ".gds/repository.yaml")
	runSessionGit(t, consumer.client, "commit", "-qm", "declare module relationship")
	runSessionGit(t, consumer.client, "-c", "protocol.file.allow=always", "submodule", "add", "-q", "--name", "module", module.remote, "modules/module")
	runSessionGit(t, consumer.client, "commit", "-qam", "add module")
	runSessionGit(t, consumer.client, "push", "-q", "origin", "main")
	runSessionGit(t, consumer.client, "switch", "-qc", "task/consumer-complete")
	moduleCheckout := filepath.Join(consumer.client, "modules", "module")
	runSessionGit(t, moduleCheckout, "-c", "protocol.file.allow=always", "fetch", "-q", "origin", "task/module-complete")
	runSessionGit(t, moduleCheckout, "checkout", "-q", moduleTaskOID)
	runSessionGit(t, consumer.client, "add", "modules/module")
	runSessionGit(t, consumer.client, "commit", "-qm", "pin completed module")
	runSessionGit(t, consumer.client, "push", "-qu", "origin", "task/consumer-complete")
	consumerTaskOID := runSessionGit(t, consumer.client, "rev-parse", "HEAD")

	statePath := sessionStatePath(t)
	t.Setenv("GDS_ESTATE_ROOT", repositoryRoot(t))
	for _, root := range []string{module.client, consumer.client} {
		exitCode, refreshed, stderr := executeJSON(
			t, "--json", "--cwd", root, "session", "start", "--refresh", "origin",
			"--state-path", statePath,
		)
		if exitCode != 3 || !refreshed.Mutation.Completed {
			t.Fatalf("refresh %s exit=%d stderr=%q envelope=%#v", root, exitCode, stderr, refreshed)
		}
	}
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", consumer.client, "complete", "--plan",
		"--task-id", completeTestTaskID,
		"--checkout", consumer.client, "--checkout", module.client,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-consumer-complete",
	)
	if exitCode != 0 {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planData, ok := planned.Data.(map[string]any)
	if !ok {
		t.Fatalf("plan data=%#v", planned.Data)
	}
	planObject, _ := planData["plan"].(map[string]any)
	steps, _ := planObject["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("dependency steps=%#v", steps)
	}
	firstStep, _ := steps[0].(map[string]any)
	if firstStep["repository_id"] != moduleID {
		t.Fatalf("dependency order=%#v", steps)
	}
	planID := syncPlanID(t, planned.Data)
	exitCode, applied, stderr := executeJSON(
		t, "--json", "complete", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-consumer-complete",
		"--approval-ref", "owner-approved:module-consumer-complete",
	)
	if exitCode != 0 || !applied.Mutation.Completed {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	if moduleMain := runSessionGit(t, module.remote, "rev-parse", "refs/heads/main"); moduleMain != moduleTaskOID {
		t.Fatalf("module main=%s want=%s", moduleMain, moduleTaskOID)
	}
	if consumerMain := runSessionGit(t, consumer.remote, "rev-parse", "refs/heads/main"); consumerMain != consumerTaskOID {
		t.Fatalf("consumer main=%s want=%s", consumerMain, consumerTaskOID)
	}
	if gitlink := strings.Fields(runSessionGit(t, consumer.client, "ls-tree", "HEAD", "modules/module"))[2]; gitlink != moduleTaskOID {
		t.Fatalf("final gitlink=%s want=%s", gitlink, moduleTaskOID)
	}
	if branch := runSessionGit(t, module.client, "branch", "--show-current"); branch != "main" {
		t.Fatalf("module branch=%q", branch)
	}
	if branch := runSessionGit(t, consumer.client, "branch", "--show-current"); branch != "main" {
		t.Fatalf("consumer branch=%q", branch)
	}
}
