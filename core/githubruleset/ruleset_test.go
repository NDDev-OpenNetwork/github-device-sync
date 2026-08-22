package githubruleset

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type rulesetFixture struct {
	scope  githubprovider.RepositoryMutationScope
	state  *githubprovider.RepositoryRulesetState
	writes int
}

func TestRulesetHandlerCreatesAndVerifiesClosedDefaultBranchRuleset(t *testing.T) {
	fixture := newRulesetFixture()
	step := rulesetStep(nil, desiredRuleset(0))
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	plan, err := operations.NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGR", now, now.Add(15*time.Minute),
		operations.PlanInput{
			Operation: "reconcile-github-ruleset",
			Actor:     operations.Actor{Type: "agent-session", SessionID: "test-session"},
			Preconditions: []operations.Precondition{{
				RepositoryID: step.RepositoryID, HeadOID: strings.Repeat("a", 40),
				ManifestDigest: "sha256:" + strings.Repeat("a", 64),
				PolicyDigest:   "sha256:" + strings.Repeat("b", 64),
			}},
			Steps: []operations.Step{step}, ApprovalClass: "github-ruleset-write",
		},
	)
	if err != nil || len(plan.Validate(schemas)) != 0 {
		t.Fatalf("plan err=%v findings=%#v", err, plan.Validate(schemas))
	}
	handler := &Handler{Reader: fixture, Writer: fixture}
	result, err := handler.Apply(context.Background(), step)
	if err != nil || fixture.writes != 1 || fixture.state == nil || len(fixture.state.BypassActors) != 0 {
		t.Fatalf("result=%#v err=%v fixture=%#v", result, err, fixture)
	}
	raw, _ := json.Marshal(result.After)
	verify := &Handler{Reader: fixture, Scope: fixture.scope}
	if err := verify.Verify(context.Background(), step, raw); err != nil {
		t.Fatal(err)
	}
}

func TestRulesetHandlerPreservesNonEmptyBypassAndExternalRules(t *testing.T) {
	fixture := newRulesetFixture()
	actorID := int64(7)
	state := desiredState(Scope{Owner: "example", Name: "repository"}, desiredRuleset(9), 9)
	state.BypassActors = []githubprovider.RulesetBypassActor{{
		ActorID: &actorID, ActorType: "Team", BypassMode: "always",
	}}
	state.Enforcement = "evaluate"
	state.Rules = append(state.Rules,
		githubprovider.RulesetRule{Type: "non_fast_forward"},
		githubprovider.RulesetRule{Type: "pull_request", RequiredApprovingReviewCount: 1},
	)
	state.WritablePayload = json.RawMessage(`{"name":"gds-default-branch","target":"branch","enforcement":"evaluate","bypass_actors":[{"actor_id":7,"actor_type":"Team","bypass_mode":"always"}],"conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"rules":[{"type":"non_fast_forward"},{"type":"pull_request","parameters":{"required_approving_review_count":1}}]}`)
	fixture.state = &state
	expected := visibleState(state)
	step := rulesetStep(&expected, desiredRuleset(9))
	result, err := (&Handler{Reader: fixture, Writer: fixture}).Apply(context.Background(), step)
	if err != nil || fixture.writes != 1 || len(fixture.state.BypassActors) != 1 || requiredChecks(fixture.state.Rules) == nil {
		t.Fatalf("err=%v writes=%d", err, fixture.writes)
	}
	raw, _ := json.Marshal(result.After)
	if err := (&Handler{Reader: fixture, Scope: fixture.scope}).Verify(context.Background(), step, raw); err != nil {
		t.Fatalf("verify preserved external state: %v", err)
	}
}

func newRulesetFixture() *rulesetFixture {
	return &rulesetFixture{scope: githubprovider.RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{githubprovider.MutationRepositoryRuleset},
	}}
}

func (fixture *rulesetFixture) Scope() githubprovider.RepositoryMutationScope { return fixture.scope }

