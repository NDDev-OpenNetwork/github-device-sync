package governance

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

func TestCompareSeparatesManagedObservedAndIgnoredFields(t *testing.T) {
	policy := compiler.CompiledPolicyDocument{
		CompiledPolicy: compiler.CompiledPolicyMetadata{Digest: "sha256:policy"},
		Effective: map[string]any{"github": map[string]any{
			"actions": map[string]any{
				"selected_actions": map[string]any{
					"management": "managed",
					"value": map[string]any{
						"github_owned_allowed": true,
						"verified_allowed":     false,
						"patterns_allowed":     []any{"example-org/ci-workflows/.github/workflows/*@*"},
					},
				},
			},
			"merge": map[string]any{
				"allow_squash_merge": map[string]any{"management": "managed", "value": true},
				"allow_rebase_merge": map[string]any{"management": "managed", "value": false},
			},
			"releases": map[string]any{
				"immutable": map[string]any{"management": "managed", "value": true},
			},
			"security": map[string]any{"management": "observed"},
			"rulesets": map[string]any{"management": "ignored"},
		}},
	}
	snapshot := githubprovider.GovernanceSnapshot{
		Repository: githubprovider.Repository{
			Merge: githubprovider.MergeSettings{AllowSquashMerge: true, AllowRebaseMerge: true},
			Security: githubprovider.SecuritySettings{
				Available: true, Features: map[string]string{"secret_scanning": "enabled"},
			},
		},
		Rulesets:          []githubprovider.RulesetSummary{},
		ImmutableReleases: githubprovider.ImmutableReleases{Enabled: false},
		SelectedActions: &githubprovider.SelectedActionsPermissions{
			GitHubOwnedAllowed: true,
			PatternsAllowed:    []string{"example-org/ci-workflows/.github/workflows/*@*"},
		},
	}
	result := Compare(policy, snapshot)
	if result.Status != "drift" || result.PolicyDigest != "sha256:policy" ||
		result.Counts["compliant"] != 2 || result.Counts["drift"] != 2 ||
		result.Counts["observed"] != 1 || result.Counts["ignored"] != 1 ||
		len(result.Fields) != 6 {
		t.Fatalf("result=%#v", result)
	}
}

func TestCompareReportsMissingGitHubPolicyWithoutInventingDrift(t *testing.T) {
	result := Compare(compiler.CompiledPolicyDocument{}, githubprovider.GovernanceSnapshot{})
	if result.Status != "not-declared" || len(result.Fields) != 0 || len(result.Counts) != 0 {
		t.Fatalf("result=%#v", result)
	}
}
