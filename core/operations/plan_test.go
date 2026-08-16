package operations

import (
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func validPlanInput() PlanInput {
	return PlanInput{
		Operation: "fixture-operation",
		Actor:     Actor{Type: "agent-session", SessionID: "session-test"},
		Preconditions: []Precondition{{
			RepositoryID:   "repo_01JEXAMPZ0000000000000000C",
			HeadOID:        "0123456789abcdef0123456789abcdef01234567",
			ManifestDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			PolicyDigest:   "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		}},
		Steps: []Step{{
			StepID: "step-001", RepositoryID: "repo_01JEXAMPZ0000000000000000C",
			Action: "fixture-action", RequiresApproval: true,
			Compensation: Compensation{Mode: "explicit-plan", Action: "fixture-restore"},
		}},
		ApprovalClass: "local-write",
	}
}

func TestNewPlanHasCanonicalDigestAndValidSchema(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	plan, err := NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGV", created, created.Add(15*time.Minute), validPlanInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if findings := plan.Validate(schemas); len(findings) != 0 {
		t.Fatalf("plan findings: %+v", findings)
	}
	second, err := NewPlan(
		plan.PlanID, created, created.Add(15*time.Minute), validPlanInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.PlanDigest != plan.PlanDigest {
		t.Fatalf("non-deterministic digest: %s %s", plan.PlanDigest, second.PlanDigest)
	}
}

func TestPlanDigestDetectsTampering(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	plan, err := NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGV", created, created.Add(15*time.Minute), validPlanInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Action = "tampered-action"
	found := false
	for _, finding := range plan.Validate(schemas) {
		if finding.Code == "GDS_PLAN_DIGEST_MISMATCH" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected plan digest mismatch")
	}
}

func TestPlanSemanticValidationBindsScopePreconditionsAndSteps(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	plan, err := NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGV", created,
		created.Add(15*time.Minute), validPlanInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps = append(plan.Steps, plan.Steps[0])
	plan.ExpiresAt = plan.CreatedAt
	plan.PlanDigest, err = planDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	semantic := 0
	for _, finding := range plan.Validate(schemas) {
		if finding.Code == "GDS_PLAN_SEMANTIC_INVALID" {
			semantic++
		}
		if finding.Code == "GDS_PLAN_DIGEST_MISMATCH" {
			t.Fatal("semantic fixture unexpectedly has a stale digest")
		}
	}
	if semantic < 2 {
		t.Fatalf("semantic findings=%d, want duplicate step and invalid expiry", semantic)
	}
}
