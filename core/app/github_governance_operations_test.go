package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

func TestGitHubGovernancePlanIsReadyAndMissingWriteRuntimeFailsBeforeMutation(t *testing.T) {
	root := appTestRepositoryRoot(t)
	runtimePath := appTestRuntimeConfig(t, root)
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
	options := GitHubGovernanceOperationOptions{
		GitHubGovernanceOptions: GitHubGovernanceOptions{
			GitHubReadOptions: GitHubReadOptions{
				RuntimeConfig: runtimePath, InstallationID: "installation:github-opennetwork",
			},
			Owner: "NDDev-OpenNetwork", Repository: "github-device-sync",
		},
		ProjectionOperationOptions: ProjectionOperationOptions{
			StatePath: statePath, DeviceID: "device_01JEXAMPZ00000000000000000",
			SessionID: "test-session", ApprovalReference: "owner:test-approval",
		},
		MutationRuntimeConfig: filepath.Join(t.TempDir(), "missing-mutation-runtime.yaml"),
	}
	planned := services.PlanGitHubGovernance(context.Background(), root, options)
	data, ok := planned.Data.(GitHubGovernancePlanData)
	if planned.ExitClass != domain.ExitSuccess || !ok || data.Plan == nil ||
		data.Plan.Operation != githubGovernanceOperation || len(data.Plan.Steps) == 0 ||
		!data.ReadyForApply || data.ApplyBlocker != "" || len(data.Plan.Validate(services.Schemas)) != 0 {
		t.Fatalf("planned=%#v data=%#v", planned, data)
	}
	applied := services.ApplyGitHubGovernance(
		context.Background(), root, data.Plan.PlanID, options,
	)
	if applied.ExitClass != domain.ExitNotProven ||
		!appHasFinding(applied, "GDS_GITHUB_MUTATION_RUNTIME_NOT_PROVEN") ||
		appHasFinding(applied, "GDS_GITHUB_GOVERNANCE_MUTATION_BLOCKED") ||
		applied.Mutation.Attempted {
		t.Fatalf("applied=%#v", applied)
	}
}

func TestGitHubGovernanceObserverUsesPlanBoundEstateRoot(t *testing.T) {
	root := appTestRepositoryRoot(t)
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	observer := githubGovernanceObserver{
		services: services, root: root, estateRoot: filepath.Join(t.TempDir(), "missing-estate"),
		repositoryID: "repo_01M0EZ7TB3KNXNSP78Z8M64WXG", providerID: 1335994527,
		owner: "NDDev-OpenNetwork", name: "github-device-sync",
		installation: "installation:github-opennetwork",
	}
	_, err = observer.Observe(context.Background(), observer.repositoryID)
	if err == nil || !strings.Contains(err.Error(), "policy does not compile") {
		t.Fatalf("expected bound estate-root compilation failure, got %v", err)
	}
}

func appGovernanceOperationServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasPrefix(request.URL.Path, "/app/installations/"):
			providerID := strings.TrimSuffix(
				strings.TrimPrefix(request.URL.Path, "/app/installations/"), "/access_tokens",
			)
			_, _ = fmt.Fprintf(writer, `{"token":"ghs_%s","expires_at":"2099-01-01T00:00:00Z","permissions":{"actions":"read","administration":"read","checks":"read","contents":"read","metadata":"read","pull_requests":"read"},"repository_selection":"all"}`, providerID)
		case request.URL.Path == "/repos/NDDev-OpenNetwork/github-device-sync":
			_, _ = writer.Write([]byte(`{
  "id": 1335994527,
  "node_id": "R_gds",
  "name": "github-device-sync",
  "full_name": "NDDev-OpenNetwork/github-device-sync",
  "private": false,
  "visibility": "public",
  "fork": false,
  "archived": false,
  "disabled": false,
  "default_branch": "main",
  "html_url": "https://github.com/NDDev-OpenNetwork/github-device-sync",
  "owner": {"login": "NDDev-OpenNetwork"},
  "allow_merge_commit": true,
  "allow_squash_merge": false,
  "allow_rebase_merge": true,
  "allow_auto_merge": false,
  "allow_update_branch": false,
  "delete_branch_on_merge": false,
  "merge_commit_title": "PR_TITLE",
  "merge_commit_message": "PR_BODY",
  "squash_merge_commit_title": "PR_TITLE",
  "squash_merge_commit_message": "PR_BODY",
  "security_and_analysis": {"secret_scanning": {"status": "enabled"}}
}`))
		case strings.HasSuffix(request.URL.Path, "/actions/permissions/workflow"):
			_, _ = writer.Write([]byte(`{"default_workflow_permissions":"read","can_approve_pull_request_reviews":false}`))
		case strings.HasSuffix(request.URL.Path, "/actions/permissions/selected-actions"):
			_, _ = writer.Write([]byte(`{"github_owned_allowed":true,"verified_allowed":false,"patterns_allowed":["NDDev-OpenNetwork/ci-workflows/.github/workflows/*@*"]}`))
		case strings.HasSuffix(request.URL.Path, "/actions/permissions"):
			_, _ = writer.Write([]byte(`{"enabled":true,"allowed_actions":"selected","sha_pinning_required":true}`))
		case strings.HasSuffix(request.URL.Path, "/rulesets"):
			_, _ = writer.Write([]byte(`[]`))
		default:
			http.NotFound(writer, request)
		}
	}))
}
