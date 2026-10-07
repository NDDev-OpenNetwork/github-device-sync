package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestDevinVersionDetectionDoesNotConfigureTheHarness(t *testing.T) {
	root := repoRootForTest(t)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	program := filepath.Join(bin, "devin")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n[ \"$1\" = --version ] || exit 42\nprintf 'devin 1.2.3\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	home := t.TempDir()
	t.Setenv("HOME", home)
	observation, findings := Detect(context.Background(), root, "devin", schemas)
	if len(findings) != 0 || observation.Result != "observed" || observation.Version != "devin 1.2.3" {
		t.Fatalf("Devin observation=%+v findings=%+v", observation, findings)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("version observation changed the home directory: %v %v", entries, err)
	}
}

func TestDevinRegistryDoesNotClaimDelegatedProofOrInstallConfiguration(t *testing.T) {
	root := repoRootForTest(t)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	report, findings := ValidateStatic(root, "devin", schemas)
	if len(findings) != 0 || report.CapabilityStatus != "provisional" || report.RuntimeEvidence != "not-proven" || report.RuntimeEvidenceOwner != "" {
		t.Fatalf("unproven Devin capabilities were promoted: %+v %+v", report, findings)
	}
	adapter, findings := NewAdapter(root, "devin", schemas)
	if len(findings) != 0 {
		t.Fatal(findings)
	}
	target := t.TempDir()
	_, findings = adapter.PlanInstall(target, RenderRequest{SkillProfile: "core", Scope: "project"})
	if !containsHarnessFinding(findings, "GDS_HARNESS_PROJECT_SKILLS_NOT_PROVEN") {
		t.Fatalf("observation-only Devin produced a configuration plan: %+v", findings)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refused adapter plan changed the target: %v %v", entries, err)
	}
}

func TestExplicitDevinRuntimeProofRemainsUnproven(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	_, findings := Validate(repoRootForTest(t), "devin", schemas)
	if !containsHarnessFinding(findings, "GDS_HARNESS_RUNTIME_UNOWNED") {
		t.Fatalf("an explicit runtime request accepted an installed-only CLI: %+v", findings)
	}
}
