package operations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

func TestLoadKillSwitchesIsStrictAndFailsClosed(t *testing.T) {
	values := map[string]string{
		MutationsDisabledEnvironment:    "false",
		WebhookReadOnlyEnvironment:      "true",
		RolloutPausedEnvironment:        "true",
		HarnessHooksDisabledEnvironment: "false",
	}
	loaded, err := LoadKillSwitches(func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MutationsDisabled || !loaded.WebhookProcessingReadOnly ||
		!loaded.RolloutPaused || loaded.HarnessHooksDisabled {
		t.Fatalf("unexpected kill switches: %+v", loaded)
	}

	loaded, err = LoadKillSwitches(func(string) (string, bool) { return "yes", true })
	if err == nil || !loaded.MutationsDisabled {
		t.Fatalf("invalid switch did not fail closed: %+v %v", loaded, err)
	}
}

func TestGlobalMutationKillSwitchBlocksBeforeOperation(t *testing.T) {
	engine, store, checker, handler, plan := testEngine(t)
	engine.KillSwitches.MutationsDisabled = true
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:blocked")
	operationError(t, err, "GDS_MUTATIONS_DISABLED", domain.ExitPolicy)
	if result.Status != "blocked" || !result.KillSwitches.MutationsDisabled ||
		checker.calls != 0 || handler.applyCalls != 0 {
		t.Fatalf("kill switch reached runtime: result=%+v checker=%d handler=%d", result, checker.calls, handler.applyCalls)
	}
	if _, err := store.GetOperationByPlan(context.Background(), plan.PlanID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("kill switch created operation: %v", err)
	}
}

func TestGlobalMutationKillSwitchBlocksVerificationJournal(t *testing.T) {
	engine, store, _, _, plan := testEngine(t)
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:verify")
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := store.ListEvents(context.Background(), result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	engine.KillSwitches.MutationsDisabled = true
	verified, err := engine.Verify(context.Background(), result.OperationID)
	operationError(t, err, "GDS_MUTATIONS_DISABLED", domain.ExitPolicy)
	if !verified.KillSwitches.MutationsDisabled {
		t.Fatalf("verify result omitted kill switch: %+v", verified)
	}
	eventsAfter, err := store.ListEvents(context.Background(), result.OperationID)
	if err != nil || len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("blocked verify changed journal: before=%d after=%d err=%v", len(eventsBefore), len(eventsAfter), err)
	}
}

func TestApprovalEvidenceBindsReferenceToExactPlanAndScope(t *testing.T) {
	engine, store, _, _, plan := testEngine(t)
	reference := "owner-request:exact-scope"
	result, err := engine.Apply(context.Background(), plan.PlanID, reference)
	if err != nil {
		t.Fatal(err)
	}
	if result.Approval == nil || result.Approval.PlanID != plan.PlanID ||
		result.Approval.PlanDigest != plan.PlanDigest ||
		result.Approval.ApprovalClass != plan.ApprovalClass ||
		result.Approval.ScopeDigest == "" ||
		result.Approval.ReferenceDigest != digestString(reference) {
		t.Fatalf("approval evidence is not scope-bound: %+v", result.Approval)
	}
	events, err := store.ListEvents(context.Background(), result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var recorded ApprovalEvidence
	for _, event := range events {
		if event.EventType == "approval-recorded" {
			if err := json.Unmarshal(event.Payload, &recorded); err != nil {
				t.Fatal(err)
			}
		}
	}
	if recorded != *result.Approval {
		t.Fatalf("journal approval mismatch: got=%+v want=%+v", recorded, *result.Approval)
	}
	encoded, _ := json.Marshal(events)
	if string(encoded) == "" || strings.Contains(string(encoded), reference) {
		t.Fatalf("raw approval leaked into journal: %s", encoded)
	}
}

func TestStepIdempotencyKeyBindsPlanAndExactStep(t *testing.T) {
	_, _, _, _, plan := testEngine(t)
	first, err := StepIdempotencyKey(plan, plan.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := StepIdempotencyKey(plan, plan.Steps[0])
	if err != nil || first != second {
		t.Fatalf("idempotency key is not deterministic: %q %q %v", first, second, err)
	}
	changed := plan.Steps[0]
	changed.Parameters = map[string]any{"fixture": "changed"}
	third, err := StepIdempotencyKey(plan, changed)
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("idempotency key did not bind exact step parameters")
	}
}
