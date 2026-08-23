package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
)

func TestGitHubInventoryAndReconciliationUseLiveReadOnlyRuntime(t *testing.T) {
	root := appTestRepositoryRoot(t)
	runtimePath := appTestRuntimeConfig(t, root)
	server := appTestGitHubServer(t, false)
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 5 * time.Second
	services.GitHubRuntimeBuildOptions = githubruntime.BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	}
	inventory := services.GitHubInventory(context.Background(), root, GitHubReadOptions{
		RuntimeConfig: runtimePath, InstallationID: "installation:github-personal",
	})
	if inventory.ExitClass != domain.ExitSuccess || inventory.Mutation.Attempted {
		t.Fatalf("inventory=%#v", inventory)
	}
	data, ok := inventory.Data.(GitHubInventoryData)
	if !ok || data.Inventory.TotalCount != 1 || data.AccountLogin != "example-user" {
		t.Fatalf("inventory data=%#v", inventory.Data)
	}
	governance := services.GitHubGovernance(context.Background(), root, GitHubGovernanceOptions{
		GitHubReadOptions: GitHubReadOptions{
			RuntimeConfig: runtimePath, InstallationID: "installation:github-personal",
		},
		Owner: "example-user", Repository: "example",
	})
	governanceData, ok := governance.Data.(GitHubGovernanceData)
	if governance.ExitClass != domain.ExitSuccess || !ok ||
		governanceData.Comparison.Status != "observed-only" ||
		governanceData.Snapshot.Repository.ID != 1002 ||
		!governanceData.Snapshot.Actions.SHAPinningRequired {
		t.Fatalf("governance=%#v", governance)
	}
	plan := services.ReconcileGitHub(context.Background(), root, GitHubReadOptions{
		RuntimeConfig: runtimePath,
	})
	if plan.ExitClass != domain.ExitSuccess || plan.Mutation.Attempted {
		t.Fatalf("plan=%#v", plan)
	}
	planData, ok := plan.Data.(ReconciliationPlanData)
	if !ok || len(planData.Result.Inventory.Repositories) != 5 ||
		len(planData.Result.Drift) != 5 || len(planData.ExternalMutations) != 0 {
		t.Fatalf("plan data=%#v", plan.Data)
	}
	summary := services.ReportEstateSummary(context.Background(), root, GitHubReadOptions{
		RuntimeConfig: runtimePath,
	})
	summaryData, ok := summary.Data.(EstateSummaryReport)
	if summary.ExitClass != domain.ExitSuccess || !ok || summaryData.Repositories != 5 ||
		summaryData.ManagementModes["observe-only"] != 3 ||
		summaryData.ManagementModes["managed"] != 2 ||
		summaryData.IdentityStates["unassigned"] != 5 ||
		summaryData.DriftByClass["identity"] != 5 {
		t.Fatalf("summary=%#v", summary)
	}
}

func TestGitHubInventoryRejectsWrongInstallationOwner(t *testing.T) {
	root := appTestRepositoryRoot(t)
	runtimePath := appTestRuntimeConfig(t, root)
	server := appTestGitHubServer(t, true)
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 5 * time.Second
	services.GitHubRuntimeBuildOptions = githubruntime.BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	}
	envelope := services.GitHubInventory(context.Background(), root, GitHubReadOptions{
		RuntimeConfig: runtimePath, InstallationID: "installation:github-personal",
	})
	if envelope.ExitClass != domain.ExitSecurity ||
		!appHasFinding(envelope, "GDS_GITHUB_INSTALLATION_ACCOUNT_MISMATCH") {
		t.Fatalf("envelope=%#v", envelope)
	}
}

func TestGitHubGovernanceCompareRejectsDifferentLocalRepository(t *testing.T) {
	root := appTestRepositoryRoot(t)
	runtimePath := appTestRuntimeConfig(t, root)
	server := appTestGitHubServer(t, false)
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 5 * time.Second
	services.GitHubRuntimeBuildOptions = githubruntime.BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	}
	envelope := services.GitHubGovernance(context.Background(), root, GitHubGovernanceOptions{
		GitHubReadOptions: GitHubReadOptions{
			RuntimeConfig: runtimePath, InstallationID: "installation:github-personal",
		},
		Owner: "example-user", Repository: "example", CompareLocal: true,
	})
	if envelope.ExitClass != domain.ExitSecurity ||
		!appHasFinding(envelope, "GDS_GITHUB_GOVERNANCE_LOCAL_IDENTITY_MISMATCH") {
		t.Fatalf("envelope=%#v", envelope)
	}
}

func TestGitHubInventoryReportsMissingRuntimeAsNotProven(t *testing.T) {
	root := appTestRepositoryRoot(t)
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	envelope := services.GitHubInventory(context.Background(), root, GitHubReadOptions{
		RuntimeConfig:  filepath.Join(t.TempDir(), "missing.yaml"),
		InstallationID: "installation:github-personal",
	})
	if envelope.ExitClass != domain.ExitNotProven ||
		!appHasFinding(envelope, "GDS_GITHUB_RUNTIME_NOT_PROVEN") {
		t.Fatalf("envelope=%#v", envelope)
	}
}

