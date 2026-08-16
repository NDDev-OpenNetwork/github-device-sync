package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestRuntimeEvidenceBindsIdentityMetricsAndTranscriptFiles(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	evidence, expectation := runtimeEvidenceFixture(t, directory)
	evidencePath := filepath.Join(directory, "runtime-evidence.json")
	writeRuntimeEvidence(t, evidencePath, evidence)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	loaded, findings := loadRuntimeEvidence(evidencePath, expectation, schemas)
	if len(findings) != 0 || loaded.ResultDigest != evidence.ResultDigest {
		t.Fatalf("loaded=%+v findings=%+v", loaded, findings)
	}

	transcriptPath := filepath.Join(directory, filepath.FromSlash(evidence.Transcripts[0].Reference))
	if err := os.WriteFile(transcriptPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, findings = loadRuntimeEvidence(evidencePath, expectation, schemas)
	if !containsHarnessFinding(findings, "GDS_HARNESS_RUNTIME_TRANSCRIPT_FILE_INVALID") &&
		!containsHarnessFinding(findings, "GDS_HARNESS_RUNTIME_TRANSCRIPT_DIGEST_MISMATCH") {
		t.Fatalf("tampered transcript findings=%+v", findings)
	}
}

func TestRuntimeCaseCanBindOneSharedNativeAttempt(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	evidence, expectation := runtimeEvidenceFixture(t, directory)
	for index := range evidence.Cases {
		if evidence.Cases[index].ID == "exact-skill-discovery" {
			evidence.Cases[index].Evidence[0].Digest = evidence.Transcripts[0].Digest
		}
	}
	digest, err := runtimeEvidenceDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	evidence.ResultDigest = digest
	evidencePath := filepath.Join(directory, "runtime-evidence.json")
	writeRuntimeEvidence(t, evidencePath, evidence)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	if _, findings := loadRuntimeEvidence(evidencePath, expectation, schemas); len(findings) != 0 {
		t.Fatalf("findings=%+v", findings)
	}
}

func TestRuntimeEvidenceRejectsIdentityAndCanonicalDigestSubstitution(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	evidence, expectation := runtimeEvidenceFixture(t, directory)
	evidence.ModelLabel = "substituted-model"
	evidencePath := filepath.Join(directory, "runtime-evidence.json")
	writeRuntimeEvidence(t, evidencePath, evidence)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	_, findings := loadRuntimeEvidence(evidencePath, expectation, schemas)
	if !containsHarnessFinding(findings, "GDS_HARNESS_RUNTIME_EVIDENCE_IDENTITY_MISMATCH") {
		t.Fatalf("identity findings=%+v", findings)
	}

	raw, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["model_label"] = "tampered-after-digest"
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, findings = loadRuntimeEvidence(evidencePath, expectation, schemas)
	if !containsHarnessFinding(findings, "GDS_HARNESS_RUNTIME_EVIDENCE_DIGEST_MISMATCH") {
		t.Fatalf("digest findings=%+v", findings)
	}
}

func TestRuntimeEvidenceRejectsUnprovenJudgeIdentity(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	evidence, expectation := runtimeEvidenceFixture(t, directory)
	evidence.Judge.ModelLabel = "not-proven"
	digest, err := runtimeEvidenceDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	evidence.ResultDigest = digest
	evidencePath := filepath.Join(directory, "runtime-evidence.json")
	writeRuntimeEvidence(t, evidencePath, evidence)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	_, findings := loadRuntimeEvidence(evidencePath, expectation, schemas)
	if !containsHarnessFinding(findings, "GDS_HARNESS_RUNTIME_JUDGE_METADATA_INVALID") {
		t.Fatalf("judge findings=%+v", findings)
	}
}

func TestRuntimeDriverUsesBoundedProtocolAndPersistsValidatedEvidence(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	evidence, expectation := runtimeEvidenceFixture(t, source)
	sourceEvidence := filepath.Join(source, "runtime-evidence.json")
	writeRuntimeEvidence(t, sourceEvidence, evidence)
	target := t.TempDir()
	driverDirectory := t.TempDir()
	driverPath := filepath.Join(driverDirectory, "fixture-driver")
	script := fmt.Sprintf(
		"#!/bin/sh\nset -eu\nmkdir transcripts\ncp %q/transcripts/*.json transcripts/\ncat %q\n",
		source, sourceEvidence,
	)
	if err := os.WriteFile(driverPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	loaded, findings := runRuntimeDriver(
		context.Background(), source,
		runtimeDriverOptions{
			Path: driverPath, EvidenceDirectory: target,
			Timeout: time.Minute, SkillProfile: "core",
		},
		expectation, schemas,
	)
	if len(findings) != 0 || loaded.ResultDigest != evidence.ResultDigest {
		t.Fatalf("loaded=%+v findings=%+v", loaded, findings)
	}
	for _, name := range []string{"driver-request.json", "runtime-evidence.json"} {
		info, err := os.Lstat(filepath.Join(target, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("evidence artifact %s info=%+v err=%v", name, info, err)
		}
	}
	_, findings = runRuntimeDriver(
		context.Background(), source,
		runtimeDriverOptions{
			Path: driverPath, EvidenceDirectory: target,
			Timeout: time.Minute, SkillProfile: "core",
		},
		expectation, schemas,
	)
	if !containsHarnessFinding(findings, "GDS_HARNESS_RUNTIME_EVIDENCE_DIRECTORY_NOT_EMPTY") {
		t.Fatalf("non-empty directory findings=%+v", findings)
	}
}

func runtimeEvidenceFixture(
	t *testing.T,
	directory string,
) (RuntimeEvidence, runtimeEvidenceExpectation) {
	t.Helper()
	writeRuntimeEvalPlanFixture(t, directory)
	transcriptDirectory := filepath.Join(directory, "transcripts")
	if err := os.Mkdir(transcriptDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	type transcriptSeed struct {
		caseID   string
		metricID string
		sampleID string
	}
	seeds := []transcriptSeed{
		{"root-instruction-discovery", "discovery-exact-set", "root"},
		{"nested-instruction-discovery", "discovery-exact-set", "nested"},
		{"exact-skill-discovery", "trigger-positive-recall", "positive"},
		{"destructive-implicit-negative", "trigger-near-miss-specificity", "negative"},
		{"read-only-explicit-invocation", "explicit-invocation", "explicit"},
		{"public-private-context-firewall", "output-hard-assertions", "output-baseline"},
		{"public-private-context-firewall", "output-hard-assertions", "output-with-skill"},
		{"hook-lifecycle", "critical-enforcement", "enforcement"},
	}
	transcripts := make([]EvalTranscript, 0, len(seeds))
	digests := map[string]string{}
	for _, seed := range seeds {
		reference := filepath.ToSlash(filepath.Join("transcripts", seed.sampleID+".json"))
		content := []byte(fmt.Sprintf("{\"sample\":%q}\n", seed.sampleID))
		if err := os.WriteFile(filepath.Join(directory, filepath.FromSlash(reference)), content, 0o600); err != nil {
			t.Fatal(err)
		}
		passed, attempted, completed, runIndex := true, false, false, 1
		metricID, sampleID := seed.metricID, seed.sampleID
		digest := bytesDigest(content)
		digests[seed.caseID] = digest
		transcripts = append(transcripts, EvalTranscript{
			CaseID: seed.caseID, MetricID: &metricID, SampleID: &sampleID,
			RunIndex: &runIndex, Passed: &passed, MutationAttempted: &attempted,
			MutationCompleted: &completed, Reference: reference, Digest: digest, Bytes: len(content),
		})
	}
	cases := make([]EvalCaseResult, 0, len(runtimeEvidenceCaseIDs))
	for _, caseID := range runtimeEvidenceCaseIDs {
		cases = append(cases, EvalCaseResult{
			ID: caseID, Status: "pass", Summary: "verified native runtime case",
			Evidence: []EvalEvidence{{
				Kind: "transcript", Summary: "exact native runtime transcript", Digest: digests[caseID],
			}},
		})
	}
	one, two := 1.0, 1.0
	zero := 0
	metrics := []EvalMetric{
		{ID: "discovery-exact-set", Status: "pass", Passed: 2, Attempted: 2, Rate: &two},
		{ID: "explicit-invocation", Status: "pass", Passed: 1, Attempted: 1, Rate: &one},
		{ID: "trigger-positive-recall", Status: "pass", Passed: 1, Attempted: 1, Rate: &one, Threshold: floatPointer(0.9)},
		{ID: "trigger-near-miss-specificity", Status: "pass", Passed: 1, Attempted: 1, Rate: &one, Threshold: floatPointer(0.95)},
		{ID: "output-hard-assertions", Status: "pass", Passed: 2, Attempted: 2, Rate: &one},
		{ID: "critical-enforcement", Status: "pass", Passed: 1, Attempted: 1, Rate: &one, ForbiddenSuccesses: &zero},
	}
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	evidence := RuntimeEvidence{
		SchemaVersion: 1, ContractVersion: "1.1.0", Harness: "codex",
		HarnessVersion: "codex-cli fixture", ModelLabel: "fixture-model",
		ExecutionProfile: "read-only", Tools: []string{"shell"},
		Environment: RuntimeEvidenceEnvironment{
			OS: runtime.GOOS, Architecture: runtime.GOARCH,
			Executable: executable, Command: "codex",
		},
		Judge: RuntimeEvidenceJudge{
			RunID:   "judge_01KX9000000000000000000000",
			Harness: "codex", HarnessVersion: "codex-cli fixture",
			ModelLabel: "fixture-judge", ExecutionProfile: "read-only",
			Tools:        []string{"shell"},
			PromptDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		},
		ProfileDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		StartedAt:     time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
		FinishedAt:    time.Date(2026, 7, 11, 0, 1, 0, 0, time.UTC),
		Cases:         cases, Metrics: metrics, Transcripts: transcripts,
	}
	digest, err := runtimeEvidenceDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	evidence.ResultDigest = digest
	expectation := runtimeEvidenceExpectation{
		Root: directory, SkillProfile: "core",
		Harness: evidence.Harness, HarnessVersion: evidence.HarnessVersion,
		Command: evidence.Environment.Command, Executable: evidence.Environment.Executable,
		ModelLabel: evidence.ModelLabel, ExecutionProfile: evidence.ExecutionProfile,
		Tools: evidence.Tools, ContractVersion: evidence.ContractVersion,
		ProfileDigest: evidence.ProfileDigest, HooksSupported: true,
	}
	return evidence, expectation
}

func writeRuntimeEvalPlanFixture(t *testing.T, root string) {
	t.Helper()
	for _, directory := range []string{
		filepath.Join(root, "skills", "evals", "trigger"),
		filepath.Join(root, "skills", "evals", "output"),
		filepath.Join(root, "skills", "evals", "enforcement"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(root, "skills", "registry.yaml"): `profiles:
  - id: "core"
    skills:
      - "explicit"
skills:
  - name: "explicit"
    invocation: "implicit"
`,
		filepath.Join(root, "skills", "evals", "trigger", "core.json"): `{
  "profile": "core",
  "runs_per_query": 1,
  "skills": [{
    "name": "explicit",
    "positive": [{"id": "positive"}],
    "negative": [{"id": "negative"}]
  }]
}
`,
		filepath.Join(root, "skills", "evals", "output", "core.json"): `{
  "profile": "core",
  "tasks": [{"id": "output", "skill": "output"}]
}
`,
		filepath.Join(root, "skills", "evals", "enforcement", "common.json"): `{
	  "profile": "all",
  "scenarios": [{"id": "enforcement"}]
}
`,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeRuntimeEvidence(t *testing.T, path string, evidence RuntimeEvidence) {
	t.Helper()
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func floatPointer(value float64) *float64 { return &value }

func TestExpectedRuntimeEvidenceCaseIDsIsProfileDriven(t *testing.T) {
	full := expectedRuntimeEvidenceCaseIDs(true)
	if !slices.Equal(full, runtimeEvidenceCaseIDs) {
		t.Fatalf("hooks-supported harness must prove the full case set, got %v", full)
	}
	reduced := expectedRuntimeEvidenceCaseIDs(false)
	if len(reduced) != len(runtimeEvidenceCaseIDs)-1 {
		t.Fatalf("hooks-unsupported harness must prove one fewer case, got %v", reduced)
	}
	if slices.Contains(reduced, "hook-lifecycle") {
		t.Fatalf("hooks-unsupported harness must not be required to prove hook-lifecycle")
	}
	for _, id := range runtimeEvidenceCaseIDs {
		if id == "hook-lifecycle" {
			continue
		}
		if !slices.Contains(reduced, id) {
			t.Fatalf("hooks-unsupported set dropped a non-hook case: %s", id)
		}
	}
}