func (fixture *rulesetFixture) ListRepositoryRulesets(
	context.Context,
) ([]githubprovider.RulesetSummary, githubprovider.ResponseMeta, error) {
	if fixture.state == nil {
		return []githubprovider.RulesetSummary{}, githubprovider.ResponseMeta{}, nil
	}
	return []githubprovider.RulesetSummary{{
		ID: fixture.state.ID, Name: fixture.state.Name, Target: fixture.state.Target,
		SourceType: fixture.state.SourceType, Source: fixture.state.Source,
		Enforcement: fixture.state.Enforcement,
	}}, githubprovider.ResponseMeta{}, nil
}

func (fixture *rulesetFixture) GetRepositoryRuleset(
	_ context.Context, id int64,
) (githubprovider.RepositoryRulesetState, githubprovider.ResponseMeta, error) {
	if fixture.state == nil || fixture.state.ID != id {
		return githubprovider.RepositoryRulesetState{}, githubprovider.ResponseMeta{},
			&githubprovider.APIError{Kind: githubprovider.ErrorNotFoundOrInaccessible, StatusCode: 404}
	}
	return *fixture.state, githubprovider.ResponseMeta{}, nil
}

func (fixture *rulesetFixture) UpsertDefaultBranchRuleset(
	_ context.Context, desired githubprovider.RepositoryRuleset, current *githubprovider.RepositoryRulesetState,
) (githubprovider.RulesetSummary, githubprovider.MutationMeta, error) {
	id := desired.ID
	if id == 0 {
		id = 9
	}
	state := desiredState(Scope{Owner: fixture.scope.Owner, Name: fixture.scope.Name}, desired, id)
	if current != nil {
		state = applyOwnedState(*current, desired)
	} else {
		state.WritablePayload = json.RawMessage(`{"name":"gds-default-branch","target":"branch","enforcement":"active","bypass_actors":[],"conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"CI / required"}],"strict_required_status_checks_policy":true,"do_not_enforce_on_create":false}}]}`)
	}
	fixture.state = &state
	fixture.writes++
	return githubprovider.RulesetSummary{
		ID: id, Name: desired.Name, Target: "branch", SourceType: "Repository",
		Source: fixture.scope.Owner + "/" + fixture.scope.Name, Enforcement: "active",
	}, githubprovider.MutationMeta{RepositoryID: 42, StatusCode: 201}, nil
}

func rulesetStep(
	expected *githubprovider.RepositoryRulesetState,
	desired githubprovider.RepositoryRuleset,
) operations.Step {
	return operations.Step{
		StepID: "set-default-ruleset", RepositoryID: "repo_01JEXAMPZ0000000000000000C",
		Action: Action, RequiresApproval: true,
		Compensation: operations.Compensation{Mode: "manual"},
		Parameters: OperationParameters(Parameters{
			Scope: Scope{
				ReadInstallationID: "installation:read", MutationCapabilityID: "mutation:write",
				ProviderRepositoryID: 42, Owner: "example", Name: "repository",
			},
			Expected: expected, Desired: desired,
		}),
	}
}

func desiredRuleset(id int64) githubprovider.RepositoryRuleset {
	return githubprovider.RepositoryRuleset{
		ID: id, Name: "gds-default-branch", Target: "branch", Enforcement: "active",
		Rules: []githubprovider.RulesetRule{{Type: "required_status_checks",
			StrictRequiredStatusChecksPolicy: true,
			RequiredStatusChecks:             []githubprovider.RequiredStatusCheck{{Context: "CI / required"}}}},
	}
}

// A stored expectation must be representable in the plan contract, and Apply
// compares against exactly this form. An empty observed list decodes to a nil
// slice, which serializes as null and is rejected where an array is required --
// so the canonical form has to normalize it on both sides.
func TestVisibleStateCanonicalizesEmptyCollections(t *testing.T) {
	visible := VisibleState(githubprovider.RepositoryRulesetState{
		ID: 9, Name: "Protect main", Target: "branch", Enforcement: "active",
		BypassActorsKnown: true,
		BypassActors:      []githubprovider.RulesetBypassActor{{ActorType: "RepositoryRole"}},
		WritablePayload:   []byte(`{"name":"Protect main"}`),
	})
	if visible.ConditionIncludes == nil || visible.ConditionExcludes == nil || visible.Rules == nil {
		t.Fatalf("empty collections stayed nil and would serialize as null: %#v", visible)
	}
	if visible.WritablePayload != nil {
		t.Error("the full writable payload must not survive into the comparable form")
	}
	if visible.WritableDigest == "" {
		t.Error("the digest must stand in for the dropped payload")
	}
	if visible.BypassActorsKnown || len(visible.BypassActors) != 0 {
		t.Error("privileged bypass detail must not survive into the comparable form")
	}
	// Idempotent: applying it to an already-visible state must not change it.
	if again := VisibleState(visible); !reflect.DeepEqual(again, visible) {
		t.Error("VisibleState is not idempotent, so a stored expectation could never match")
	}
}

