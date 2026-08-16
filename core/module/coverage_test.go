package module

import (
	"testing"
)

func coverageCodes(findings []string) map[string]int {
	counted := map[string]int{}
	for _, code := range findings {
		counted[code]++
	}
	return counted
}

// The direction that motivated this: `agent-runtime` enforced `staticcheck` and
// `govulncheck` as required status checks while its anchor named neither, so
// anyone reading the anchor as the contract ran a weaker check than the branch
// required.
func TestAnEnforcedContextTheAnchorOmitsIsReported(t *testing.T) {
	t.Parallel()
	_, findings := CompareRequiredContexts(
		"agent-runtime", "repo_01JEXAMPZ0000000000000000K",
		[]string{"test"}, []string{"test", "govulncheck", "staticcheck"},
	)
	if len(findings) != 2 {
		t.Fatalf("findings = %#v", findings)
	}
	for _, finding := range findings {
		if finding.Code != "GDS_MODULE_REQUIRED_CONTEXT_UNDECLARED" {
			t.Fatalf("finding = %#v", finding)
		}
	}
}

// The opposite direction is the more dangerous reading: the estate records a
// gate that nothing asks for.
func TestADeclaredContextTheBranchDoesNotEnforceIsReported(t *testing.T) {
	t.Parallel()
	_, findings := CompareRequiredContexts(
		"example", "repo_01JEXAMPZ0000000000000000K",
		[]string{"test", "govulncheck"}, []string{"test"},
	)
	if len(findings) != 1 ||
		findings[0].Code != "GDS_MODULE_REQUIRED_CONTEXT_UNENFORCED" ||
		findings[0].Evidence["context"] != "govulncheck" {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestAnAgreedGateReportsNothing(t *testing.T) {
	t.Parallel()
	coverage, findings := CompareRequiredContexts(
		"ci-workflows", "repo_01JEXAMPZ0000000000000000K",
		[]string{"ci-gate"}, []string{"ci-gate"},
	)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if len(coverage.Agreed) != 1 || coverage.Agreed[0] != "ci-gate" {
		t.Fatalf("coverage = %#v", coverage)
	}
}

// GitHub matches a required status check context exactly. Folding case here
// would report agreement the provider does not honour, so a module whose anchor
// says `Test` while the branch requires `test` must be told both are wrong.
func TestContextComparisonIsCaseExact(t *testing.T) {
	t.Parallel()
	_, findings := CompareRequiredContexts(
		"example", "repo_01JEXAMPZ0000000000000000K",
		[]string{"Test"}, []string{"test"},
	)
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	counted := coverageCodes(codes)
	if counted["GDS_MODULE_REQUIRED_CONTEXT_UNDECLARED"] != 1 ||
		counted["GDS_MODULE_REQUIRED_CONTEXT_UNENFORCED"] != 1 {
		t.Fatalf("codes = %#v", codes)
	}
}

// A module with no required check on either side is not an agreement. Silence
// there would read as "the gate matches" for a module that has no gate.
func TestAModuleWithNoGateOnEitherSideIsStated(t *testing.T) {
	t.Parallel()
	_, findings := CompareRequiredContexts("example", "", nil, nil)
	if len(findings) != 1 || findings[0].Code != "GDS_MODULE_REQUIRED_CONTEXT_ABSENT" {
		t.Fatalf("findings = %#v", findings)
	}
}

// A context repeated by two rulesets is one gate, and reporting it twice would
// make an agreeing module look like a drifting one.
func TestARepeatedContextIsCountedOnce(t *testing.T) {
	t.Parallel()
	coverage, findings := CompareRequiredContexts(
		"example", "", []string{"ci-gate"}, []string{"ci-gate", "ci-gate"},
	)
	if len(findings) != 0 || len(coverage.Enforced) != 1 {
		t.Fatalf("coverage = %#v findings = %#v", coverage, findings)
	}
}
