package git

import (
	"context"
	"testing"
)

func TestRemoveRemoteUsesExactURLAndVerifiesAbsence(t *testing.T) {
	repository, _ := mutationRepository(t)
	runFetchGit(t, repository, "remote", "add", "upstream", "https://github.com/upstream/source.git")
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.RemoveRemote(
		context.Background(), repository, "upstream", "https://github.com/upstream/source.git",
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.State != "present" || report.After.State != "missing" {
		t.Fatalf("report=%#v", report)
	}
	if _, err := runner.RemoveRemote(
		context.Background(), repository, "upstream", "https://github.com/upstream/source.git",
	); err == nil {
		t.Fatal("removed an already absent remote")
	}
}

func TestRemoveRemoteRejectsUnexpectedURL(t *testing.T) {
	repository, _ := mutationRepository(t)
	runFetchGit(t, repository, "remote", "add", "upstream", "https://github.com/upstream/source.git")
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RemoveRemote(
		context.Background(), repository, "upstream", "https://github.com/other/source.git",
	); err == nil {
		t.Fatal("removed a remote with unexpected URL")
	}
}
