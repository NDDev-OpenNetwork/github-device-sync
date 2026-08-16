package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitlinkMutationFixture(t *testing.T) (string, string, string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, source, "init", "-q", "-b", "task/pin")
	runFetchGit(t, source, "config", "user.name", "GDS Fixture")
	runFetchGit(t, source, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(source, ".gitmodules"), []byte(
		"[submodule \"module\"]\n\tpath = modules/module\n\turl = https://github.com/example/module.git\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, source, "add", ".gitmodules", "README.md")
	oldOID := "0123456789abcdef0123456789abcdef01234567"
	newOID := "1123456789abcdef0123456789abcdef01234567"
	runFetchGit(t, source, "update-index", "--add", "--cacheinfo", "160000,"+oldOID+",modules/module")
	runFetchGit(t, source, "commit", "-qm", "initial gitlink")
	repository := filepath.Join(t.TempDir(), "client")
	runFetchGit(t, filepath.Dir(repository), "clone", "-q", "--no-recurse-submodules", source, repository)
	return repository, oldOID, newOID
}

func TestUpdateGitlinkChangesOnlyExactUninitializedIndexEntry(t *testing.T) {
	repository, oldOID, newOID := gitlinkMutationFixture(t)
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.UpdateGitlink(
		context.Background(), repository, "module", oldOID, newOID,
	)
	if err != nil {
		t.Fatalf("update error=%v report=%+v", err, report)
	}
	if report.Before.GitlinkOID != oldOID || report.After.GitlinkOID != newOID ||
		report.After.Staged != 1 || report.After.HeadOID != report.Before.HeadOID {
		t.Fatalf("report=%+v", report)
	}
	if got := strings.Fields(runFetchGit(t, repository, "ls-files", "--stage", "modules/module"))[1]; got != newOID {
		t.Fatalf("gitlink=%s want=%s", got, newOID)
	}
}

func TestUpdateGitlinkPreservesDirtyCheckout(t *testing.T) {
	repository, oldOID, newOID := gitlinkMutationFixture(t)
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, _ := NewMutationRunner()
	if _, err := runner.UpdateGitlink(context.Background(), repository, "module", oldOID, newOID); err == nil {
		t.Fatal("gitlink mutation accepted unrelated dirty state")
	}
	if got := strings.Fields(runFetchGit(t, repository, "ls-files", "--stage", "modules/module"))[1]; got != oldOID {
		t.Fatalf("blocked mutation changed gitlink: %s", got)
	}
}
