package validation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// A repository's anchor states the required status check contexts its protected
// branch enforces, and `.github/rulesets/*.json` is the tracked document that
// asks the provider for them. Two declarations of one gate drift the moment a
// check is renamed, added or dropped in one of them.
//
// Reconciling the tracked ruleset with the provider is already somebody's job
// (`scripts/report_ruleset_drift.py`, `gds github ruleset`). This closes the
// other half locally: if the anchor agrees with the tracked ruleset and the
// tracked ruleset agrees with the provider, the anchor agrees with the provider,
// and no network call is needed to know it.
//
// A repository that tracks no ruleset is not checked here. Half the modules in
// this estate track none, and their gate is only knowable from the provider --
// which is what `gds module coverage` is for.

type rulesetDocument struct {
	Rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	} `json:"rules"`
	Conditions struct {
		RefName struct {
			Include []string `json:"include"`
		} `json:"ref_name"`
	} `json:"conditions"`
}

// RequiredContextFindings compares an anchor's declared required contexts with
// the tracked default-branch ruleset.
//
// Both directions are reported. A context in the ruleset but not the anchor
// means the anchor understates the gate, so anyone reading it as the contract
// expects a weaker check than the branch requires. A context in the anchor but
// not the ruleset means it overstates it, and the estate records assurance that
// nothing asks for.
func RequiredContextFindings(root string, declared []string) []domain.Finding {
	if len(declared) == 0 {
		return nil
	}
	directory := filepath.Join(root, ".github", "rulesets")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil // no tracked ruleset: the provider is the only witness
	}
	tracked := map[string]struct{}{}
	found := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			continue
		}
		var document rulesetDocument
		if json.Unmarshal(raw, &document) != nil {
			continue
		}
		if !includesDefaultBranch(document.Conditions.RefName.Include) {
			continue
		}
		found = true
		for _, rule := range document.Rules {
			if rule.Type != "required_status_checks" {
				continue
			}
			for _, check := range rule.Parameters.RequiredStatusChecks {
				tracked[check.Context] = struct{}{}
			}
		}
	}
	if !found {
		return nil
	}

	findings := []domain.Finding{}
	declaredSet := map[string]struct{}{}
	for _, context := range declared {
		declaredSet[context] = struct{}{}
	}
	missing := []string{}
	for context := range tracked {
		if _, claimed := declaredSet[context]; !claimed {
			missing = append(missing, context)
		}
	}
	extra := []string{}
	for context := range declaredSet {
		if _, enforced := tracked[context]; !enforced {
			extra = append(extra, context)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, context := range missing {
		findings = append(findings, domain.Finding{
			Code: "GDS_REPOSITORY_REQUIRED_CONTEXT_UNDECLARED", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf(
				"The tracked default-branch ruleset requires status check %q and the anchor does not declare it.",
				context,
			),
			Evidence: map[string]any{"context": context},
		})
	}
	for _, context := range extra {
		findings = append(findings, domain.Finding{
			Code: "GDS_REPOSITORY_REQUIRED_CONTEXT_UNENFORCED", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf(
				"The anchor declares required status check %q and the tracked default-branch ruleset does not ask for it.",
				context,
			),
			Evidence: map[string]any{"context": context},
		})
	}
	return findings
}

func includesDefaultBranch(includes []string) bool {
	for _, include := range includes {
		if include == "~DEFAULT_BRANCH" || include == "~ALL" {
			return true
		}
	}
	return false
}
