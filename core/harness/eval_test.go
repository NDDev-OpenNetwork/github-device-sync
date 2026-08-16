package harness

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestEvaluateEmitsTypedEvidenceWithoutPromotingUnprovenRuntime(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	run, findings := Evaluate(
		context.Background(), root, "codex", EvalOptions{
			SkillProfile: "core", ModelLabel: "not-proven",
			ExecutionProfile: "read-only", Tools: []string{"shell", "shell"},
		}, schemas, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
		bytes.NewReader(make([]byte, 10)),
	)
	if run.Result != "not-proven" || len(run.Cases) != len(RuntimeCaseIDs) ||
		run.ResultDigest == "" || run.HarnessVersion != nil {
		t.Fatalf("run=%+v", run)
	}
	if len(run.Tools) != 1 || run.Tools[0] != "shell" {
		t.Fatalf("tools=%v", run.Tools)
	}
	for _, caseID := range []string{
		"clean-install", "generated-projection-drift", "update-and-rollback", "remove",
	} {
		if result := evalCaseByID(run.Cases, caseID); result.Status != "pass" || len(result.Evidence) == 0 {
			t.Fatalf("case %s=%+v", caseID, result)
		}
	}
	if result := evalCaseByID(run.Cases, "exact-skill-discovery"); result.Status != "not-proven" {
		t.Fatalf("runtime-only case=%+v", result)
	}
	if !containsHarnessFinding(findings, "GDS_HARNESS_EVAL_NOT_PROVEN") {
		t.Fatalf("findings=%+v", findings)
	}
	for _, finding := range findings {
		if finding.Code == "GDS_INSTANCE_INVALID" || finding.Code == "GDS_HARNESS_EVAL_DIGEST_MISMATCH" {
			t.Fatalf("invalid evidence finding=%+v", finding)
		}
	}

	tampered := run
	tampered.ModelLabel = "tampered"
	if !containsHarnessFinding(validateEvalRun(tampered, schemas), "GDS_HARNESS_EVAL_DIGEST_MISMATCH") {
		t.Fatal("tampered evaluation digest was accepted")
	}
}

func evalCaseByID(cases []EvalCaseResult, id string) EvalCaseResult {
	for _, item := range cases {
		if item.ID == id {
			return item
		}
	}
	return EvalCaseResult{}
}
