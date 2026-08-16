package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetRepositoryGovernanceReturnsBoundedTypedEvidence(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("X-GitHub-Request-Id", strings.ReplaceAll(request.URL.Path, "/", "-"))
		switch request.URL.Path {
		case "/repos/example/repository":
			_, _ = writer.Write([]byte(`{
  "id": 1,
  "node_id": "node-1",
  "name": "repository",
  "full_name": "example/repository",
  "private": true,
  "visibility": "private",
  "fork": false,
  "archived": false,
  "disabled": false,
  "default_branch": "main",
  "html_url": "https://github.com/example/repository",
  "owner": {"login": "example"},
  "allow_merge_commit": false,
  "allow_squash_merge": true,
  "allow_rebase_merge": true,
  "allow_auto_merge": true,
  "allow_update_branch": true,
  "delete_branch_on_merge": true,
  "squash_merge_commit_title": "PR_TITLE",
  "squash_merge_commit_message": "PR_BODY",
  "security_and_analysis": {
    "advanced_security": {"status": "enabled"},
    "secret_scanning": {"status": "enabled"}
  }
}`))
		case "/repos/example/repository/actions/permissions":
			_, _ = writer.Write([]byte(
				`{"enabled":true,"allowed_actions":"selected","sha_pinning_required":true}`,
			))
		case "/repos/example/repository/actions/permissions/selected-actions":
			_, _ = writer.Write([]byte(`{
  "github_owned_allowed": true,
  "verified_allowed": false,
  "patterns_allowed": ["example-org/ci-workflows/.github/workflows/*@*"]
}`))
		case "/repos/example/repository/actions/permissions/workflow":
			_, _ = writer.Write([]byte(
				`{"default_workflow_permissions":"read","can_approve_pull_request_reviews":false}`,
			))
		case "/repos/example/repository/immutable-releases":
			_, _ = writer.Write([]byte(`{"enabled":true,"enforced_by_owner":false}`))
		case "/repos/example/repository/rulesets":
			if request.URL.Query().Get("per_page") != "100" || request.URL.Query().Get("page") != "1" {
				t.Errorf("ruleset query=%s", request.URL.RawQuery)
			}
			_, _ = writer.Write([]byte(`[
  {"id": 2,"name":"tags","target":"tag","source_type":"Repository","source":"example/repository","enforcement":"active"},
  {"id": 1,"name":"main","target":"branch","source_type":"Organization","source":"example","enforcement":"active"}
]`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)
	snapshot, err := client.GetRepositoryGovernance(
		context.Background(), "example", "repository",
	)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 6 || snapshot.InstallationID != "installation:test" ||
		snapshot.Repository.ID != 1 || !snapshot.Repository.Merge.AllowSquashMerge ||
		!snapshot.Repository.Merge.DeleteBranchOnMerge ||
		snapshot.Repository.Security.Features["advanced_security"] != "enabled" ||
		!snapshot.Actions.Enabled || snapshot.Actions.AllowedActions != "selected" ||
		!snapshot.Actions.SHAPinningRequired || snapshot.SelectedActions == nil ||
		!snapshot.SelectedActions.GitHubOwnedAllowed ||
		len(snapshot.SelectedActions.PatternsAllowed) != 1 ||
		snapshot.Workflow.Default != "read" ||
		!snapshot.ImmutableReleases.Enabled || snapshot.ImmutableReleases.EnforcedByOwner ||
		len(snapshot.Rulesets) != 2 || snapshot.Rulesets[0].ID != 1 ||
		snapshot.Permissions.Status != "verified-exact" || len(snapshot.RequestIDs) != 6 {
		t.Fatalf("snapshot=%#v requests=%d", snapshot, requests.Load())
	}
}

func TestGetRepositoryGovernanceTreatsDocumentedImmutableRelease404AsDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/example/repository":
			_, _ = writer.Write([]byte(repositoryJSON(1, "example", "repository", false)))
		case "/repos/example/repository/actions/permissions":
			_, _ = writer.Write([]byte(`{"enabled":true,"allowed_actions":"all"}`))
		case "/repos/example/repository/actions/permissions/workflow":
			_, _ = writer.Write([]byte(`{"default_workflow_permissions":"read"}`))
		case "/repos/example/repository/immutable-releases":
			http.NotFound(writer, request)
		case "/repos/example/repository/rulesets":
			_, _ = writer.Write([]byte(`[]`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)
	snapshot, err := client.GetRepositoryGovernance(context.Background(), "example", "repository")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ImmutableReleases.Enabled || snapshot.ImmutableReleases.EnforcedByOwner {
		t.Fatalf("immutable releases=%+v", snapshot.ImmutableReleases)
	}
}

func TestGetActionsPermissionsAcceptsDisabledResponseWithoutAllowedActions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"enabled":false,"sha_pinning_required":false}`))
	}))
	defer server.Close()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)

	permissions, _, err := client.getActionsPermissions(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if permissions.Enabled || permissions.AllowedActions != "" || permissions.SHAPinningRequired {
		t.Fatalf("permissions=%+v", permissions)
	}
}

func TestGetActionsPermissionsRejectsEnabledResponseWithoutAllowedActions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"enabled":true,"sha_pinning_required":false}`))
	}))
	defer server.Close()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)

	if _, _, err := client.getActionsPermissions(context.Background(), ""); err == nil {
		t.Fatal("enabled Actions response without allowed_actions was accepted")
	}
}

func TestGetRepositoryGovernanceRejectsRulesetPagination(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()
	server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/example/repository":
			_, _ = writer.Write([]byte(repositoryJSON(1, "example", "repository", false)))
		case "/repos/example/repository/actions/permissions":
			_, _ = writer.Write([]byte(`{"enabled":true,"allowed_actions":"all"}`))
		case "/repos/example/repository/actions/permissions/workflow":
			_, _ = writer.Write([]byte(`{"default_workflow_permissions":"read"}`))
		case "/repos/example/repository/rulesets":
			writer.Header().Set(
				"Link",
				fmt.Sprintf(
					`<%s/repos/example/repository/rulesets?per_page=100&page=2>; rel="next"`,
					server.URL,
				),
			)
			_, _ = writer.Write([]byte(`[]`))
		default:
			http.NotFound(writer, request)
		}
	})
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)
	_, err := client.GetRepositoryGovernance(context.Background(), "example", "repository")
	if err == nil || !strings.Contains(err.Error(), "100-item bound") {
		t.Fatalf("ruleset pagination error=%v", err)
	}
}

func TestGetRepositoryGovernanceRejectsDuplicateSelectedActionPatterns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/example/repository":
			_, _ = writer.Write([]byte(repositoryJSON(1, "example", "repository", false)))
		case "/repos/example/repository/actions/permissions":
			_, _ = writer.Write([]byte(`{"enabled":true,"allowed_actions":"selected"}`))
		case "/repos/example/repository/actions/permissions/selected-actions":
			_, _ = writer.Write([]byte(`{
  "github_owned_allowed": true,
  "verified_allowed": false,
  "patterns_allowed": ["example/action@*", "example/action@*"]
}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)
	_, err := client.GetRepositoryGovernance(context.Background(), "example", "repository")
	if err == nil {
		t.Fatal("duplicate selected-action pattern was accepted")
	}
}
