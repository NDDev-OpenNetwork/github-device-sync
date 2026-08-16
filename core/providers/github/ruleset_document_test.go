package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A tracked contract is written in the provider's wire shape, where rule fields
// sit under `parameters`. Unmarshalling it straight into RepositoryRuleset
// succeeds and silently yields empty rules, so every later comparison passes
// vacuously and drift is never reported. These pin the decoder that prevents it.
func TestDecodeRulesetDocumentReadsNestedRuleParameters(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "live-branch-ruleset.json"))
	if err != nil {
		t.Fatal(err)
	}
	ruleset, err := DecodeRulesetDocument(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	var checks *RulesetRule
	for index := range ruleset.Rules {
		if ruleset.Rules[index].Type == "required_status_checks" {
			checks = &ruleset.Rules[index]
		}
	}
	if checks == nil {
		t.Fatal("required_status_checks rule was dropped")
	}
	if len(checks.RequiredStatusChecks) == 0 {
		t.Fatal("nested required_status_checks decoded empty; comparisons would pass vacuously")
	}
	if !checks.StrictRequiredStatusChecksPolicy {
		t.Error("nested strict policy flag was not decoded")
	}

	// A direct unmarshal is the mistake this decoder exists to prevent; proving
	// it stays silent keeps the reason for the decoder visible.
	var direct RepositoryRuleset
	if err := json.Unmarshal(raw, &direct); err != nil {
		t.Fatalf("direct unmarshal unexpectedly failed: %v", err)
	}
	for _, rule := range direct.Rules {
		if rule.Type == "required_status_checks" && len(rule.RequiredStatusChecks) != 0 {
			t.Fatal("direct unmarshal now populates nested rules; the decoder's rationale changed")
		}
	}
}

func TestDecodeRulesetDocumentDropsExternallyOwnedParameters(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "live-branch-ruleset.json"))
	if err != nil {
		t.Fatal(err)
	}
	ruleset, err := DecodeRulesetDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's pull_request rule carries required_reviewers,
	// dismissal_restriction and allowed_merge_methods. Those are preserved on an
	// observation, but a desired ruleset must never declare ownership of them.
	for _, rule := range ruleset.Rules {
		if rule.ExternalParameters != nil {
			t.Errorf("rule %q carries external parameters into the desired ruleset", rule.Type)
		}
		if rule.OpaqueParameters != nil {
			t.Errorf("rule %q carries opaque parameters into the desired ruleset", rule.Type)
		}
	}
}

func TestDecodeRulesetDocumentRejectsMalformedContracts(t *testing.T) {
	for _, testCase := range []struct {
		name string
		raw  string
	}{
		{"no rules", `{"name":"x","target":"branch","enforcement":"active","rules":[]}`},
		{"no name", `{"target":"branch","enforcement":"active","rules":[{"type":"deletion"}]}`},
		{"repeated rule", `{"name":"x","rules":[{"type":"deletion"},{"type":"deletion"}]}`},
		{"not an object", `[]`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := DecodeRulesetDocument([]byte(testCase.raw)); err == nil {
				t.Fatal("malformed ruleset document was accepted")
			}
		})
	}
}
