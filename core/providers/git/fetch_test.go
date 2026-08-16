package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runFetchGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func TestFetchRemoteIsNonIntegratingAndClassifiesForcedUpdates(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(writer, "fixture.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, writer, "commit", "-qam", "second")
	runFetchGit(t, writer, "push", "-q", "origin", "main")

	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	remoteOID, err := runner.ObserveRemoteBranch(
		context.Background(), client, "origin", "refs/heads/main",
	)
	if err != nil || remoteOID == firstOID {
		t.Fatalf("remote branch oid=%q first=%q err=%v", remoteOID, firstOID, err)
	}
	if localOID := stringsTrim(runFetchGit(t, client, "rev-parse", "refs/remotes/origin/main")); localOID != firstOID {
		t.Fatalf("ls-remote mutated local origin/main: %s", localOID)
	}
	report, err := runner.FetchRemote(context.Background(), client, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) != 1 || report.Changes[0].Kind != "fast-forward" {
		t.Fatalf("fast-forward report=%#v", report)
	}
	clientHead := stringsTrim(runFetchGit(t, client, "rev-parse", "HEAD"))
	if clientHead != firstOID {
		t.Fatalf("fetch integrated client HEAD: got=%s want=%s", clientHead, firstOID)
	}
	if _, err := os.Lstat(filepath.Join(client, ".git", "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("fetch wrote FETCH_HEAD: %v", err)
	}

	runFetchGit(t, writer, "reset", "--hard", firstOID)
	if err := os.WriteFile(filepath.Join(writer, "fixture.txt"), []byte("rewritten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, writer, "commit", "-qam", "rewritten")
	runFetchGit(t, writer, "push", "-q", "--force", "origin", "main")
	report, err = runner.FetchRemote(context.Background(), client, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) != 1 || report.Changes[0].Kind != "forced-update" {
		t.Fatalf("forced-update report=%#v", report)
	}
	if stringsTrim(runFetchGit(t, client, "rev-parse", "HEAD")) != firstOID {
		t.Fatal("forced fetch integrated client HEAD")
	}

	runFetchGit(t, client, "config", "core.sshCommand", "false")
	if _, err := runner.FetchRemote(context.Background(), client, "origin"); err == nil {
		t.Fatal("fetch accepted executable local Git configuration")
	}
}

func TestFetchURLRejectsCredentialsHelpersAndSymlinkedLocalTargets(t *testing.T) {
	root := t.TempDir()
	for _, unsafe := range []string{
		"ext::sh -c id", "https://user:password@example.test/repo.git",
		"https://token@example.test/repo.git", "ssh://token@example.test/repo.git",
		"ssh://git:password@example.test/repo.git", "token@example.test:repository.git",
		"https://example.test/repo.git?token=secret", "-upload-pack=unsafe",
	} {
		if err := validateFetchURL(root, unsafe); err == nil {
			t.Fatalf("unsafe fetch URL accepted: %q", unsafe)
		}
	}
	for _, safe := range []string{
		"https://example.test/repo.git", "ssh://git@example.test/repo.git",
		"git@example.test:repository.git", "example.test:repository.git",
	} {
		if err := validateFetchURL(root, safe); err != nil {
			t.Errorf("credential-free fetch URL rejected: %q: %v", safe, err)
		}
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := validateFetchURL(root, link); err == nil {
		t.Fatal("symlinked local fetch target accepted")
	}
}

func stringsTrim(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
