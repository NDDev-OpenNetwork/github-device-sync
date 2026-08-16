package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fastForwardFixtureState struct {
	client   string
	writer   string
	firstOID string
}

func fastForwardFixture(t *testing.T) fastForwardFixtureState {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runFetchGit(t, filepath.Dir(remote), "init", "--bare", "-q", remote)
	client, firstOID := mutationRepository(t)
	runFetchGit(t, client, "branch", "-M", "main")
	runFetchGit(t, client, "remote", "add", "origin", remote)
	runFetchGit(t, client, "push", "-qu", "origin", "main")
	runFetchGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	writer := filepath.Join(t.TempDir(), "writer")
	runFetchGit(t, filepath.Dir(writer), "clone", "-q", remote, writer)
	runFetchGit(t, writer, "config", "user.name", "GDS Writer")
	runFetchGit(t, writer, "config", "user.email", "writer@example.invalid")
	return fastForwardFixtureState{client: client, writer: writer, firstOID: firstOID}
}

func (fixture fastForwardFixtureState) push(t *testing.T, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.writer, "fixture.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, fixture.writer, "commit", "-qam", content)
	runFetchGit(t, fixture.writer, "push", "-q", "origin", "main")
	return stringsTrim(runFetchGit(t, fixture.writer, "rev-parse", "HEAD"))
}

func TestFastForwardCheckoutUsesExactCleanCASAndDisablesHooks(t *testing.T) {
	fixture := fastForwardFixture(t)
	target := fixture.push(t, "second\n")
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.FetchRemote(context.Background(), fixture.client, "origin"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "post-merge-ran")
	hook := filepath.Join(fixture.client, ".git", "hooks", "post-merge")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := runner.FastForwardCheckout(
		context.Background(), fixture.client, "refs/heads/main", fixture.firstOID,
		"refs/remotes/origin/main", target,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.HeadOID != fixture.firstOID || report.After.HeadOID != target ||
		!report.Before.Clean || !report.After.Clean {
		t.Fatalf("report=%#v", report)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("post-merge hook ran: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(fixture.client, "fixture.txt")); err != nil ||
		string(content) != "second\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if _, err := runner.FastForwardCheckout(
		context.Background(), fixture.client, "refs/heads/main", fixture.firstOID,
		"refs/remotes/origin/main", target,
	); err == nil {
		t.Fatal("stale expected HEAD was accepted")
	}
}

func TestFastForwardCheckoutPreservesDirtyStateAndBlocksExecutableConfig(t *testing.T) {
	fixture := fastForwardFixture(t)
	target := fixture.push(t, "second\n")
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.FetchRemote(context.Background(), fixture.client, "origin"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.client, "local.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.FastForwardCheckout(
		context.Background(), fixture.client, "refs/heads/main", fixture.firstOID,
		"refs/remotes/origin/main", target,
	); err == nil {
		t.Fatal("dirty checkout was fast-forwarded")
	}
	if head := stringsTrim(runFetchGit(t, fixture.client, "rev-parse", "HEAD")); head != fixture.firstOID {
		t.Fatalf("dirty checkout HEAD changed: %s", head)
	}
	if content, err := os.ReadFile(filepath.Join(fixture.client, "local.txt")); err != nil ||
		string(content) != "preserve\n" {
		t.Fatalf("dirty state changed: content=%q err=%v", content, err)
	}
	if err := os.Remove(filepath.Join(fixture.client, "local.txt")); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, fixture.client, "config", "filter.unsafe.smudge", "sh -c 'exit 99'")
	if _, err := runner.FastForwardCheckout(
		context.Background(), fixture.client, "refs/heads/main", fixture.firstOID,
		"refs/remotes/origin/main", target,
	); err == nil {
		t.Fatal("executable filter configuration was accepted")
	}
	if head := stringsTrim(runFetchGit(t, fixture.client, "rev-parse", "HEAD")); head != fixture.firstOID {
		t.Fatalf("unsafe-config checkout HEAD changed: %s", head)
	}
}

func TestValidateLocalBranchRefRejectsOptionAndDotComponents(t *testing.T) {
	t.Parallel()
	if err := validateLocalBranchRef("refs/heads/task/valid"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"refs/heads/-option", "refs/heads/.hidden", "refs/heads/task/.hidden",
		"refs/heads/task.lock",
	} {
		if err := validateLocalBranchRef(value); err == nil {
			t.Errorf("invalid branch ref accepted: %q", value)
		}
	}
}
