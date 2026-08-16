package github

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetRepositoryRulesetNormalizesVisibleStateAndMarksHiddenBypass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("includes_parents") != "false" {
			t.Errorf("query=%s", request.URL.RawQuery)
		}
		_, _ = writer.Write([]byte(rulesetStateJSON(false)))
	}))
	defer server.Close()
	client := testClient(t, server, fixedToken("token", time.Now().Add(time.Hour)), nil)
	state, _, err := client.GetRepositoryRuleset(context.Background(), "example", "repository", 9)
	if err != nil || state.BypassActorsKnown || len(state.Rules) != 2 ||
		state.Rules[0].Type != "non_fast_forward" || state.ConditionIncludes[0] != "~DEFAULT_BRANCH" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if len(state.WritablePayload) != 0 {
		t.Fatal("unprivileged observation claimed a lossless writable payload")
	}
}

func TestRepositoryMutatorRequiresVisibleRulesetBypassActors(t *testing.T) {
	visible := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(rulesetStateJSON(visible)))
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
	if _, _, err := repository.GetRepositoryRuleset(context.Background(), 9); err == nil {
		t.Fatal("mutation reader accepted a response with hidden bypass actors")
	}
	visible = true
	state, _, err := repository.GetRepositoryRuleset(context.Background(), 9)
	if err != nil || !state.BypassActorsKnown || len(state.BypassActors) != 0 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if !bytes.Contains(state.WritablePayload, []byte(`"future_condition"`)) || !bytes.Contains(state.WritablePayload, []byte(`"provider_rule_metadata"`)) {
		t.Fatalf("lossless writable payload dropped provider fields: %s", state.WritablePayload)
	}
}

func rulesetStateJSON(includeBypass bool) string {
	bypass := ""
	if includeBypass {
		bypass = `,"bypass_actors":[]`
	}
	return fmt.Sprintf(`{"id":9,"name":"gds-default-branch","target":"branch","source_type":"Repository","source":"example/repository","enforcement":"active"%s,"conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]},"future_condition":{"preserve":true}},"rules":[{"type":"non_fast_forward"},{"type":"pull_request","provider_rule_metadata":{"preserve":true},"parameters":{"required_approving_review_count":1,"dismiss_stale_reviews_on_push":true,"require_code_owner_review":true,"required_review_thread_resolution":true,"require_last_push_approval":false}}]}`, bypass)
}
