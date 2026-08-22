package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestCurrentCanonicalCatalogValid(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	outcome := Validate(root, schemas)
	if len(outcome.Findings) != 0 {
		for _, finding := range outcome.Findings {
			t.Logf("%s: %s (%v)", finding.Code, finding.Message, finding.Evidence)
		}
		t.Fatalf("canonical skill catalog has %d findings", len(outcome.Findings))
	}
	entries, err := os.ReadDir(filepath.Join(root, "skills", "canonical"))
	if err != nil {
		t.Fatal(err)
	}
	wantSkillCount := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "gds-") {
			wantSkillCount++
		}
	}
	if outcome.Report.SkillCount != wantSkillCount {
		t.Fatalf("skill count = %d, want %d (canonical directories)", outcome.Report.SkillCount, wantSkillCount)
	}
	for _, budget := range outcome.Report.BudgetUse {
		if budget.Chars > budget.Limit {
			t.Fatalf("plugin %s metadata budget = %d, limit %d", budget.Plugin, budget.Chars, budget.Limit)
		}
	}
}

func TestExplicitOnlySkillRequiresCodexInvocationPolicy(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills", "canonical", "gds-fixture")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
name: gds-fixture
description: Use this fixture skill only for its validation test.
---

# Contract
## Use when
fixture
## Do not use when
fixture
## Inputs
fixture
## Preconditions
fixture
## Workflow
fixture
## Stop conditions
fixture
## Verification
fixture
## Output
fixture
## References
fixture
`
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillRoot, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	sidecar := `interface:
  display_name: Fixture
  short_description: Fixture validation skill
  default_prompt: Use $gds-fixture for this fixture.
policy:
  allow_implicit_invocation: true
`
	if err := os.WriteFile(filepath.Join(skillRoot, "agents", "openai.yaml"), []byte(sidecar), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := Definition{
		Name: "gds-fixture", Path: "skills/canonical/gds-fixture",
		Invocation: "explicit-only", Mutation: "external",
		Interface: Interface{
			DisplayName: "Fixture", ShortDescription: "Fixture validation skill",
			DefaultPrompt: "Use $gds-fixture for this fixture.",
		},
	}
	findings := validateSkill(
		root, Budgets{DescriptionChars: 600, SkillLines: 300}, &definition,
	)
	found := false
	for _, finding := range findings {
		if finding.Code == "GDS_SKILL_EXPLICIT_ONLY_PROJECTION_MISSING" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing Codex invocation-policy finding: %s", strings.TrimSpace(body))
	}
}

func TestExternalMutationMustBeExplicitOnly(t *testing.T) {
	registry := Registry{
		Budgets: Budgets{InitialMetadataChars: 8000},
		Skills: []Definition{{
			Name: "gds-unsafe", Invocation: "implicit", Mutation: "external",
			Path: "skills/canonical/gds-unsafe",
		}},
	}
	_, findings := validateRegistry(t.TempDir(), &registry)
	found := false
	for _, finding := range findings {
		if finding.Code == "GDS_SKILL_EXTERNAL_MUTATION_NOT_EXPLICIT" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected external-mutation invocation finding")
	}
}
