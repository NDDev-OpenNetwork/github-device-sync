package validation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

// Derived from the tracked files rather than from a fixture, so adding a
// required check to the ruleset and forgetting the anchor fails here rather than
// agreeing with itself.
func trackedRequiredContexts(t *testing.T, root string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".github", "rulesets", "branch-main.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Rules []struct {
			Type       string `json:"type"`
			Parameters struct {
				RequiredStatusChecks []struct {
					Context string `json:"context"`
				} `json:"required_status_checks"`
			} `json:"parameters"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	contexts := []string{}
	for _, rule := range document.Rules {
		if rule.Type != "required_status_checks" {
			continue
		}
		for _, check := range rule.Parameters.RequiredStatusChecks {
			contexts = append(contexts, check.Context)
		}
	}
	sort.Strings(contexts)
	return contexts
}

func anchorRequiredContexts(t *testing.T, root string) []string {
	t.Helper()
	value, err := serialization.DecodeFile(filepath.Join(root, ".gds", "repository.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	document, _ := value.(map[string]any)
	verification, _ := document["verification"].(map[string]any)
	raw, _ := verification["required_contexts"].([]any)
	contexts := make([]string, 0, len(raw))
	for _, entry := range raw {
		if text, ok := entry.(string); ok {
			contexts = append(contexts, text)
		}
	}
	sort.Strings(contexts)
	return contexts
}

// This control plane declares its own gate, which is what keeps the field from
// being the decorative kind it was added to replace.
func TestThisRepositoryDeclaresTheGateItsRulesetAsksFor(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	declared := anchorRequiredContexts(t, root)
	if len(declared) == 0 {
		t.Fatal("the anchor declares no required context")
	}
	if findings := RequiredContextFindings(root, declared); len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	tracked := trackedRequiredContexts(t, root)
	if len(declared) != len(tracked) {
		t.Fatalf("anchor declares %d contexts, the tracked ruleset asks for %d", len(declared), len(tracked))
	}
}

func TestDriftIsReportedInBothDirections(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	tracked := trackedRequiredContexts(t, root)
	if len(tracked) < 2 {
		t.Skip("the tracked ruleset requires fewer than two contexts")
	}

	// The anchor understates the gate.
	findings := RequiredContextFindings(root, tracked[1:])
	if len(findings) != 1 ||
		findings[0].Code != "GDS_REPOSITORY_REQUIRED_CONTEXT_UNDECLARED" ||
		findings[0].Evidence["context"] != tracked[0] {
		t.Fatalf("findings = %#v", findings)
	}

	// The anchor overstates it.
	findings = RequiredContextFindings(root, append(append([]string{}, tracked...), "invented-gate"))
	if len(findings) != 1 ||
		findings[0].Code != "GDS_REPOSITORY_REQUIRED_CONTEXT_UNENFORCED" ||
		findings[0].Evidence["context"] != "invented-gate" {
		t.Fatalf("findings = %#v", findings)
	}
}

// A repository that tracks no ruleset has no local witness, and inventing a
// verdict there would report agreement or drift with nothing behind either.
func TestATreeWithNoTrackedRulesetIsNotJudged(t *testing.T) {
	t.Parallel()
	if findings := RequiredContextFindings(t.TempDir(), []string{"ci-gate"}); len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
}

// A ruleset that does not reach the default branch says nothing about the gate
// on it. Counting one would attribute a tag or feature-branch rule to `main`.
func TestARulesetThatMissesTheDefaultBranchIsIgnored(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, ".github", "rulesets")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	document := `{"conditions":{"ref_name":{"include":["refs/tags/v*"]}},` +
		`"rules":[{"type":"required_status_checks","parameters":` +
		`{"required_status_checks":[{"context":"tag-gate"}]}}]}`
	if err := os.WriteFile(
		filepath.Join(directory, "tag-semver.json"), []byte(document), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if findings := RequiredContextFindings(root, []string{"ci-gate"}); len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
}