// Preserved external parameters are raw bytes and DeepEqual compares bytes. A
// plan stores them through Go's encoder, which orders object keys; a fresh
// observation keeps the provider's order. Without canonicalizing, the same state
// compared unequal to itself and every apply reported "state changed after
// planning" without attempting a mutation -- deterministically, forever.
func TestVisibleStateCanonicalizesPreservedRawParameters(t *testing.T) {
	providerOrder := githubprovider.RepositoryRulesetState{
		ID: 9, Name: "Protect main", Target: "branch", Enforcement: "active",
		Rules: []githubprovider.RulesetRule{{
			Type:               "pull_request",
			ExternalParameters: json.RawMessage(`{"enabled":false,"allowed_actors":[]}`),
			OpaqueParameters:   json.RawMessage(`{"b":2,"a":1}`),
		}},
	}
	storedOrder := githubprovider.RepositoryRulesetState{
		ID: 9, Name: "Protect main", Target: "branch", Enforcement: "active",
		Rules: []githubprovider.RulesetRule{{
			Type:               "pull_request",
			ExternalParameters: json.RawMessage(`{"allowed_actors":[],"enabled":false}`),
			OpaqueParameters:   json.RawMessage(`{"a":1,"b":2}`),
		}},
	}
	if !reflect.DeepEqual(VisibleState(providerOrder), VisibleState(storedOrder)) {
		t.Fatal("key order in preserved raw parameters still decides equality")
	}
	// Content must still decide it: a real difference has to survive.
	changed := providerOrder
	changed.Rules = []githubprovider.RulesetRule{{
		Type:               "pull_request",
		ExternalParameters: json.RawMessage(`{"enabled":true,"allowed_actors":[]}`),
		OpaqueParameters:   json.RawMessage(`{"b":2,"a":1}`),
	}}
	if reflect.DeepEqual(VisibleState(providerOrder), VisibleState(changed)) {
		t.Fatal("a genuine external-parameter change was normalized away")
	}
	// Unparseable bytes are preserved rather than dropped: the raw field exists
	// to carry external state through untouched.
	broken := providerOrder
	broken.Rules = []githubprovider.RulesetRule{{Type: "x", ExternalParameters: json.RawMessage(`{not json`)}}
	if got := VisibleState(broken).Rules[0].ExternalParameters; string(got) != `{not json` {
		t.Fatalf("unparseable external parameters were altered: %s", got)
	}
}

func TestApplyOwnedStatePreservesPullRequestExternalParameters(t *testing.T) {
	external := json.RawMessage(`{"required_reviewers":[],"dismissal_restriction":{"enabled":false}}`)
	current := githubprovider.RepositoryRulesetState{Enforcement: "active", Rules: []githubprovider.RulesetRule{{
		Type: "pull_request", RequireExtraApprovalForUnattributedChanges: true,
		AllowedMergeMethods: []string{"merge", "squash", "rebase"}, ExternalParameters: external,
	}}}
	desired := githubprovider.RepositoryRuleset{Enforcement: "active", Rules: []githubprovider.RulesetRule{{
		Type: "pull_request", AllowedMergeMethods: []string{"merge"},
	}}}
	updated := applyOwnedState(current, desired)
	if len(updated.Rules) != 1 || updated.Rules[0].RequireExtraApprovalForUnattributedChanges ||
		!reflect.DeepEqual(updated.Rules[0].AllowedMergeMethods, []string{"merge"}) ||
		!reflect.DeepEqual(updated.Rules[0].ExternalParameters, external) {
		t.Fatalf("owned pull update lost external parameters or desired controls: %#v", updated.Rules)
	}
}
