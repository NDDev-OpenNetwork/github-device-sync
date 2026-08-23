package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForkSyncRejectsForceModeAtCLIContract(t *testing.T) {
	exitCode, envelope, stderr := executeJSON(t, "--json", "fork", "sync", "--force")
	if exitCode != 4 || envelope.Mutation.Attempted ||
		!containsFinding(envelope.Findings, "GDS_CLI_INPUT_INVALID") {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
}

func TestForkSyncFastForwardsWithoutForceAndVerifiesExactRefs(t *testing.T) {
	checkout, origin, upstream := forkCLIFixture(t)
	writer := filepath.Join(t.TempDir(), "upstream-writer")
	runSessionGit(t, filepath.Dir(writer), "clone", "-q", upstream, writer)
	runSessionGit(t, writer, "config", "user.name", "Upstream")
	runSessionGit(t, writer, "config", "user.email", "upstream@example.invalid")
	if err := os.WriteFile(filepath.Join(writer, "upstream.txt"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, writer, "add", "upstream.txt")
	runSessionGit(t, writer, "commit", "-qm", "upstream")
	target := runSessionGit(t, writer, "rev-parse", "HEAD")
	runSessionGit(t, writer, "push", "-q", "origin", "main")
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	statePath := sessionStatePath(t)
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", checkout, "fork", "sync", "--plan",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "fork-sync",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	exitCode, unapproved, stderr := executeJSON(
		t, "--json", "fork", "sync", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "fork-sync",
	)
	if exitCode != 6 || unapproved.Mutation.Attempted {
		t.Fatalf("unapproved exit=%d stderr=%q envelope=%#v", exitCode, stderr, unapproved)
	}
	exitCode, applied, stderr := executeJSON(
		t, "--json", "fork", "sync", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "fork-sync", "--approval-ref", "owner-approved:fork-sync",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	if head := runSessionGit(t, checkout, "rev-parse", "HEAD"); head != target {
		t.Fatalf("checkout head=%s want=%s", head, target)
	}
	if remote := runSessionGit(t, origin, "rev-parse", "refs/heads/main"); remote != target {
		t.Fatalf("origin head=%s want=%s", remote, target)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "fork", "sync", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "fork-sync",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func TestForkDetachPreservesUpstreamHistoryAndRemovesOnlyLocalRemote(t *testing.T) {
	repository, baseCandidate, statePath := repositoryOnboardFixture(t)
	raw, err := os.ReadFile(baseCandidate)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "\nrelease:\n", `
fork:
  upstream:
    provider: "github"
    repository_id: 987654321
    owner: "upstream-owner"
    name: "upstream-project"
  policy: "maintained-patch"
  sync_branch: "main"
  preserve_fork_commits: true
  allow_force_sync: false

release:
`, 1))
	if err := os.Mkdir(filepath.Join(repository, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchorPath := filepath.Join(repository, ".gds", "repository.yaml")
	if err := os.WriteFile(anchorPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, repository, "remote", "add", "upstream", "https://github.com/upstream-owner/upstream-project.git")
	runSessionGit(t, repository, "add", ".gds/repository.yaml")
	runSessionGit(t, repository, "commit", "-qm", "declare fork")
	head := runSessionGit(t, repository, "rev-parse", "HEAD")
	runSessionGit(t, repository, "update-ref", "refs/remotes/origin/main", head)
	targetRaw := []byte(strings.Replace(string(raw), `policy: "maintained-patch"`, `policy: "detached"`, 1))
	targetAnchor := filepath.Join(t.TempDir(), "detached.yaml")
	if err := os.WriteFile(targetAnchor, targetRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", repository, "fork", "detach", "--plan",
		"--anchor", targetAnchor, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "fork-detach",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	exitCode, applied, stderr := executeJSON(
		t, "--json", "fork", "detach", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "fork-detach", "--approval-ref", "owner-approved:fork-detach",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	if remotes := strings.Fields(runSessionGit(t, repository, "remote")); len(remotes) != 1 || remotes[0] != "origin" {
		t.Fatalf("remotes=%#v", remotes)
	}
	observed, err := os.ReadFile(anchorPath)
	if err != nil || string(observed) != string(targetRaw) {
		t.Fatalf("detached anchor mismatch err=%v", err)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "fork", "detach", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "fork-detach",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func forkCLIFixture(t *testing.T) (string, string, string) {
	t.Helper()
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	runSessionGit(t, filepath.Dir(upstream), "init", "--bare", "-q", upstream)
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.Mkdir(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, seed, "init", "-q", "-b", "main")
	runSessionGit(t, seed, "config", "user.name", "Seed")
	runSessionGit(t, seed, "config", "user.email", "seed@example.invalid")
	if err := os.Mkdir(filepath.Join(seed, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	anchor = []byte(strings.Replace(string(anchor), "\nrelease:\n", `
fork:
  upstream:
    provider: "github"
    repository_id: 987654321
    owner: "upstream-owner"
    name: "upstream-project"
  policy: "maintained-patch"
  sync_branch: "main"
  preserve_fork_commits: true
  allow_force_sync: false

release:
`, 1))
	if err := os.WriteFile(filepath.Join(seed, ".gds", "repository.yaml"), anchor, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "fixture.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, seed, "add", ".gds/repository.yaml", "fixture.txt")
	runSessionGit(t, seed, "commit", "-qm", "base")
	runSessionGit(t, seed, "remote", "add", "upstream", upstream)
	runSessionGit(t, seed, "push", "-q", "upstream", "main")
	runSessionGit(t, upstream, "symbolic-ref", "HEAD", "refs/heads/main")
	origin := filepath.Join(t.TempDir(), "origin.git")
	runSessionGit(t, filepath.Dir(origin), "clone", "--bare", "-q", upstream, origin)
	checkout := filepath.Join(t.TempDir(), "checkout")
	runSessionGit(t, filepath.Dir(checkout), "clone", "-q", origin, checkout)
	runSessionGit(t, checkout, "config", "user.name", "Fork")
	runSessionGit(t, checkout, "config", "user.email", "fork@example.invalid")
	runSessionGit(t, checkout, "remote", "add", "upstream", upstream)
	return checkout, origin, upstream
}
