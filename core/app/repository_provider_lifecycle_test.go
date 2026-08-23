package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitops"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/repository"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

func TestRepositoryTransferIsFailClosedWithoutUserTokenAdapter(t *testing.T) {
	ready, blocker := repositoryProviderMutationGate(
		estate.Config{}, estate.Assignment{}, estate.MutationCapability{}, "active", repository.TransferOperation,
	)
	if ready || blocker != repository.TransferApplyBlocker {
		t.Fatalf("ready=%v blocker=%q", ready, blocker)
	}
}

func TestRepositoryRenamePlanBindsProviderRemoteAndRejectsInsecureMutationRuntime(t *testing.T) {
	source := appTestRepositoryRoot(t)
	root := filepath.Join(t.TempDir(), "repository")
	clone := exec.Command("git", "clone", "--quiet", "--no-local", source, root)
	if output, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone fixture: %v: %s", err, output)
	}
	attach := exec.Command("git", "switch", "--create", "fixture-main")
	attach.Dir = root
	if output, err := attach.CombinedOutput(); err != nil {
		t.Fatalf("attach fixture HEAD: %v: %s", err, output)
	}
	remote := exec.Command(
		"git", "remote", "set-url", "origin",
		"https://github.com/NDDev-OpenNetwork/github-device-sync.git",
	)
	remote.Dir = root
	if output, err := remote.CombinedOutput(); err != nil {
		t.Fatalf("set fixture remote: %v: %s", err, output)
	}

	current, err := os.ReadFile(filepath.Join(root, ".gds", "repository.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	oldProvider := "  owner: \"NDDev-OpenNetwork\"\n  name: \"github-device-sync\"\n\nclassification:"
	newProvider := "  owner: \"NDDev-OpenNetwork\"\n  name: \"github-device-sync-renamed\"\n" +
		"  aliases:\n    - owner: \"NDDev-OpenNetwork\"\n      name: \"github-device-sync\"\n\nclassification:"
	candidate := strings.Replace(string(current), oldProvider, newProvider, 1)
	if candidate == string(current) {
		t.Fatal("fixture provider locator was not replaced")
	}
	candidatePath := filepath.Join(t.TempDir(), "repository.yaml")
	if err := os.WriteFile(candidatePath, []byte(candidate), 0o600); err != nil {
		t.Fatal(err)
	}

	runtimePath := appTestRuntimeConfig(t, root)
	mutationRuntimeFixture := filepath.Join(
		source, "tests", "fixtures", "schemas", "v1", "valid-github-mutation-runtime.yaml",
	)
	mutationRuntimeRaw, err := os.ReadFile(mutationRuntimeFixture)
	if err != nil {
		t.Fatal(err)
	}
	mutationRuntimePath := filepath.Join(t.TempDir(), "github-mutation-runtime.yaml")
	if err := os.WriteFile(mutationRuntimePath, mutationRuntimeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mutationRuntimePath, 0o644); err != nil {
		t.Fatal(err)
	}
	appModuleReleaseKeys(t)
	server := appGovernanceOperationServer(t)
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 5 * time.Second
	services.GitHubRuntimeBuildOptions = githubruntime.BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	}
	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateRoot, "state.db")
	store, err := state.Initialize(context.Background(), statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	options := RepositoryTransitionOptions{
		ProjectionOperationOptions: ProjectionOperationOptions{
			StatePath: statePath, DeviceID: "device_01JEXAMPZ00000000000000000",
			SessionID: "test-session", ApprovalReference: "owner:test-approval",
		},
		GitHubReadOptions:     GitHubReadOptions{RuntimeConfig: runtimePath},
		AnchorPath:            candidatePath,
		MutationRuntimeConfig: mutationRuntimePath,
	}
	planned := services.PlanRepositoryTransition(
		context.Background(), root, repository.RenameOperation, options,
	)
	data, ok := planned.Data.(RepositoryTransitionPlanData)
	if planned.ExitClass != domain.ExitSuccess || !ok || len(data.Plan.Steps) != 3 ||
		data.Plan.Steps[0].Action != repository.ProviderLifecycleAction ||
		data.Plan.Steps[1].Action != gitops.UpdateRemoteAction ||
		data.Plan.Steps[2].Action != "materialize-repository-anchor" ||
		!data.ReadyForApply || data.ApplyBlocker != "" || len(data.Plan.Validate(services.Schemas)) != 0 {
		t.Fatalf("planned=%#v data=%#v", planned, data)
	}
	_, remoteName, expectedURL, targetURL, err := gitops.RemoteUpdateStep(data.Plan.Steps[1])
	if err != nil || remoteName != "origin" ||
		expectedURL != "https://github.com/NDDev-OpenNetwork/github-device-sync.git" ||
		targetURL != "https://github.com/NDDev-OpenNetwork/github-device-sync-renamed.git" {
		t.Fatalf("remote step expected=%q target=%q remote=%q err=%v", expectedURL, targetURL, remoteName, err)
	}
	applied := services.ApplyRepositoryTransition(
		context.Background(), root, repository.RenameOperation, data.Plan.PlanID, options,
	)
	if applied.ExitClass != domain.ExitSecurity ||
		!appHasFinding(applied, "GDS_GITHUB_MUTATION_RUNTIME_SECURITY_VIOLATION") ||
		appHasFinding(applied, "GDS_REPOSITORY_PROVIDER_MUTATION_BLOCKED") ||
		applied.Mutation.Attempted {
		t.Fatalf("applied=%#v", applied)
	}
}
