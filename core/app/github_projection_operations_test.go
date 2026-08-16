package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubchange"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

func TestGitHubProjectionPlanBindsExactCandidateAndMissingRuntimeBlocksMutation(t *testing.T) {
	source := appTestRepositoryRoot(t)
	root := filepath.Join(t.TempDir(), "repository")
	clone := exec.Command("git", "clone", "--quiet", "--no-local", source, root)
	if output, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone fixture: %v: %s", err, output)
	}
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	local, findings := services.projectionOperationContext(context.Background(), root)
	if len(findings) != 0 || len(local.candidate.Files) == 0 {
		t.Fatalf("candidate findings=%#v candidate=%#v", findings, local.candidate)
	}
	server := appProjectionOperationServer(t)
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	services.GitHubRuntimeBuildOptions = githubruntime.BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	}
	runtimePath := appTestRuntimeConfig(t, root)
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
	options := GitHubProjectionOperationOptions{
		GitHubReadOptions: GitHubReadOptions{RuntimeConfig: runtimePath},
		ProjectionOperationOptions: ProjectionOperationOptions{
			StatePath: statePath, DeviceID: "device_01JEXAMPZ00000000000000000",
			SessionID: "test-session", ApprovalReference: "owner:test-approval",
		},
		MutationRuntimeConfig: filepath.Join(t.TempDir(), "missing-mutation-runtime.yaml"),
	}
	planned := services.PlanGitHubProjection(context.Background(), root, options)
	data, ok := planned.Data.(GitHubProjectionPlanData)
	if planned.ExitClass != domain.ExitSuccess || !ok || data.Plan == nil || data.NoChanges ||
		!data.ReadyForApply || data.ApplyBlocker != "" ||
		data.Plan.Operation != githubProjectionOperation ||
		len(data.Plan.Validate(services.Schemas)) != 0 || githubchange.ValidatePlan(*data.Plan) != nil {
		t.Fatalf("planned=%#v data=%#v", planned, data)
	}
	if len(data.Plan.Steps) != len(local.candidate.Files)+2 ||
		data.RequiredOps[0] != "branch" || data.RequiredOps[len(data.RequiredOps)-1] != "workflow-caller" {
		t.Fatalf("steps=%d required=%#v files=%d", len(data.Plan.Steps), data.RequiredOps, len(local.candidate.Files))
	}
	for _, step := range data.Plan.Steps[1 : len(data.Plan.Steps)-1] {
		parameters, err := githubchange.StepParameters(step)
		if err != nil || parameters.Content == nil || parameters.Content.FinalStatus != "added" {
			t.Fatalf("step=%#v err=%v", step, err)
		}
	}
	applied := services.ApplyGitHubProjection(context.Background(), root, data.Plan.PlanID, options)
	if applied.ExitClass != domain.ExitNotProven ||
		!appHasFinding(applied, "GDS_GITHUB_MUTATION_RUNTIME_NOT_PROVEN") ||
		appHasFinding(applied, "GDS_GITHUB_PROJECTION_MUTATION_BLOCKED") ||
		applied.Mutation.Attempted {
		t.Fatalf("applied=%#v", applied)
	}
}

func appProjectionOperationServer(t *testing.T) *httptest.Server {
	t.Helper()
	baseSHA := strings.Repeat("a", 40)
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasPrefix(request.URL.Path, "/app/installations/"):
			providerID := strings.TrimSuffix(
				strings.TrimPrefix(request.URL.Path, "/app/installations/"), "/access_tokens",
			)
			_, _ = fmt.Fprintf(writer, `{"token":"ghs_%s","expires_at":"2099-01-01T00:00:00Z","permissions":{"actions":"read","administration":"read","checks":"read","contents":"read","metadata":"read","pull_requests":"read"},"repository_selection":"all"}`, providerID)
		case request.URL.Path == "/repos/example-org/github-device-sync":
			_, _ = writer.Write([]byte(`{
  "id": 1000000001,
  "node_id": "R_gds",
  "name": "github-device-sync",
  "full_name": "example-org/github-device-sync",
  "private": false,
  "visibility": "public",
  "fork": false,
  "archived": false,
  "disabled": false,
  "default_branch": "main",
  "html_url": "https://github.com/example-org/github-device-sync",
  "owner": {"login": "example-org"}
}`))
		case request.URL.Path == "/repos/example-org/github-device-sync/git/ref/heads/main":
			_, _ = fmt.Fprintf(writer, `{"ref":"refs/heads/main","object":{"sha":%q}}`, baseSHA)
		case strings.Contains(request.URL.Path, "/git/ref/heads/gds/projection-"):
			http.NotFound(writer, request)
		case strings.Contains(request.URL.Path, "/contents/"):
			http.NotFound(writer, request)
		case strings.HasSuffix(request.URL.Path, "/pulls"):
			_, _ = writer.Write([]byte(`[]`))
		default:
			http.NotFound(writer, request)
		}
	}))
}
