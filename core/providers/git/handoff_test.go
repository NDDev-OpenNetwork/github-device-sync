package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommitAndPushHandoffCommitsOnlySelectedFilesAndPreservesHooks(t *testing.T) {
	fixture := fastForwardFixture(t)
	runFetchGit(t, fixture.client, "switch", "-qc", "task/handoff")
	if err := os.WriteFile(filepath.Join(fixture.client, "fixture.txt"), []byte("selected tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.client, "selected.txt"), []byte("selected untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.client, "unrelated.txt"), []byte("preserve staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, fixture.client, "add", "unrelated.txt")
	marker := filepath.Join(t.TempDir(), "commit-hook-ran")
	hook := filepath.Join(fixture.client, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.CommitAndPushHandoff(
		context.Background(), fixture.client, "refs/heads/task/handoff", fixture.firstOID,
		"refs/heads/task/handoff", zeroOID(40),
		[]string{"fixture.txt", "selected.txt"}, "chore: checkpoint handoff",
		CommitIdentity{Name: "GDS Owner", Email: "owner@example.invalid"},
		time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.HeadOID != fixture.firstOID || report.Before.RemoteOID != zeroOID(40) ||
		report.After.HeadOID == fixture.firstOID || report.After.RemoteOID != report.After.HeadOID {
		t.Fatalf("report=%#v", report)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("commit hook ran: %v", err)
	}
	staged := strings.Fields(runFetchGit(t, fixture.client, "diff", "--cached", "--name-only"))
	if len(staged) != 1 || staged[0] != "unrelated.txt" {
		t.Fatalf("unrelated staged state changed: %v", staged)
	}
	committed := strings.Fields(runFetchGit(
		t, fixture.client, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD",
	))
	if strings.Join(committed, ",") != "fixture.txt,selected.txt" {
		t.Fatalf("committed files=%v", committed)
	}
	if upstream := strings.TrimSpace(runFetchGit(
		t, fixture.client, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}",
	)); upstream != "origin/task/handoff" {
		t.Fatalf("upstream=%q", upstream)
	}
}

func TestCommitAndPushHandoffBlocksNetworkRemoteBeforeCommit(t *testing.T) {
	root, head := mutationRepository(t)
	runFetchGit(t, root, "branch", "-M", "task/handoff")
	runFetchGit(t, root, "remote", "add", "origin", "https://example.invalid/repository.git")
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.CommitAndPushHandoff(
		context.Background(), root, "refs/heads/task/handoff", head,
		"refs/heads/task/handoff", zeroOID(40), []string{"fixture.txt"},
		"chore: checkpoint handoff",
		CommitIdentity{Name: "GDS Owner", Email: "owner@example.invalid"},
		time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	)
	if !errors.Is(err, ErrNetworkMutationDisabled) {
		t.Fatalf("network mutation error=%v", err)
	}
	if observed := strings.TrimSpace(runFetchGit(t, root, "rev-parse", "HEAD")); observed != head {
		t.Fatalf("network-blocked handoff changed HEAD: %s", observed)
	}
}
