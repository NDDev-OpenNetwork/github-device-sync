package app

import (
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"testing"
)

func TestDeliveryPolicyRemovesChecksAndRequiresObservedAbsence(t *testing.T) {
	original := githubprovider.RepositoryRuleset{Name: "main", Enforcement: "active", Rules: []githubprovider.RulesetRule{
		{Type: "required_signatures"}, {Type: "required_status_checks", RequiredStatusChecks: []githubprovider.RequiredStatusCheck{{Context: "background"}}},
	}}
	for _, profile := range []string{"", "standard", "continuous-development"} {
		desired := rulesetForDeliveryPolicy(original, compiler.CompiledPolicyDocument{Effective: map[string]any{"delivery": map[string]any{"profile": profile}}})
		advisory := profile == "continuous-development"
		if desired.RemoveRequiredStatusChecks != advisory {
			t.Fatal("removal is not opt-in")
		}
		observed := githubprovider.RepositoryRulesetState{Enforcement: "active", Rules: original.Rules}
		if rulesetOwnedStateMatches(observed, desired) == advisory {
			t.Fatal("comparison did not account for existing checks")
		}
		observed.Rules = []githubprovider.RulesetRule{{Type: "required_signatures"}}
		if rulesetOwnedStateMatches(observed, desired) != advisory {
			t.Fatal("comparison did not account for absent checks")
		}
	}
	if len(original.Rules) != 2 || original.Rules[1].Type != "required_status_checks" {
		t.Fatal("projection changed its input")
	}
}
