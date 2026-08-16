package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Shaped after a real response from this estate: an organization ruleset
// contributes parameterless rules, a repository ruleset contributes the
// required status checks, and both arrive from one call already matched to the
// branch. Reconstructing that from ruleset documents was tried and was wrong --
// an organization ruleset id is a 404 on the repository endpoint.
const branchRulesFixture = `[
  {"type":"deletion","ruleset_id":20161162,"ruleset_source":"example-org","ruleset_source_type":"Organization"},
  {"type":"non_fast_forward","ruleset_id":20161162,"ruleset_source":"example-org","ruleset_source_type":"Organization"},
  {"type":"required_signatures","ruleset_id":20161162,"ruleset_source":"example-org","ruleset_source_type":"Organization"},
  {"type":"required_status_checks","ruleset_id":18506136,
   "ruleset_source":"example-org/ci-workflows","ruleset_source_type":"Repository",
   "parameters":{"strict_required_status_checks_policy":true,
     "required_status_checks":[{"context":"ci-gate","integration_id":15368}]}}
]`

func branchRulesServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			if !strings.HasSuffix(request.URL.Path, "/rules/branches/main") {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(body))
		}))
	t.Cleanup(server.Close)
	return server
}

func TestBranchRulesCarryTheirRulesetIdentity(t *testing.T) {
	t.Parallel()
	server := branchRulesServer(t, branchRulesFixture, http.StatusOK)
	client := testClient(t, server, fixedToken("t", time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC)), nil)

	rules, _, err := client.ListBranchRules(
		context.Background(), "example-org", "ci-workflows", "main",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 {
		t.Fatalf("rules = %#v", rules)
	}
	// The ruleset id is what lets the caller drop rules from a ruleset that
	// reports without blocking. Without it every `evaluate` ruleset would be
	// counted as a gate.
	var checks *BranchRule
	for index := range rules {
		if rules[index].Type == "required_status_checks" {
			checks = &rules[index]
		}
	}
	if checks == nil || checks.RulesetID != 18506136 ||
		checks.RulesetSourceKind != "Repository" {
		t.Fatalf("required_status_checks rule = %#v", checks)
	}
	if len(checks.Parameters.RequiredStatusChecks) != 1 ||
		checks.Parameters.RequiredStatusChecks[0].Context != "ci-gate" {
		t.Fatalf("contexts = %#v", checks.Parameters.RequiredStatusChecks)
	}
}

// A branch with no rule is a real answer, and it is the one `github-actions`
// gives: an empty gate must not be mistaken for a failed read.
func TestAnUngatedBranchReadsAsEmptyRatherThanFailing(t *testing.T) {
	t.Parallel()
	server := branchRulesServer(t, `[]`, http.StatusOK)
	client := testClient(t, server, fixedToken("t", time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC)), nil)
	rules, _, err := client.ListBranchRules(
		context.Background(), "example-org", "github-actions", "main",
	)
	if err != nil || len(rules) != 0 {
		t.Fatalf("rules = %#v err = %v", rules, err)
	}
}

// A rule without the ruleset it came from cannot be paired with enforcement, so
// accepting it would silently count a reporting-only ruleset as a gate.
func TestARuleWithoutItsRulesetIsRefused(t *testing.T) {
	t.Parallel()
	server := branchRulesServer(t, `[{"type":"required_status_checks"}]`, http.StatusOK)
	client := testClient(t, server, fixedToken("t", time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC)), nil)
	if _, _, err := client.ListBranchRules(
		context.Background(), "example-org", "ci-workflows", "main",
	); err == nil {
		t.Fatal("a rule with no ruleset identity was accepted")
	}
}

func TestABranchNameThatIsNotOneIsRefusedBeforeTheRequest(t *testing.T) {
	t.Parallel()
	server := branchRulesServer(t, branchRulesFixture, http.StatusOK)
	client := testClient(t, server, fixedToken("t", time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC)), nil)
	for _, branch := range []string{"", "..", "main branch", "-main"} {
		if _, _, err := client.ListBranchRules(
			context.Background(), "example-org", "ci-workflows", branch,
		); err == nil {
			t.Fatalf("branch %q was accepted", branch)
		}
	}
}
