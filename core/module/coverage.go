package module

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// A module's anchor says which commands prove it. Nothing said which checks its
// protected branch enforces, and the two drifted in both directions.
//
// `ci-workflows` declared `python3 scripts/validate_all.py` long after that
// invocation stopped working -- a declared gate that does not run, which `gds
// module verify` now catches by running it. The opposite direction is worse and
// executing nothing reaches it: `agent-runtime` enforced `staticcheck` and
// `govulncheck` as required status checks while its anchor named neither, so the
// estate's picture of assurance was weaker than the gate, and a module could
// have quietly lost a required check without any tracked file changing.
//
// Comparing those needs a claim to compare against, which is why the anchor
// states the contexts rather than having them derived. A required context is a
// check run name and not a command; inferring one from the other would produce a
// confident answer with nothing behind it.

// ContextCoverage is what a module claims about its gate and what the provider
// enforces.
type ContextCoverage struct {
	GitmodulesName string   `json:"gitmodules_name"`
	RepositoryID   string   `json:"repository_id,omitempty"`
	Declared       []string `json:"declared"`
	Enforced       []string `json:"enforced"`
	Agreed         []string `json:"agreed"`
}

// CompareRequiredContexts reports where an anchor's claimed gate and the
// enforced one disagree.
//
// Both directions are reported, and they are not the same defect. A context
// enforced but undeclared means the anchor understates the gate: anyone reading
// it as the contract runs a weaker check than the branch requires. A context
// declared but unenforced means the anchor overstates it, which is the more
// dangerous reading, because the estate then records assurance that nothing
// produces.
func CompareRequiredContexts(
	gitmodulesName string,
	repositoryID string,
	declared []string,
	enforced []string,
) (ContextCoverage, []domain.Finding) {
	coverage := ContextCoverage{
		GitmodulesName: gitmodulesName, RepositoryID: repositoryID,
		Declared: normalizeContexts(declared),
		Enforced: normalizeContexts(enforced),
		Agreed:   []string{},
	}
	findings := []domain.Finding{}

	declaredSet := map[string]struct{}{}
	for _, context := range coverage.Declared {
		declaredSet[context] = struct{}{}
	}
	enforcedSet := map[string]struct{}{}
	for _, context := range coverage.Enforced {
		enforcedSet[context] = struct{}{}
	}

	for _, context := range coverage.Enforced {
		if _, claimed := declaredSet[context]; claimed {
			coverage.Agreed = append(coverage.Agreed, context)
			continue
		}
		findings = append(findings, domain.Finding{
			Code: "GDS_MODULE_REQUIRED_CONTEXT_UNDECLARED", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf(
				"Module %q enforces required status check %q and its anchor does not declare it.",
				gitmodulesName, context,
			),
			Evidence: map[string]any{
				"gitmodules_name": gitmodulesName, "repository_id": repositoryID,
				"context": context,
			},
		})
	}
	for _, context := range coverage.Declared {
		if _, enforcedHere := enforcedSet[context]; enforcedHere {
			continue
		}
		findings = append(findings, domain.Finding{
			Code: "GDS_MODULE_REQUIRED_CONTEXT_UNENFORCED", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf(
				"Module %q declares required status check %q and its protected branch does not enforce it.",
				gitmodulesName, context,
			),
			Evidence: map[string]any{
				"gitmodules_name": gitmodulesName, "repository_id": repositoryID,
				"context": context,
			},
		})
	}

	// An anchor that declares nothing while the branch enforces something is
	// already covered above, context by context. The remaining case -- neither
	// side names anything -- is reported once, because a module with no required
	// check at all is a fact worth stating rather than an empty agreement.
	if len(coverage.Declared) == 0 && len(coverage.Enforced) == 0 {
		findings = append(findings, domain.Finding{
			Code: "GDS_MODULE_REQUIRED_CONTEXT_ABSENT", Severity: domain.SeverityMedium,
			Message: fmt.Sprintf(
				"Module %q declares no required status check and its protected branch enforces none.",
				gitmodulesName,
			),
			Evidence: map[string]any{
				"gitmodules_name": gitmodulesName, "repository_id": repositoryID,
			},
		})
	}
	return coverage, findings
}

// normalizeContexts sorts and de-duplicates while preserving the provider's
// exact bytes.
//
// Case is significant: GitHub matches a required status check context exactly,
// so `Test` and `test` are different gates and folding them here would report
// agreement that the provider does not honour.
func normalizeContexts(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	sort.Strings(result)
	return result
}
