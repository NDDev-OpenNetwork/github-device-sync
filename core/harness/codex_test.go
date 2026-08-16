package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestCodexStaticContractPassesAndRuntimeIsDelegated(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	report, findings := ValidateCodex(root, schemas)
	// Runtime proof is owned by the private example-harnesses repository, which the
	// module bridge names. The evidence value has to say that: recording
	// `not-proven` for a decision to delegate reads like a failed attempt, and
	// that is exactly how the release gate came to report success over seventeen
	// profiles that had never been runtime-tested here.
	if report.RuntimeEvidence != "delegated" {
		t.Fatalf("runtime evidence = %q, want delegated", report.RuntimeEvidence)
	}
	if report.RuntimeEvidenceOwner != "example-org/example-harnesses" {
		t.Fatalf("evidence owner = %q, want the bridge owner", report.RuntimeEvidenceOwner)
	}
	if len(report.Plugins) != 3 {
		t.Fatalf("plugin count = %d, want 3", len(report.Plugins))
	}
	if report.Instructions == nil || report.Instructions.LongestChain.CombinedBytes == 0 ||
		report.Instructions.LongestChain.CombinedBytes > report.Instructions.AlertBytes {
		t.Fatalf("instruction report = %#v", report.Instructions)
	}
	if len(findings) != 0 {
		t.Fatalf("runtime proof is delegated, expected no findings: %+v", findings)
	}
}

func TestCodexInstructionInspectionDetectsOverrideAndDuplicateChain(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("# Contract\n")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "AGENTS.md"), []byte("masked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "AGENTS.override.md"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	report, findings := inspectCodexInstructions(root)
	if report.LongestChain.CombinedBytes != len(content)*2 ||
		!containsHarnessFinding(findings, "GDS_CODEX_OVERRIDE_ACTIVE") ||
		!containsHarnessFinding(findings, "GDS_CODEX_INSTRUCTION_DUPLICATE") {
		t.Fatalf("report = %#v, findings = %#v", report, findings)
	}
}

func containsHarnessFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestCanonicalRegistryHasExactHarnessSet(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	report, findings := ValidateAll(root, schemas)
	if len(report.Harnesses) != len(CanonicalIDs) {
		t.Fatalf("harness count = %d, want %d", len(report.Harnesses), len(CanonicalIDs))
	}
	if report.RuntimeContract.Cases != len(RuntimeCaseIDs) ||
		report.RuntimeContract.Harnesses != len(CanonicalIDs) {
		t.Fatalf("runtime contract = %#v", report.RuntimeContract)
	}
	if len(report.Aliases) != 1 || report.Aliases["antigravity"] != "antigravity-cli" {
		t.Fatalf("alias map = %#v", report.Aliases)
	}
	// The control plane delegates harness runtime proof to the isolated per-harness
	// setup systems (every profile declares runtime_tests.required=false), so a
	// valid canonical catalog raises no findings at all.
	if len(findings) != 0 {
		t.Fatalf("expected no findings once runtime proof is delegated, got %#v", findings)
	}
}
