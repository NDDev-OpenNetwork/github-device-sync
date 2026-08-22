package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// liveRulesetFixture is the exact shape the GitHub API returns for the tracked
// default-branch ruleset, sanitized only for repository identity. It exists so a
// provider-side schema addition is caught by a failing test here instead of by a
// governance run that can no longer read live state.
func liveRulesetFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "live-branch-ruleset.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCurrentLiveRulesetShapeIsObservable(t *testing.T) {
	fixture := liveRulesetFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(fixture)
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
	state, _, err := repository.GetRepositoryRuleset(context.Background(), 9)
	if err != nil {
		t.Fatalf("current live ruleset shape is not observable: %v", err)
	}

	var pullRequest *RulesetRule
	for index := range state.Rules {
		if state.Rules[index].Type == "pull_request" {
			pullRequest = &state.Rules[index]
		}
	}
	if pullRequest == nil {
		t.Fatal("observation dropped the pull_request rule")
	}
	// Owned fields stay typed and exact.
	if pullRequest.RequiredApprovingReviewCount != 0 || !pullRequest.DismissStaleReviewsOnPush ||
		pullRequest.RequireCodeOwnerReview || !pullRequest.RequiredReviewThreadResolution ||
		pullRequest.RequireLastPushApproval || pullRequest.RequireExtraApprovalForUnattributedChanges ||
		!reflect.DeepEqual(pullRequest.AllowedMergeMethods, []string{"merge"}) {
		t.Fatalf("owned pull_request fields were not typed exactly: %#v", pullRequest)
	}
	// Externally managed fields are preserved rather than rejected or dropped.
	var external map[string]json.RawMessage
	if err := json.Unmarshal(pullRequest.ExternalParameters, &external); err != nil {
		t.Fatalf("external pull_request parameters were not preserved: %v", err)
	}
	for _, key := range []string{"required_reviewers", "dismissal_restriction"} {
		if _, present := external[key]; !present {
			t.Errorf("external pull_request parameter %q was dropped", key)
		}
	}
	if _, leaked := external["required_approving_review_count"]; leaked {
		t.Error("an owned field was also recorded as external")
	}
	for _, key := range []string{"require_last_push_approval", "require_extra_approval_for_unattributed_changes", "allowed_merge_methods"} {
		if _, leaked := external[key]; leaked {
			t.Errorf("owned pull_request parameter %q was also recorded as external", key)
		}
	}
}

func TestPullRequestOwnedParametersRoundTripExactly(t *testing.T) {
	rule, err := normalizeRulesetRule("pull_request", json.RawMessage(`{
		"required_approving_review_count":0,
		"dismiss_stale_reviews_on_push":true,
		"require_code_owner_review":false,
		"required_review_thread_resolution":true,
		"require_last_push_approval":false,
		"require_extra_approval_for_unattributed_changes":true,
		"allowed_merge_methods":["merge"],
		"required_reviewers":[]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !rule.RequireExtraApprovalForUnattributedChanges ||
		!reflect.DeepEqual(rule.AllowedMergeMethods, []string{"merge"}) {
		t.Fatalf("owned pull-request parameters were not decoded: %#v", rule)
	}
	payload := rulesetRulePayload(RulesetRule{
		Type: "pull_request", DismissStaleReviewsOnPush: true,
		RequiredReviewThreadResolution:             true,
		RequireExtraApprovalForUnattributedChanges: false,
		AllowedMergeMethods:                        []string{"merge"},
	})
	parameters := payload["parameters"].(map[string]any)
	if parameters["require_extra_approval_for_unattributed_changes"] != false ||
		!reflect.DeepEqual(parameters["allowed_merge_methods"], []string{"merge"}) {
		t.Fatalf("owned pull-request parameters were not encoded: %#v", parameters)
	}
}

func TestOwnedRulesetUpdatePreservesEveryExternalFieldByteForByte(t *testing.T) {
	fixture := liveRulesetFixture(t)
	var submitted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			if err := json.NewDecoder(request.Body).Decode(&submitted); err != nil {
				t.Errorf("decode update payload: %v", err)
			}
			_, _ = writer.Write(fixture)
			return
		}
		_, _ = writer.Write(fixture)
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
	current, _, err := repository.GetRepositoryRuleset(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}

	// Change only the rule GDS owns; every other rule must survive untouched.
	desired := RepositoryRuleset{
		ID: 9, Name: current.Name, Target: "branch", Enforcement: "active",
		Rules: []RulesetRule{{
			Type:                             "required_status_checks",
			StrictRequiredStatusChecksPolicy: true,
			RequiredStatusChecks:             []RequiredStatusCheck{{Context: "CodeQL / CodeQL (go)"}},
		}},
	}
	if _, _, err := repository.UpsertDefaultBranchRuleset(context.Background(), desired, &current); err != nil {
		t.Fatalf("owned update failed: %v", err)
	}
	if submitted == nil {
		t.Fatal("no update payload was submitted")
	}

	var fixtureDocument struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(fixture, &fixtureDocument); err != nil {
		t.Fatal(err)
	}
	expectedByType := map[string]map[string]any{}
	for _, rule := range fixtureDocument.Rules {
		expectedByType[rule["type"].(string)] = rule
	}
	submittedRules, ok := submitted["rules"].([]any)
	if !ok {
		t.Fatalf("update payload lost its rules: %#v", submitted)
	}
	seen := map[string]bool{}
	for _, value := range submittedRules {
		rule, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("submitted rule is not an object: %#v", value)
		}
		ruleType, _ := rule["type"].(string)
		seen[ruleType] = true
		if ruleType == "required_status_checks" {
			continue // the owned rule is intentionally replaced
		}
		if !reflect.DeepEqual(rule, expectedByType[ruleType]) {
			t.Errorf("externally managed rule %q was rewritten\n got: %#v\nwant: %#v",
				ruleType, rule, expectedByType[ruleType])
		}
	}
	for ruleType := range expectedByType {
		if !seen[ruleType] {
			t.Errorf("update payload dropped rule %q", ruleType)
		}
	}
	// Bypass actors and conditions are external state and must be carried through.
	if !reflect.DeepEqual(submitted["bypass_actors"], []any{map[string]any{
		"actor_id": float64(5), "actor_type": "RepositoryRole", "bypass_mode": "always",
	}}) {
		t.Errorf("bypass actors were not preserved: %#v", submitted["bypass_actors"])
	}
}

func TestObservationIsStableAcrossRepeatedReads(t *testing.T) {
	fixture := liveRulesetFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()
	client := testClient(t, server, fixedToken("token", time.Now().Add(time.Hour)), nil)
	first, _, err := client.GetRepositoryRuleset(context.Background(), "example", "repository", 9)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := client.GetRepositoryRuleset(context.Background(), "example", "repository", 9)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated observation of the same payload is not stable")
	}
}
