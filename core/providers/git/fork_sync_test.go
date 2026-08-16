package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncForkFastForwardPreservesHistoryAndPublishesExactUpstream(t *testing.T) {
	checkout, origin, upstream, base := forkSyncFixture(t)
	writer := filepath.Join(t.TempDir(), "writer")
	runFetchGit(t, filepath.Dir(writer), "clone", "-q", upstream, writer)
	runFetchGit(t, writer, "config", "user.name", "Upstream")
	runFetchGit(t, writer, "config", "user.email", "upstream@example.invalid")
	if err := os.WriteFile(filepath.Join(writer, "upstream.txt"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, writer, "add", "upstream.txt")
	runFetchGit(t, writer, "commit", "-qm", "upstream")
	target := stringsTrim(runFetchGit(t, writer, "rev-parse", "HEAD"))
	runFetchGit(t, writer, "push", "-q", "origin", "main")
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.SyncForkFastForward(
		context.Background(), checkout, "refs/heads/main", base, target,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.OriginOID != base || report.After.OriginOID != target ||
		stringsTrim(runFetchGit(t, origin, "rev-parse", "refs/heads/main")) != target {
		t.Fatalf("report=%#v", report)
	}
}

func TestObserveForkFastForwardBlocksForkOnlyCommits(t *testing.T) {
	checkout, origin, _, _ := forkSyncFixture(t)
	if err := os.WriteFile(filepath.Join(checkout, "fork.txt"), []byte("fork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, checkout, "add", "fork.txt")
	runFetchGit(t, checkout, "commit", "-qm", "fork")
	runFetchGit(t, checkout, "push", "-q", "origin", "main")
	runFetchGit(t, checkout, "fetch", "-q", "origin")
	writer := filepath.Join(t.TempDir(), "upstream-writer")
	upstream := stringsTrim(runFetchGit(t, checkout, "remote", "get-url", "upstream"))
	runFetchGit(t, filepath.Dir(writer), "clone", "-q", upstream, writer)
	runFetchGit(t, writer, "config", "user.name", "Upstream")
	runFetchGit(t, writer, "config", "user.email", "upstream@example.invalid")
	if err := os.WriteFile(filepath.Join(writer, "upstream.txt"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, writer, "add", "upstream.txt")
	runFetchGit(t, writer, "commit", "-qm", "upstream")
	runFetchGit(t, writer, "push", "-q", "origin", "main")
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ObserveForkFastForward(context.Background(), checkout, "refs/heads/main"); err == nil {
		t.Fatal("fork-only commits were not blocked")
	}
	if stringsTrim(runFetchGit(t, origin, "rev-parse", "refs/heads/main")) == "" {
		t.Fatal("origin fork history disappeared")
	}
}

func forkSyncFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	runFetchGit(t, filepath.Dir(upstream), "init", "--bare", "-q", upstream)
	seed, base := mutationRepository(t)
	runFetchGit(t, seed, "branch", "-M", "main")
	runFetchGit(t, seed, "remote", "add", "upstream", upstream)
	runFetchGit(t, seed, "push", "-q", "upstream", "main")
	runFetchGit(t, upstream, "symbolic-ref", "HEAD", "refs/heads/main")
	origin := filepath.Join(t.TempDir(), "origin.git")
	runFetchGit(t, filepath.Dir(origin), "clone", "--bare", "-q", upstream, origin)
	checkout := filepath.Join(t.TempDir(), "checkout")
	runFetchGit(t, filepath.Dir(checkout), "clone", "-q", origin, checkout)
	runFetchGit(t, checkout, "config", "user.name", "Fork")
	runFetchGit(t, checkout, "config", "user.email", "fork@example.invalid")
	runFetchGit(t, checkout, "remote", "add", "upstream", upstream)
	return checkout, origin, upstream, base
}
