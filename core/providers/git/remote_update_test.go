package git

import (
	"context"
	"testing"
)

func TestUpdateRemoteUsesExactPlanStatesAndIsIdempotent(t *testing.T) {
	repository, _ := mutationRepository(t)
	expected := "https://github.com/old-owner/source.git"
	target := "https://github.com/new-owner/source.git"
	runFetchGit(t, repository, "remote", "add", "origin", expected)
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.UpdateRemote(context.Background(), repository, "origin", expected, target)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.State != "expected" || report.After.State != "target" {
		t.Fatalf("report=%#v", report)
	}
	replayed, err := runner.UpdateRemote(context.Background(), repository, "origin", expected, target)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Before.State != "target" || replayed.After.State != "target" {
		t.Fatalf("replayed=%#v", replayed)
	}
	if got := stringsTrim(runFetchGit(t, repository, "remote", "get-url", "origin")); got != target {
		t.Fatalf("fetch URL=%q", got)
	}
	if got := stringsTrim(runFetchGit(t, repository, "remote", "get-url", "--push", "origin")); got != target {
		t.Fatalf("push URL=%q", got)
	}
}

func TestUpdateRemoteRejectsDriftAndAmbiguousPushURL(t *testing.T) {
	repository, _ := mutationRepository(t)
	runFetchGit(t, repository, "remote", "add", "origin", "https://github.com/other/source.git")
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.UpdateRemote(
		context.Background(), repository, "origin",
		"https://github.com/old-owner/source.git", "https://github.com/new-owner/source.git",
	); err == nil {
		t.Fatal("updated a remote outside both immutable plan states")
	}
	runFetchGit(t, repository, "remote", "set-url", "origin", "https://github.com/old-owner/source.git")
	runFetchGit(t, repository, "remote", "set-url", "--add", "--push", "origin", "https://github.com/mirror/source.git")
	if _, err := runner.UpdateRemote(
		context.Background(), repository, "origin",
		"https://github.com/old-owner/source.git", "https://github.com/new-owner/source.git",
	); err == nil {
		t.Fatal("updated a remote with ambiguous push URLs")
	}
}
