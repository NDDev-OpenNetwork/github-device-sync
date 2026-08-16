package operations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

// invalidPlan builds a plan whose digest is correct and whose `head_oid` is not
// a Git OID, so schema validation fails at one exact instance path.
func invalidPlan(t *testing.T, planID string) Plan {
	t.Helper()
	created := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	input := validPlanInput()
	input.Preconditions[0].HeadOID = "not-a-git-oid"
	plan, err := NewPlan(planID, created, created.Add(15*time.Minute), input)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func findingText(findings []domain.Finding) string {
	parts := make([]string, 0, len(findings))
	for _, finding := range findings {
		parts = append(parts, finding.Message)
	}
	return strings.Join(parts, " | ")
}

// A rejected plan must name what is wrong with it.
//
// `plan.Validate` produces the exact instance path and the exact violated
// constraint, and both used to be dropped one line later in favour of a count.
// The caller then saw "Plan failed schema or semantic validation." and nothing
// else, so the only way to learn which field failed was to rebuild the plan in a
// throwaway program and call `Validate` directly. Three independent defects
// stacked inside `gds module update-pin` behind exactly that message.
func TestPutPlanReportsTheExactInstancePathThatFailedValidation(t *testing.T) {
	engine, _, _, _, _ := testEngine(t)
	plan := invalidPlan(t, "plan_01KX7BV07RHD6KRA4Z4J0KCHGW")

	putErr := engine.PutPlan(context.Background(), plan)
	if putErr == nil {
		t.Fatal("storing a schema-invalid plan succeeded")
	}
	typed := new(Error)
	if !errors.As(putErr, &typed) || typed.Code != "GDS_PLAN_INVALID" {
		t.Fatalf("expected GDS_PLAN_INVALID, got %v", putErr)
	}

	if len(typed.Findings) == 0 {
		t.Fatal("the error carries no findings, so the caller cannot know what failed")
	}
	joined := findingText(typed.Findings)
	if !strings.Contains(joined, "/preconditions/0/head_oid") {
		t.Fatalf("findings do not name the failing instance path: %s", joined)
	}
	for _, finding := range typed.Findings {
		if finding.Code == "" || finding.Severity == "" {
			t.Fatalf("finding is not a complete domain finding: %+v", finding)
		}
	}

	// The plain error string is a caller surface too: anything that logs `err`
	// without unwrapping must still learn the field rather than a count.
	if !strings.Contains(putErr.Error(), "/preconditions/0/head_oid") {
		t.Fatalf("error string hides the failing path: %s", putErr.Error())
	}
}

// The stored-plan path loses the same information at the same place, and it is
// the harder failure to diagnose: the caller did not build the plan in this
// process and cannot inspect what it holds.
func TestLoadPlanReportsTheExactInstancePathThatFailedValidation(t *testing.T) {
	engine, store, _, _, _ := testEngine(t)
	plan := invalidPlan(t, "plan_01KX7BV07RHD6KRA4Z4J0KCHGX")
	body, err := plan.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// Written through the store so the engine's own validation is bypassed:
	// this reproduces a plan that was storable once and is not valid now.
	if err := store.PutPlan(context.Background(), state.PlanRecord{
		PlanID: plan.PlanID, Operation: plan.Operation, PlanDigest: plan.PlanDigest,
		Body: body, Status: "planned", CreatedAt: plan.CreatedAt,
		ExpiresAt: plan.ExpiresAt, InsertedAt: plan.CreatedAt,
	}); err != nil {
		t.Fatal(err)
	}

	_, _, loadErr := engine.loadPlan(context.Background(), plan.PlanID)
	if loadErr == nil {
		t.Fatal("loading a schema-invalid stored plan succeeded")
	}
	typed := new(Error)
	if !errors.As(loadErr, &typed) || typed.Code != "GDS_PLAN_INVALID" {
		t.Fatalf("expected GDS_PLAN_INVALID, got %v", loadErr)
	}
	if len(typed.Findings) == 0 {
		t.Fatal("the stored-plan error carries no findings")
	}
	if !strings.Contains(findingText(typed.Findings), "/preconditions/0/head_oid") {
		t.Fatalf("findings do not name the failing instance path: %v", typed.Findings)
	}
}

// A digest mismatch is a different defect from a schema violation and the stored
// -plan branch reports both with one code. The findings must not claim a schema
// problem when the schema is satisfied and only the digest moved.
func TestLoadPlanSeparatesDigestMismatchFromSchemaFindings(t *testing.T) {
	engine, store, _, _, _ := testEngine(t)
	created := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	plan, err := NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGY", created, created.Add(15*time.Minute), validPlanInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := plan.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutPlan(context.Background(), state.PlanRecord{
		PlanID: plan.PlanID, Operation: plan.Operation,
		PlanDigest: "sha256:" + strings.Repeat("0", 64),
		Body:       body, Status: "planned", CreatedAt: plan.CreatedAt,
		ExpiresAt: plan.ExpiresAt, InsertedAt: plan.CreatedAt,
	}); err != nil {
		t.Fatal(err)
	}

	_, _, loadErr := engine.loadPlan(context.Background(), plan.PlanID)
	typed := new(Error)
	if !errors.As(loadErr, &typed) || typed.Code != "GDS_PLAN_INVALID" {
		t.Fatalf("expected GDS_PLAN_INVALID, got %v", loadErr)
	}
	if len(typed.Findings) != 1 || typed.Findings[0].Code != "GDS_PLAN_RECORD_DIGEST_MISMATCH" {
		t.Fatalf("digest mismatch is not reported as its own finding: %+v", typed.Findings)
	}
}