func appTestGitHubServer(t *testing.T, wrongPersonalOwner bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasPrefix(request.URL.Path, "/app/installations/"):
			providerID := strings.TrimSuffix(
				strings.TrimPrefix(request.URL.Path, "/app/installations/"), "/access_tokens",
			)
			_, _ = fmt.Fprintf(
				writer, `{"token":"ghs_%s","expires_at":"2099-01-01T00:00:00Z",`+
					`"permissions":{"actions":"read","administration":"read",`+
					`"checks":"read","contents":"read","metadata":"read",`+
					`"pull_requests":"read"},"repository_selection":"all"}`,
				providerID,
			)
		case request.URL.Path == "/installation/repositories":
			owner := "example-user"
			id := 1002
			if strings.Contains(request.Header.Get("Authorization"), "900001") {
				owner, id = "example-org", 1001
			} else if strings.Contains(request.Header.Get("Authorization"), "900003") {
				owner, id = "example-media", 1003
			} else if strings.Contains(request.Header.Get("Authorization"), "900004") {
				owner, id = "example-guild", 1004
			} else if strings.Contains(request.Header.Get("Authorization"), "900005") {
				owner, id = "NDDev-OpenNetwork", 1005
			} else if wrongPersonalOwner {
				owner = "unexpected-owner"
			}
			_, _ = fmt.Fprintf(writer, `{
  "total_count": 1,
  "repositories": [{
    "id": %d,
    "node_id": "R_fixture_%d",
    "name": "example",
    "full_name": %q,
    "private": true,
    "visibility": "private",
    "fork": false,
    "archived": false,
    "disabled": false,
    "default_branch": "main",
    "html_url": %q,
    "owner": {"login": %q}
  }]
}`, id, id, owner+"/example", "https://github.com/"+owner+"/example", owner)
		case strings.HasSuffix(request.URL.Path, "/actions/permissions/workflow"):
			_, _ = writer.Write([]byte(
				`{"default_workflow_permissions":"read","can_approve_pull_request_reviews":false}`,
			))
		case strings.HasSuffix(request.URL.Path, "/actions/permissions/selected-actions"):
			_, _ = writer.Write([]byte(`{
  "github_owned_allowed": true,
  "verified_allowed": false,
  "patterns_allowed": ["example-org/ci-workflows/.github/workflows/*@*"]
}`))
		case strings.HasSuffix(request.URL.Path, "/actions/permissions"):
			_, _ = writer.Write([]byte(
				`{"enabled":true,"allowed_actions":"selected","sha_pinning_required":true}`,
			))
		case strings.HasSuffix(request.URL.Path, "/rulesets"):
			_, _ = writer.Write([]byte(`[]`))
		case strings.HasPrefix(request.URL.Path, "/repos/"):
			parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
			if len(parts) != 3 {
				http.NotFound(writer, request)
				return
			}
			owner := parts[1]
			id := 1002
			if strings.EqualFold(owner, "example-org") {
				id = 1001
			}
			_, _ = fmt.Fprintf(writer, `{
  "id": %d,
  "node_id": "R_fixture_%d",
  "name": %q,
  "full_name": %q,
  "private": true,
  "visibility": "private",
  "fork": false,
  "archived": false,
  "disabled": false,
  "default_branch": "main",
  "html_url": %q,
  "owner": {"login": %q},
  "allow_squash_merge": true,
  "delete_branch_on_merge": true,
  "security_and_analysis": {"secret_scanning": {"status": "enabled"}}
}`, id, id, parts[2], owner+"/"+parts[2], "https://github.com/"+owner+"/"+parts[2], owner)
		default:
			http.NotFound(writer, request)
		}
	}))
}

func appTestRuntimeConfig(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-runtime.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "github-runtime.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	t.Setenv("GDS_GITHUB_APP_ORGANIZATION_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_APP_PERSONAL_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_APP_EXAMPLE_MEDIA_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_APP_GUILD_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_APP_OPENNETWORK_KEY", string(privatePEM))
	return path
}

func appTestRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	estateRoot := t.TempDir()
	for _, directory := range []string{"estate", "policies"} {
		if err := copyAppTestTree(root, estateRoot, directory); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, ".gds", "repository.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	text = strings.Replace(text,
		"  roles:\n    - \"project\"\n    - \"module\"\n",
		"  roles:\n    - \"control-plane\"\n", 1)
	text = strings.Replace(text, "    - \"public-module\"\n", "    - \"control-plane\"\n", 1)
	text = strings.Replace(text, "  context_profile: \"project-default\"\n", "  context_profile: \"control-plane\"\n", 1)
	start := strings.Index(text, "\nmodule:\n")
	end := strings.Index(text, "\nrelease:\n")
	if start < 0 || end <= start {
		t.Fatal("public engine anchor has no removable module section")
	}
	text = text[:start] + text[end:]
	anchorPath := filepath.Join(estateRoot, ".gds", "repository.yaml")
	if err := os.MkdirAll(filepath.Dir(anchorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(anchorPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"init", "--quiet"},
		{"config", "user.name", "GDS app test"},
		{"config", "user.email", "app-test@example.invalid"},
		{"add", "--all"},
		{"commit", "--quiet", "-m", "test: external estate fixture"},
	} {
		command := exec.Command("git", arguments...)
		command.Dir = estateRoot
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	t.Setenv("GDS_ESTATE_ROOT", estateRoot)
	return root
}

func copyAppTestTree(sourceRoot string, targetRoot string, relative string) error {
	source := filepath.Join(sourceRoot, relative)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("test estate source is not a regular file: %s", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
}

func appHasFinding(envelope domain.Envelope, code string) bool {
	for _, finding := range envelope.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
