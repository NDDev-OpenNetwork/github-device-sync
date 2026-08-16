package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type completionFixtureState struct {
	client  string
	remote  string
	mainOID string
	taskOID string
}

func completionFixture(t *testing.T) completionFixtureState {
	t.Helper()
	fixture := fastForwardFixture(t)
	runFetchGit(t, fixture.client, "switch", "-qc", "task/complete")
	if err := os.WriteFile(filepath.Join(fixture.client, "fixture.txt"), []byte("complete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, fixture.client, "commit", "-qam", "complete")
	runFetchGit(t, fixture.client, "push", "-qu", "origin", "task/complete")
	remote := strings.TrimSpace(runFetchGit(t, fixture.client, "remote", "get-url", "origin"))
	return completionFixtureState{
		client: fixture.client, remote: remote, mainOID: fixture.firstOID,
		taskOID: strings.TrimSpace(runFetchGit(t, fixture.client, "rev-parse", "HEAD")),
	}
}

func TestCompleteTaskBranchIntegratesAndCleansOnlyReachableRefs(t *testing.T) {
	fixture := completionFixture(t)
	marker := filepath.Join(t.TempDir(), "completion-hook-ran")
	for _, hookName := range []string{"post-checkout", "post-merge"} {
		hook := filepath.Join(fixture.client, ".git", "hooks", hookName)
		if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 99\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.CompleteTaskBranch(
		context.Background(), fixture.client, "refs/heads/main", "refs/heads/task/complete",
		fixture.mainOID, fixture.taskOID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.CurrentBranchRef != "refs/heads/task/complete" ||
		report.After.CurrentBranchRef != "refs/heads/main" ||
		report.After.HeadOID != fixture.taskOID ||
		report.After.LocalTaskOID != zeroOID(40) || report.After.RemoteTaskOID != zeroOID(40) {
		t.Fatalf("report=%#v", report)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("completion hook ran: %v", err)
	}
	if remoteMain := strings.TrimSpace(runFetchGit(t, fixture.remote, "rev-parse", "refs/heads/main")); remoteMain != fixture.taskOID {
		t.Fatalf("remote main=%s want=%s", remoteMain, fixture.taskOID)
	}
	if output := runFetchGit(t, fixture.client, "branch", "--list", "task/complete"); strings.TrimSpace(output) != "" {
		t.Fatalf("local task branch remains: %q", output)
	}
}

func TestCompleteTaskBranchPreservesWorkWhenDefaultIsActiveElsewhere(t *testing.T) {
	fixture := completionFixture(t)
	linked := filepath.Join(t.TempDir(), "linked-main")
	runFetchGit(t, fixture.client, "worktree", "add", "-q", linked, "main")
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.CompleteTaskBranch(
		context.Background(), fixture.client, "refs/heads/main", "refs/heads/task/complete",
		fixture.mainOID, fixture.taskOID,
	)
	if err == nil {
		t.Fatal("completion updated a default branch active in another worktree")
	}
	if remoteMain := strings.TrimSpace(runFetchGit(t, fixture.remote, "rev-parse", "refs/heads/main")); remoteMain != fixture.mainOID {
		t.Fatalf("blocked completion changed remote main: %s", remoteMain)
	}
	if task := strings.TrimSpace(runFetchGit(t, fixture.remote, "rev-parse", "refs/heads/task/complete")); task != fixture.taskOID {
		t.Fatalf("blocked completion changed remote task: %s", task)
	}
}
