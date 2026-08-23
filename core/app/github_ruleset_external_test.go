package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

// GDS owns the required-status-check rule wholesale, so the desired state it
// sends replaces the live list. A context missing from the desired state is a
// context deleted -- and the contexts the platform emits are, by construction,
// never in the tracked file the generator owns. Without the carry step the first
// reconcile after any generated context changes would quietly remove branch
// protection nobody asked to remove, leaving no evidence but their absence.
func writeRuleset(t *testing.T, root string, contexts []string, external string) {
	t.Helper()
	checks := ""
	for index, context := range contexts {
		if index > 0 {
			checks += ","
		}
		checks += `{"context":"` + context + `"}`
	}
	document := `{"name":"Protect main","target":"branch","enforcement":"active","rules":[` +
		`{"type":"required_status_checks","parameters":{"required_status_checks":[` + checks +
		`],"strict_required_status_checks_policy":true}}]}`
	if err := os.MkdirAll(filepath.Join(root, ".github", "rulesets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".github", "rulesets", "branch-main.json"), []byte(document), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if external == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(root, "requirements"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "requirements", "external-required-checks.json"), []byte(external), 0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func desiredContexts(t *testing.T, ruleset githubprovider.RepositoryRuleset) []string {
	t.Helper()
	for _, rule := range ruleset.Rules {
		if rule.Type != "required_status_checks" {
			continue
		}
		contexts := make([]string, 0, len(rule.RequiredStatusChecks))
		for _, check := range rule.RequiredStatusChecks {
			contexts = append(contexts, check.Context)
		}
		return contexts
	}
	t.Fatal("desired ruleset has no required_status_checks rule")
	return nil
}

func TestDesiredRulesetCarriesDeclaredExternalContexts(t *testing.T) {
	root := t.TempDir()
	writeRuleset(t, root, []string{"CI / build"},
		`{"schema_version":1,"contexts":[
			{"context":"Analyze (go)","owner":"github-code-quality","reason":"platform"},
			{"context":"Analyze (python)","owner":"github-code-quality","reason":"platform"}]}`)

	ruleset, err := loadTrackedRuleset(root)
	if err != nil {
		t.Fatal(err)
	}
	contexts := desiredContexts(t, ruleset)
	for _, want := range []string{"CI / build", "Analyze (go)", "Analyze (python)"} {
		if !slices.Contains(contexts, want) {
			t.Errorf("desired state would delete %q from the live ruleset; got %v", want, contexts)
		}
	}
}

func TestMissingRulesetPlansCreationWithoutInventingObservedIdentity(t *testing.T) {
	t.Parallel()
	if expected := rulesetPlanExpected(githubRulesetOperationContext{exists: false}); expected != nil {
		t.Fatalf("missing ruleset produced observed precondition: %#v", expected)
	}
	observed := githubprovider.RepositoryRulesetState{ID: 9, Name: "Protect main", SourceType: "Repository", Source: "example/repository"}
	expected := rulesetPlanExpected(githubRulesetOperationContext{exists: true, observed: observed})
	if expected == nil || expected.ID != 9 {
		t.Fatalf("existing ruleset lost observed identity: %#v", expected)
	}
}

func TestDesiredRulesetIsUnchangedWithoutADeclaration(t *testing.T) {
	root := t.TempDir()
	writeRuleset(t, root, []string{"CI / build"}, "")

	ruleset, err := loadTrackedRuleset(root)
	if err != nil {
		t.Fatal(err)
	}
	if contexts := desiredContexts(t, ruleset); !slices.Equal(contexts, []string{"CI / build"}) {
		t.Errorf("absent declaration must claim nothing, got %v", contexts)
	}
}

func TestRulesetPlannerDetectsOwnedPullRequestDrift(t *testing.T) {
	desired := githubprovider.RepositoryRuleset{Enforcement: "active", Rules: []githubprovider.RulesetRule{
		{
			Type: "pull_request", DismissStaleReviewsOnPush: true,
			RequiredReviewThreadResolution:             true,
			RequireExtraApprovalForUnattributedChanges: false,
			AllowedMergeMethods:                        []string{"merge"},
		},
	}}
	observed := githubprovider.RepositoryRulesetState{Enforcement: "active", Rules: []githubprovider.RulesetRule{
		{
			Type: "pull_request", DismissStaleReviewsOnPush: true,
			RequiredReviewThreadResolution:             true,
			RequireExtraApprovalForUnattributedChanges: true,
			AllowedMergeMethods:                        []string{"merge", "squash", "rebase"},
		},
	}}
	if rulesetOwnedStateMatches(observed, desired) {
		t.Fatal("planner ignored owned pull-request merge-control drift")
	}
	observed.Rules[0].RequireExtraApprovalForUnattributedChanges = false
	observed.Rules[0].AllowedMergeMethods = []string{"merge"}
	if !rulesetOwnedStateMatches(observed, desired) {
		t.Fatal("planner rejected matching owned pull-request merge controls")
	}
}

// A declaration that names a generated context would pin, as unowned, something
// the generator governs -- so the two sources of truth would disagree with no
// way to tell which won.
func TestDeclarationMayNotClaimAGeneratedContext(t *testing.T) {
	root := t.TempDir()
	writeRuleset(t, root, []string{"CI / build"},
		`{"schema_version":1,"contexts":[
			{"context":"CI / build","owner":"github-code-quality","reason":"overlaps"}]}`)

	if _, err := loadTrackedRuleset(root); err == nil {
		t.Fatal("a declaration overlapping a generated context was accepted")
	}
}

func TestDeclarationRequiresBothANameAndAnOwner(t *testing.T) {
	for name, body := range map[string]string{
		"no owner":   `{"schema_version":1,"contexts":[{"context":"Analyze (go)","owner":""}]}`,
		"no context": `{"schema_version":1,"contexts":[{"context":"","owner":"platform"}]}`,
		"malformed":  `{"schema_version":1,"contexts":`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRuleset(t, root, []string{"CI / build"}, body)
			if _, err := loadTrackedRuleset(root); err == nil {
				t.Fatal("an unusable declaration was accepted")
			}
		})
	}
}
