package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repositoryOnboardFixture(t *testing.T) (string, string, string) {
	t.Helper()
	disableGitFixtureMaintenance(t)
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, repository, "init", "-q", "-b", "main")
	runSessionGit(t, repository, "config", "user.name", "GDS Fixture")
	runSessionGit(t, repository, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, repository, "add", "README.md")
	runSessionGit(t, repository, "commit", "-qm", "initial")
	head := runSessionGit(t, repository, "rev-parse", "HEAD")
	runSessionGit(t, repository, "remote", "add", "origin", "https://github.com/example-user/example-project.git")
	runSessionGit(t, repository, "update-ref", "refs/remotes/origin/main", head)
	runSessionGit(t, repository, "branch", "--set-upstream-to=origin/main", "main")
	candidate := filepath.Join(t.TempDir(), "repository.yaml")
	raw, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return repository, candidate, sessionStatePath(t)
}

func TestRepositoryOnboardRequiresApprovalAndVerifiesExactAnchor(t *testing.T) {
	repository, candidate, statePath := repositoryOnboardFixture(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "onboard", "--plan",
		"--anchor", candidate, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-onboard",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	exitCode, unapproved, stderr := executeJSON(
		t, "--json", "repository", "onboard", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "repository-onboard",
	)
	if exitCode != 6 || unapproved.Mutation.Attempted {
		t.Fatalf("unapproved exit=%d stderr=%q envelope=%#v", exitCode, stderr, unapproved)
	}
	anchorPath := filepath.Join(repository, ".gds", "repository.yaml")
	if _, err := os.Lstat(anchorPath); !os.IsNotExist(err) {
		t.Fatalf("unapproved onboarding wrote anchor: %v", err)
	}
	exitCode, applied, stderr := executeJSON(
		t, "--json", "repository", "onboard", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "repository-onboard",
		"--approval-ref", "owner-approved:repository-onboard",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	want, _ := os.ReadFile(candidate)
	got, err := os.ReadFile(anchorPath)
	if err != nil || string(got) != string(want) {
		t.Fatalf("anchor mismatch err=%v\ngot=%q\nwant=%q", err, got, want)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "repository", "onboard", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "repository-onboard",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func TestRepositoryOnboardAllowsCleanUnpublishedTaskBranch(t *testing.T) {
	repository, candidate, statePath := repositoryOnboardFixture(t)
	runSessionGit(t, repository, "switch", "-qc", "task/gds-onboarding")
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "onboard", "--plan",
		"--anchor", candidate, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "task-branch-onboard",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
}

func TestRepositoryOnboardAcceptsCleanEmbeddedDetachedGitlink(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, source, "init", "-q", "-b", "main")
	runSessionGit(t, source, "config", "user.name", "Embedded Source")
	runSessionGit(t, source, "config", "user.email", "embedded@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "module.txt"), []byte("module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, source, "add", "module.txt")
	runSessionGit(t, source, "commit", "-qm", "initialize module")

	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, parent, "init", "-q", "-b", "main")
	runSessionGit(t, parent, "config", "user.name", "Embedded Parent")
	runSessionGit(t, parent, "config", "user.email", "parent@example.invalid")
	runSessionGit(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "modules/example-project")
	runSessionGit(t, parent, "add", ".gitmodules", "modules/example-project")
	runSessionGit(t, parent, "commit", "-qm", "add embedded module")
	module := filepath.Join(parent, "modules", "example-project")
	runSessionGit(t, module, "remote", "set-url", "origin", "https://github.com/example-user/example-project.git")
	runSessionGit(t, module, "checkout", "-q", "--detach", "HEAD")

	candidate := filepath.Join(t.TempDir(), "repository.yaml")
	raw, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := sessionStatePath(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", module, "repository", "onboard", "--plan",
		"--anchor", candidate, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "embedded-onboard",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	exitCode, applied, stderr := executeJSON(
		t, "--json", "repository", "onboard", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "embedded-onboard", "--approval-ref", "owner-approved:embedded-onboard",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "repository", "onboard", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "embedded-onboard",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func TestRepositoryOnboardRejectsDirtyOrAlreadyManagedCheckout(t *testing.T) {
	repository, candidate, statePath := repositoryOnboardFixture(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exitCode, dirty, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "onboard", "--plan",
		"--anchor", candidate, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-dirty",
	)
	if exitCode != 12 || !containsFinding(dirty.Findings, "GDS_REPOSITORY_ONBOARD_GIT_STATE_UNSAFE") {
		t.Fatalf("dirty exit=%d stderr=%q envelope=%#v", exitCode, stderr, dirty)
	}
	if err := os.Remove(filepath.Join(repository, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(candidate)
	if err := os.WriteFile(filepath.Join(repository, ".gds", "repository.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, repository, "add", ".gds/repository.yaml")
	runSessionGit(t, repository, "commit", "-qm", "existing anchor")
	runSessionGit(t, repository, "update-ref", "refs/remotes/origin/main", runSessionGit(t, repository, "rev-parse", "HEAD"))
	exitCode, managed, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "onboard", "--plan",
		"--anchor", candidate, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-managed",
	)
	if exitCode != 3 || !containsFinding(managed.Findings, "GDS_REPOSITORY_ALREADY_MANAGED") {
		t.Fatalf("managed exit=%d stderr=%q envelope=%#v", exitCode, stderr, managed)
	}
}

func TestRepositoryRenameRequiresProviderEvidenceWithoutMutatingAnchor(t *testing.T) {
	repository, currentAnchor, statePath := repositoryOnboardFixture(t)
	if err := os.Mkdir(filepath.Join(repository, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(currentAnchor)
	if err != nil {
		t.Fatal(err)
	}
	anchorPath := filepath.Join(repository, ".gds", "repository.yaml")
	if err := os.WriteFile(anchorPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, repository, "add", ".gds/repository.yaml")
	runSessionGit(t, repository, "commit", "-qm", "add repository anchor")
	head := runSessionGit(t, repository, "rev-parse", "HEAD")
	runSessionGit(t, repository, "update-ref", "refs/remotes/origin/main", head)
	targetRaw := []byte(strings.Replace(
		string(raw),
		"  name: \"example-project\"\n",
		"  name: \"renamed-project\"\n  aliases:\n    - owner: \"example-user\"\n      name: \"example-project\"\n",
		1,
	))
	targetAnchor := filepath.Join(t.TempDir(), "renamed.yaml")
	if err := os.WriteFile(targetAnchor, targetRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	// Isolate the runtime-config default: this case asserts the fail-closed
	// "runtime not proven" path, which only holds when no GDS runtime config
	// resolves. Without this the operator's real config is found and the CLI
	// reaches the live GitHub API instead of failing closed.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "rename", "--plan",
		"--anchor", targetAnchor, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-rename",
	)
	if exitCode != 3 || planned.Mutation.Attempted ||
		!containsFinding(planned.Findings, "GDS_GITHUB_RUNTIME_NOT_PROVEN") {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	observed, err := os.ReadFile(anchorPath)
	if err != nil || string(observed) != string(raw) {
		t.Fatalf("plan changed anchor err=%v", err)
	}
}

func TestRepositoryDeleteRequiresArchivedCompleteIdentityAnalysisAndExactConfirmation(t *testing.T) {
	repository, candidate, statePath := repositoryOnboardFixture(t)
	if err := os.Mkdir(filepath.Join(repository, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `lifecycle: "active"`, `lifecycle: "archived"`, 1))
	anchorPath := filepath.Join(repository, ".gds", "repository.yaml")
	if err := os.WriteFile(anchorPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, repository, "add", ".gds/repository.yaml")
	runSessionGit(t, repository, "commit", "-qm", "archive repository")
	head := runSessionGit(t, repository, "rev-parse", "HEAD")
	runSessionGit(t, repository, "update-ref", "refs/remotes/origin/main", head)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	// Isolate the runtime-config default: this case asserts the fail-closed
	// "runtime not proven" path, which only holds when no GDS runtime config
	// resolves. Without this the operator's real config is found and the CLI
	// reaches the live GitHub API instead of failing closed.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	inventoryRoot := filepath.Dir(repository)
	unmanaged := filepath.Join(inventoryRoot, "unmanaged")
	if err := os.Mkdir(unmanaged, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, unmanaged, "init", "-q")
	exitCode, incomplete, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "delete", "--plan",
		"--inventory-root", inventoryRoot, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-delete",
	)
	if exitCode != 3 || !containsFinding(incomplete.Findings, "GDS_IDENTITY_INDEX_ANCHOR_REQUIRED") {
		t.Fatalf("incomplete exit=%d stderr=%q envelope=%#v", exitCode, stderr, incomplete)
	}
	if err := os.RemoveAll(unmanaged); err != nil {
		t.Fatal(err)
	}
	exitCode, unconfirmed, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "delete", "--plan",
		"--inventory-root", inventoryRoot, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-delete",
	)
	if exitCode != 6 || !containsFinding(unconfirmed.Findings, "GDS_REPOSITORY_DELETE_CONFIRMATION_REQUIRED") {
		t.Fatalf("unconfirmed exit=%d stderr=%q envelope=%#v", exitCode, stderr, unconfirmed)
	}
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "delete", "--plan",
		"--inventory-root", inventoryRoot,
		"--confirm-repository-id", "repo_01JEXAMPZ0000000000000000C",
		"--confirm-provider-id", "123456789", "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-delete",
	)
	if exitCode != 3 || planned.Mutation.Attempted ||
		!containsFinding(planned.Findings, "GDS_GITHUB_RUNTIME_NOT_PROVEN") {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	if _, err := os.Stat(repository); err != nil {
		t.Fatalf("provider evidence failure changed checkout: %v", err)
	}
}

// Deletion is the one irreversible mutation here, and every path into it must
// end without touching the provider unless an exact stored plan and its
// retirement evidence both hold.
//
// The pre-existing delete test asserts the planning guards -- incomplete
// identity index, missing exact confirmation -- and never reaches the point
// where a deletion could be authorized. These cover the half it does not: what
// happens when somebody calls apply directly.
func TestRepositoryDeleteApplyRefusesAPlanThatDoesNotExist(t *testing.T) {
	repository, _, statePath := repositoryOnboardFixture(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "delete",
		"--apply", "plan_01KX7BV07RHD6KRA4Z4J0KCHGR",
		"--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-delete",
	)
	if exitCode == 0 {
		t.Fatalf("apply succeeded on an absent plan: stderr = %q, envelope = %#v", stderr, envelope)
	}
	if envelope.Mutation.Attempted || envelope.Mutation.Completed {
		t.Fatalf("mutation = %#v", envelope.Mutation)
	}
}

// A state path that cannot be opened must end the operation, not be treated as
// an empty journal that happens to contain no objection.
func TestRepositoryDeleteApplyRefusesAnUnreachableStateStore(t *testing.T) {
	repository, _, _ := repositoryOnboardFixture(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "delete",
		"--apply", "plan_01KX7BV07RHD6KRA4Z4J0KCHGR",
		"--state-path", filepath.Join(t.TempDir(), "absent", "state.db"),
		"--device-id", syncTestDeviceID, "--session-id", "repository-delete",
	)
	if exitCode == 0 || envelope.Mutation.Attempted {
		t.Fatalf("apply exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
}

// An approval authorizes an exact plan. It does not supply one, and it does not
// supply the retirement evidence that plan would have had to carry.
func TestRepositoryDeleteApplyIsNotUnlockedByAnApprovalAlone(t *testing.T) {
	repository, _, statePath := repositoryOnboardFixture(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	approval := filepath.Join(t.TempDir(), "approval.json")
	if err := os.WriteFile(approval, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	exitCode, envelope, _ := executeJSON(
		t, "--json", "--cwd", repository, "repository", "delete",
		"--apply", "plan_01KX7BV07RHD6KRA4Z4J0KCHGR",
		"--approval-ref", approval, "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-delete",
	)
	if exitCode == 0 || envelope.Mutation.Attempted {
		t.Fatalf("apply exit = %d, envelope = %#v", exitCode, envelope)
	}
}

// A retirement question is repository-wide, so a second worktree over the same
// history must block the plan. `gitprovider.Status` has always returned
// `Worktrees` and the observer has always read them; nothing looked, so an
// archived, relationship-free repository with a clean default branch could hold
// the only copy of unfinished work one directory away and still plan.
func TestRepositoryDeletePlanBlocksOnASecondaryWorktree(t *testing.T) {
	repository, candidate, statePath := repositoryOnboardFixture(t)
	if err := os.Mkdir(filepath.Join(repository, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `lifecycle: "active"`, `lifecycle: "archived"`, 1))
	if err := os.WriteFile(
		filepath.Join(repository, ".gds", "repository.yaml"), raw, 0o644,
	); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, repository, "add", ".gds/repository.yaml")
	runSessionGit(t, repository, "commit", "-qm", "archive repository")
	head := runSessionGit(t, repository, "rev-parse", "HEAD")
	runSessionGit(t, repository, "update-ref", "refs/remotes/origin/main", head)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// The second worktree is what the plan must notice. It is clean and
	// attached, so nothing about the current checkout changes.
	secondary := filepath.Join(t.TempDir(), "secondary")
	runSessionGit(t, repository, "worktree", "add", "-b", "unfinished", secondary)

	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", repository, "repository", "delete", "--plan",
		"--inventory-root", filepath.Dir(repository), "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "repository-delete",
	)
	if !containsFinding(envelope.Findings, "GDS_REPOSITORY_DELETE_SECONDARY_WORKTREE") {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	if envelope.Mutation.Attempted {
		t.Fatalf("mutation = %#v", envelope.Mutation)
	}
	for _, finding := range envelope.Findings {
		if finding.Code != "GDS_REPOSITORY_DELETE_SECONDARY_WORKTREE" {
			continue
		}
		if finding.Evidence["path"] == "" || finding.Evidence["branch"] != "unfinished" {
			t.Fatalf("evidence names no actionable worktree: %#v", finding.Evidence)
		}
	}
}
