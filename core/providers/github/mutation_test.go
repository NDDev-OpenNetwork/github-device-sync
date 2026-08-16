package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepositoryMutatorRejectsUnboundOperationsBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	mutator := mutationTestMutator(t, server, []string{MutationRepositorySettings}, nil, nil)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositorySettings},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.CreateDraftPullRequest(
		context.Background(), "Title", "Body", "task/change", "main",
	); err == nil {
		t.Fatal("pull-request mutation outside the bound scope was accepted")
	}
	enabled := true
	if _, _, err := repository.UpdateRepositorySettings(
		context.Background(), RepositorySettingsUpdate{AllowAutoMerge: &enabled},
	); err == nil {
		t.Fatal("auto-merge enablement was accepted")
	}
	if requests.Load() != 0 {
		t.Fatalf("rejected mutations made %d provider requests", requests.Load())
	}
}

func TestSetActionsPermissionsOmitsUnmanagedFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body["enabled"] != true {
			t.Errorf("partial Actions payload=%#v", body)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	mutator := mutationTestMutator(
		t, server, []string{MutationRepositorySettings}, nil, nil,
	)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositorySettings},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetActionsPermissions(
		context.Background(), ActionsPermissionsUpdate{Enabled: true},
	); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryMutatorExecutesTypedOperationsWithSpacingAndNoForce(t *testing.T) {
	var requests atomic.Int32
	var waits atomic.Int32
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	oidA := strings.Repeat("a", 40)
	oidB := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		bodyless := request.Method == http.MethodDelete ||
			request.URL.Path == "/repos/example/repository/immutable-releases"
		if request.Header.Get("Authorization") != "Bearer mutation-token" ||
			(!bodyless &&
				request.Header.Get("Content-Type") != "application/json") {
			t.Errorf("headers=%#v", request.Header)
		}
		var body map[string]any
		if !bodyless {
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
		}
		switch request.Method + " " + request.URL.Path {
		case "PATCH /repos/example/repository":
			if _, forbidden := body["visibility"]; forbidden {
				t.Error("ordinary settings mutation included visibility")
			}
			_, _ = writer.Write([]byte(repositoryJSON(42, "example", "repository", false)))
		case "PUT /repos/example/repository/actions/permissions",
			"PUT /repos/example/repository/actions/permissions/selected-actions",
			"PUT /repos/example/repository/actions/permissions/workflow",
			"PUT /repos/example/repository/immutable-releases",
			"DELETE /repos/example/repository/immutable-releases":
			writer.WriteHeader(http.StatusNoContent)
		case "PUT /repos/example/repository/contents/.github/workflows/gds.yml":
			if body["branch"] != "task/gds" {
				t.Errorf("content body=%#v", body)
			}
			_, _ = fmt.Fprintf(writer, `{"content":{"path":".github/workflows/gds.yml","sha":%q},"commit":{"sha":%q}}`, oidA, oidB)
		case "POST /repos/example/repository/git/refs":
			_, _ = fmt.Fprintf(writer, `{"ref":"refs/heads/task/gds","object":{"sha":%q}}`, oidA)
		case "PATCH /repos/example/repository/git/refs/heads/task/gds":
			if force, found := body["force"]; !found || force != false {
				t.Errorf("branch update did not explicitly forbid force: %#v", body)
			}
			_, _ = fmt.Fprintf(writer, `{"ref":"refs/heads/task/gds","object":{"sha":%q}}`, oidB)
		case "POST /repos/example/repository/pulls":
			if body["draft"] != true {
				t.Errorf("pull request was not draft: %#v", body)
			}
			_, _ = fmt.Fprintf(writer, `{"number":7,"id":8,"html_url":"https://github.com/example/repository/pull/7","draft":true,"state":"open","title":"GDS update","body":"Generated change","head":{"ref":"task/gds","sha":%q},"base":{"ref":"main"}}`, oidB)
		case "PATCH /repos/example/repository/properties/values":
			writer.WriteHeader(http.StatusNoContent)
		case "POST /repos/example/repository/rulesets":
			actors, present := body["bypass_actors"].([]any)
			if !present || len(actors) != 0 {
				t.Errorf("ruleset payload did not explicitly clear bypass actors: %#v", body)
			}
			_, _ = writer.Write([]byte(`{"id":9,"name":"gds-main","target":"branch","source_type":"Repository","source":"example/repository","enforcement":"active"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	wait := func(_ context.Context, delay time.Duration) error {
		if delay != time.Second {
			t.Fatalf("delay=%s", delay)
		}
		waits.Add(1)
		now = now.Add(delay)
		return nil
	}
	operations := []string{
		MutationBranch, MutationCustomProperties, MutationPullRequest,
		MutationRepositoryRuleset, MutationRepositorySettings, MutationWorkflowCaller,
	}
	mutator := mutationTestMutator(t, server, operations, func() time.Time { return now }, wait)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository", Operations: operations,
	})
	if err != nil {
		t.Fatal(err)
	}
	allowSquash := true
	if _, _, err := repository.UpdateRepositorySettings(
		context.Background(), RepositorySettingsUpdate{AllowSquashMerge: &allowSquash},
	); err != nil {
		t.Fatal(err)
	}
	allowedActions, shaPinned := "selected", true
	if _, err := repository.SetActionsPermissions(context.Background(), ActionsPermissionsUpdate{
		Enabled: true, AllowedActions: &allowedActions, SHAPinningRequired: &shaPinned,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetSelectedActionsPermissions(
		context.Background(), SelectedActionsPermissions{
			GitHubOwnedAllowed: true,
			PatternsAllowed:    []string{"example-org/ci-workflows/.github/workflows/*@*"},
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetWorkflowPermissions(
		context.Background(), WorkflowPermissionsUpdate{Default: "read"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetImmutableReleases(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetImmutableReleases(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.PutContent(context.Background(), ContentUpdate{
		Path: ".github/workflows/gds.yml", Message: "chore: update workflow",
		Content: []byte("name: gds\n"), Branch: "task/gds", ExpectedSHA: oidA,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.CreateBranch(context.Background(), "task/gds", oidA); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.FastForwardBranch(context.Background(), "task/gds", oidB); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.CreateDraftPullRequest(
		context.Background(), "GDS update", "Generated change", "task/gds", "main",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetCustomProperties(context.Background(), []CustomPropertyValue{
		{Name: "gds_role", Value: "project"}, {Name: "gds_managed", Value: "managed"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.UpsertDefaultBranchRuleset(
		context.Background(), RepositoryRuleset{
			Name: "gds-main", Target: "branch", Enforcement: "active",
			Rules: []RulesetRule{{Type: "non_fast_forward"}},
		}, nil,
	); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 12 || waits.Load() != 11 {
		t.Fatalf("requests=%d waits=%d", requests.Load(), waits.Load())
	}
}

func TestRepositoryMutatorDoesNotRetryTransientProviderFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	mutator := mutationTestMutator(t, server, []string{MutationRepositoryDelete}, nil, nil)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositoryDelete},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.DeleteRepository(context.Background())
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Kind != ErrorTransient || requests.Load() != 1 {
		t.Fatalf("error=%v requests=%d", err, requests.Load())
	}
}

func TestRepositoryRulesetUpdatePreservesExternallyManagedPayload(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.URL.Path != "/repos/example/repository/rulesets/9" {
			http.NotFound(writer, request)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write([]byte(`{"id":9,"name":"gds-main","target":"branch","source_type":"Repository","source":"example/repository","enforcement":"active"}`))
	}))
	defer server.Close()
	mutator := mutationTestMutator(t, server, []string{MutationRepositoryRuleset}, nil, nil)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositoryRuleset},
	})
	if err != nil {
		t.Fatal(err)
	}
	current := RepositoryRulesetState{ID: 9, WritablePayload: json.RawMessage(`{
		"name":"gds-main","target":"branch","enforcement":"evaluate",
		"bypass_actors":[{"actor_id":7,"actor_type":"Team","bypass_mode":"always"}],
		"conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":["refs/heads/vendor/**"]}},
		"rules":[
			{"type":"pull_request","parameters":{"required_approving_review_count":2,"require_last_push_approval":true,"allowed_merge_methods":["squash"]}},
			{"type":"provider_future_rule","parameters":{"opaque":{"keep":true}}},
			{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"old"}],"strict_required_status_checks_policy":false,"do_not_enforce_on_create":true}}
		]}`)}
	desired := RepositoryRuleset{ID: 9, Name: "gds-main", Target: "branch", Enforcement: "active", Rules: []RulesetRule{{
		Type: "required_status_checks", RequiredStatusChecks: []RequiredStatusCheck{{Context: "generated / required"}},
		StrictRequiredStatusChecksPolicy: true,
	}}}
	if _, _, err := repository.UpsertDefaultBranchRuleset(context.Background(), desired, &current); err != nil {
		t.Fatal(err)
	}
	actors := received["bypass_actors"].([]any)
	conditions := received["conditions"].(map[string]any)
	rules := received["rules"].([]any)
	if len(actors) != 1 || len(rules) != 3 || received["enforcement"] != "active" ||
		conditions["ref_name"].(map[string]any)["exclude"].([]any)[0] != "refs/heads/vendor/**" {
		t.Fatalf("externally managed payload was not preserved: %#v", received)
	}
	if rules[0].(map[string]any)["type"] != "pull_request" ||
		rules[1].(map[string]any)["type"] != "provider_future_rule" ||
		rules[2].(map[string]any)["type"] != "required_status_checks" {
		t.Fatalf("rule order/external rules changed: %#v", rules)
	}
}

func mutationTestMutator(
	t *testing.T,
	server *httptest.Server,
	operations []string,
	now func() time.Time,
	wait MutationWait,
) *Mutator {
	t.Helper()
	if now == nil {
		now = func() time.Time { return time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC) }
	}
	permissions := map[string]string{
		"administration": "write", "contents": "write", "custom_properties": "write",
		"metadata": "read", "pull_requests": "write", "workflows": "write",
	}
	tokens := tokenSourceFunc(func(context.Context, string) (InstallationToken, error) {
		return InstallationToken{
			Value: "mutation-token", ExpiresAt: now().Add(time.Hour),
			Permissions: clonePermissions(permissions), RepositorySelection: "selected",
		}, nil
	})
	contract, err := NewPermissionContract(permissions, map[string]string{}, "selected")
	if err != nil {
		t.Fatal(err)
	}
	httpClient := *server.Client()
	httpClient.Timeout = 2 * time.Second
	mutator, err := NewMutator(MutatorConfig{
		Client: Config{
			BaseURL: server.URL + "/", HTTPClient: &httpClient,
			TokenSource: tokens, InstallationID: "mutation:test",
			AllowInsecureLoopback: true, Now: now, PermissionContract: contract,
		},
		Operations: operations, MinimumSpacing: time.Second, Wait: wait,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mutator
}
