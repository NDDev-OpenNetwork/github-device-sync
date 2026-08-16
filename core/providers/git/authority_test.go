package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunnersIgnoreAmbientRepositorySelectionAndExecutablePath(t *testing.T) {
	repositoryA, headA := mutationRepository(t)
	repositoryB, _ := mutationRepository(t)
	const expected = "https://example.invalid/expected.git"
	const target = "https://example.invalid/target.git"
	for _, repository := range []string{repositoryA, repositoryB} {
		command := exec.Command("/usr/bin/git", "remote", "add", "origin", expected)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("configure remote: %v: %s", err, output)
		}
	}

	marker := filepath.Join(t.TempDir(), "ambient-git-ran")
	fakeDirectory := t.TempDir()
	fakeGit := filepath.Join(fakeDirectory, "git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 97\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDirectory)
	t.Setenv("GIT_DIR", filepath.Join(repositoryB, ".git"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(repositoryB, ".git"))
	t.Setenv("GIT_WORK_TREE", repositoryB)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(repositoryB, ".git", "index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(repositoryB, ".git", "objects"))
	t.Setenv("GIT_NAMESPACE", "redirected")

	reader, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	info, err := reader.RepositoryInfo(context.Background(), repositoryA)
	observedHead, headErr := reader.HeadOID(context.Background(), repositoryA)
	expectedRoot, resolveErr := filepath.EvalSymlinks(repositoryA)
	if err != nil || headErr != nil || resolveErr != nil || info.WorktreeRoot != expectedRoot || observedHead != headA {
		t.Fatalf("repository A observation was redirected: info=%+v err=%v", info, err)
	}
	mutations, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	report, err := mutations.UpdateRemote(
		context.Background(), repositoryA, "origin", expected, target,
	)
	if err != nil || report.After.State != "target" {
		t.Fatalf("repository A mutation failed: report=%+v err=%v", report, err)
	}
	if got := readRemoteURL(t, repositoryA); got != target {
		t.Fatalf("repository A remote = %q, want %q", got, target)
	}
	if got := readRemoteURL(t, repositoryB); got != expected {
		t.Fatalf("repository B was mutated through ambient authority: %q", got)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("ambient PATH Git executed: %v", err)
	}
}

func readRemoteURL(t *testing.T, repository string) string {
	t.Helper()
	command := exec.Command(
		"/usr/bin/git", "--git-dir", filepath.Join(repository, ".git"),
		"config", "--local", "--get", "remote.origin.url",
	)
	command.Env = []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin:/usr/sbin:/sbin",
	}
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(output[:len(output)-1])
}
