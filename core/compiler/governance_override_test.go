package compiler

import (
	"strings"
	"testing"
)

func TestGovernanceManagementOverrideClearsInheritedDesiredValue(t *testing.T) {
	for _, mode := range []string{"observed", "ignored"} {
		t.Run(mode, func(t *testing.T) {
			base := testSource("base", "base", 100, map[string]any{"github": map[string]any{"merge": map[string]any{
				"squash_merge_commit_title": map[string]any{"management": "managed", "value": "PR_TITLE"},
			}}})
			leaf := testSource("leaf", "repository", 100, map[string]any{"github": map[string]any{"merge": map[string]any{
				"squash_merge_commit_title": map[string]any{"management": mode},
			}}})
			result := New(testSchemas(t)).Compile(testAnchor("base", "leaf"), map[string]PolicySource{"base": base, "leaf": leaf}, DevelopmentBundleVersion)
			if len(result.Findings) != 0 {
				t.Fatalf("valid management override refused: %#v", result.Findings)
			}
			value, ok := lookupPath(result.Document.Effective, strings.Split("github.merge.squash_merge_commit_title", "."))
			if !ok {
				t.Fatal("overridden contract is absent")
			}
			contract := value.(map[string]any)
			if _, exists := contract["value"]; exists {
				t.Fatalf("%s inherited a desired value: %#v", mode, contract)
			}
			if _, exists := result.Document.Provenance["/effective/github/merge/squash_merge_commit_title/value"]; exists {
				t.Fatal("removed desired value retained provenance")
			}
			if result.Document.Provenance["/effective/github/merge/squash_merge_commit_title/management"].Source != "leaf" {
				t.Fatal("management does not bind the overriding source")
			}
		})
	}
}
