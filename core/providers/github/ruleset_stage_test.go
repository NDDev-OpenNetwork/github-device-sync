package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func stagedRulesetFailure(t *testing.T, err error) *RulesetStageError {
	t.Helper()
	if err == nil {
		t.Fatal("expected a staged ruleset failure, got success")
	}
	var staged *RulesetStageError
	if !errors.As(err, &staged) {
		t.Fatalf("error does not carry a ruleset stage: %v", err)
	}
	return staged
}

func rulesetStageTestRepository(t *testing.T, server *httptest.Server) *RepositoryMutator {
	t.Helper()
	mutator := mutationTestMutator(t, server, []string{MutationRepositoryRuleset}, nil, nil)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositoryRuleset},
	})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func ownedRulesetRule() RulesetRule {
	return RulesetRule{
		Type:                 "required_status_checks",
		RequiredStatusChecks: []RequiredStatusCheck{{Context: "GDS fast / go (1.26.5)"}},
	}
}

// TestEachRulesetRefusalNamesItsOwnStage is the regression that keeps the seven
// guards distinguishable. Before staging, every one of these cases returned a
// bare error and the operation journal recorded no stage at all, so a live
// reconcile that refused locally looked identical to one that never ran.
func TestEachRulesetRefusalNamesItsOwnStage(t *testing.T) {
	fixture := liveRulesetFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()
	repository := rulesetStageTestRepository(t, server)
	observed, _, err := repository.GetRepositoryRuleset(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}

	valid := RepositoryRuleset{
		ID: observed.ID, Name: observed.Name, Target: "branch",
		Enforcement: "active", Rules: []RulesetRule{ownedRulesetRule()},
	}

	unsupported := valid
	unsupported.Rules = []RulesetRule{{Type: "creation"}}

	missingIdentity := valid
	missingIdentity.ID = 0

	mismatchedIdentity := valid
	mismatchedIdentity.ID = observed.ID + 1

	notLossless := observed
	notLossless.WritablePayload = nil

	rulesNotAList := observed
	rulesNotAList.WritablePayload = json.RawMessage(`{"rules":"not-a-list"}`)

	for _, testCase := range []struct {
		name    string
		desired RepositoryRuleset
		current RepositoryRulesetState
		stage   RulesetStage
		reason  string
		field   string
	}{
		{
			name: "unsupported rule type", desired: unsupported, current: observed,
			stage: RulesetStageContractValidation, reason: "rule-type-unsupported", field: "creation",
		},
		{
			name: "desired identity missing", desired: missingIdentity, current: observed,
			stage: RulesetStageObservationBinding, reason: "desired-identity-missing",
		},
		{
			name: "observed identity differs", desired: mismatchedIdentity, current: observed,
			stage: RulesetStageObservationBinding, reason: "observed-identity-differs",
		},
		{
			name: "observation not lossless", desired: valid, current: notLossless,
			stage: RulesetStageObservationBinding, reason: "observation-not-lossless",
		},
		{
			name: "preserved rules are not a list", desired: valid, current: rulesNotAList,
			stage: RulesetStageExternalFieldMerge, reason: "preserved-rules-not-a-list", field: "rules",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			current := testCase.current
			_, _, err := repository.UpsertDefaultBranchRuleset(context.Background(), testCase.desired, &current)
			staged := stagedRulesetFailure(t, err)
			if staged.Stage != testCase.stage || staged.Reason != testCase.reason {
				t.Fatalf("stage/reason = %s/%s, want %s/%s",
					staged.Stage, staged.Reason, testCase.stage, testCase.reason)
			}
			if testCase.field != "" && staged.Field != testCase.field {
				t.Fatalf("field = %q, want %q", staged.Field, testCase.field)
			}
		})
	}
}

// TestPostconditionNamesTheFieldTheProviderContradicted proves the seven-way
// disjunction is gone: a write the provider accepted but answered with the
// wrong enforcement no longer reports as an unreadable body.
func TestPostconditionNamesTheFieldTheProviderContradicted(t *testing.T) {
	fixture := liveRulesetFixture(t)
	var document map[string]any
	if err := json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			answer := map[string]any{
				"id": document["id"], "name": document["name"], "target": "branch",
				"source_type": "Repository", "source": "example/repository",
				"enforcement": "evaluate",
			}
			_ = json.NewEncoder(writer).Encode(answer)
			return
		}
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()
	repository := rulesetStageTestRepository(t, server)
	observed, _, err := repository.GetRepositoryRuleset(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}
	desired := RepositoryRuleset{
		ID: observed.ID, Name: observed.Name, Target: "branch",
		Enforcement: "active", Rules: []RulesetRule{ownedRulesetRule()},
	}
	_, _, err = repository.UpsertDefaultBranchRuleset(context.Background(), desired, &observed)
	staged := stagedRulesetFailure(t, err)
	if staged.Stage != RulesetStagePostcondition || staged.Reason != "enforcement-differs" ||
		staged.Field != "enforcement" {
		t.Fatalf("stage/reason/field = %s/%s/%s, want postcondition/enforcement-differs/enforcement",
			staged.Stage, staged.Reason, staged.Field)
	}
}

// TestStagedRulesetEvidenceCarriesNoPayload keeps the journal safe. The stage
// and reason are closed sets; the wrapped cause is deliberately excluded
// because a decode error can quote the payload it failed to read.
func TestStagedRulesetEvidenceCarriesNoPayload(t *testing.T) {
	// A distinctive marker, deliberately not shaped like a real token:
	// the public-artifact gate rejects tracked text matching a credential
	// pattern, and a test proving evidence stays clean must not itself carry one.
	secret := "SENTINEL-must-not-appear-in-journal-evidence"
	failure := &RulesetStageError{
		Stage:  RulesetStageDesiredDecode,
		Reason: "preserved-payload-undecodable",
		Cause:  errors.New("invalid character in " + secret),
	}
	evidence := failure.SafeOperationFailureEvidence()
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("staged evidence leaked the wrapped cause: %s", encoded)
	}
	if evidence["stage"] != string(RulesetStageDesiredDecode) ||
		evidence["reason"] != "preserved-payload-undecodable" {
		t.Fatalf("staged evidence lost its identity: %v", evidence)
	}
	if failure.SafeProviderFailureCode() != "desired_decode/preserved-payload-undecodable" {
		t.Fatalf("unexpected failure code: %s", failure.SafeProviderFailureCode())
	}
}
