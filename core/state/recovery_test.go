package state

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func interruptedOperationFixture(t *testing.T, stepStatus string) (*Store, RecoverySnapshot) {
	t.Helper()
	store := newTestStore(t)
	ctx := context.Background()
	plan := testPlanRecord()
	if err := store.PutPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPlan(ctx, plan.PlanID, "planned", "approved"); err != nil {
		t.Fatal(err)
	}
	operationID := "op_01KX7BV07RHD6KRA4Z4J0KCHGX"
	if err := store.StartOperation(ctx, OperationRecord{
		OperationID: operationID, PlanID: plan.PlanID, Operation: plan.Operation,
		Status: "applying", Actor: json.RawMessage(`{"type":"agent-session"}`),
		StartedAt: testTime,
	}, []StepRecord{{
		OperationID: operationID, StepID: "step-001",
		RepositoryID: "repo_01JEXAMPZ0000000000000000C", Action: "fixture-action",
		IdempotencyKey: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Sequence:       0, Status: "pending",
	}}); err != nil {
		t.Fatal(err)
	}
	if stepStatus == "applying" {
		if err := store.TransitionStep(
			ctx, operationID, "step-001", "pending", "applying",
			testTime.Add(time.Second), nil, nil, "",
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AcquireLock(ctx, Lock{
		Scope: "repository", ScopeID: "repo_01JEXAMPZ0000000000000000C",
		LockID: "lock_01KX7BV07RHD6KRA4Z4J0KCHGX", OperationID: operationID,
		DeviceID: "device:test", SessionID: "session-dead", PID: 2147483647,
		AcquiredAt: testTime, LeaseExpiresAt: testTime.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.RecoverySnapshot(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	return store, snapshot
}

func TestRecoverOperationAbortsPendingJournalAndReleasesExactExpiredLock(t *testing.T) {
	store, snapshot := interruptedOperationFixture(t, "pending")
	ctx := context.Background()
	after, err := store.RecoverOperation(ctx, RecoveryMutation{
		Expected: snapshot, Mode: "abort-interrupted", Reason: "owner-process-dead",
		NextOperationStatus: "failed", NextPlanStatus: "failed",
		RecoveredAt: testTime.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.OperationStatus != "failed" || after.PlanStatus != "failed" ||
		len(after.Locks) != 0 || len(after.Steps) != 1 || after.Steps[0].Status != "blocked" ||
		after.Digest == snapshot.Digest {
		t.Fatalf("unexpected recovered state: %#v", after)
	}
	if len(after.Events) != len(snapshot.Events)+1 ||
		after.Events[len(after.Events)-1].EventType != "operation-recovered" {
		t.Fatalf("recovery event missing: %#v", after.Events)
	}
	if _, err := store.RecoverOperation(ctx, RecoveryMutation{
		Expected: snapshot, Mode: "abort-interrupted", Reason: "owner-process-dead",
		NextOperationStatus: "failed", NextPlanStatus: "failed",
		RecoveredAt: testTime.Add(3 * time.Minute),
	}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale recovery error=%v", err)
	}
}

func TestRecoverOperationRejectsUnknownApplyingStep(t *testing.T) {
	store, snapshot := interruptedOperationFixture(t, "applying")
	_, err := store.RecoverOperation(context.Background(), RecoveryMutation{
		Expected: snapshot, Mode: "close-partial", Reason: "owner-process-dead",
		NextOperationStatus: "partial", NextPlanStatus: "partial",
		RecoveredAt: testTime.Add(2 * time.Minute),
	})
	if err == nil {
		t.Fatal("recovery accepted unknown applying-step side effects")
	}
	after, loadErr := store.RecoverySnapshot(context.Background(), snapshot.OperationID)
	if loadErr != nil || after.Digest != snapshot.Digest || len(after.Locks) != 1 {
		t.Fatalf("unsafe recovery changed state: %#v %v", after, loadErr)
	}
}

func TestRecoverOperationClosesCompletedInterruptedWorkAsPartial(t *testing.T) {
	store, original := interruptedOperationFixture(t, "pending")
	ctx := context.Background()
	if err := store.TransitionStep(
		ctx, original.OperationID, "step-001", "pending", "applying",
		testTime.Add(10*time.Second), nil, nil, "",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionStep(
		ctx, original.OperationID, "step-001", "applying", "succeeded",
		testTime.Add(20*time.Second), map[string]any{"before": true},
		map[string]any{"after": true}, "",
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.RecoverySnapshot(ctx, original.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.RecoverOperation(ctx, RecoveryMutation{
		Expected: snapshot, Mode: "close-partial", Reason: "owner-process-dead",
		NextOperationStatus: "partial", NextPlanStatus: "partial",
		RecoveredAt: testTime.Add(2 * time.Minute),
	})
	if err != nil || after.OperationStatus != "partial" || after.PlanStatus != "partial" ||
		after.Steps[0].Status != "succeeded" || len(after.Locks) != 0 {
		t.Fatalf("after=%#v err=%v", after, err)
	}
}

func TestRecoverOperationReleasesOnlyStaleLockForTerminalJournal(t *testing.T) {
	store, original := interruptedOperationFixture(t, "pending")
	ctx := context.Background()
	if err := store.TransitionStep(
		ctx, original.OperationID, "step-001", "pending", "blocked",
		testTime.Add(10*time.Second), nil, nil, "fixture-blocked",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOperation(
		ctx, original.OperationID, "applying", "blocked",
		testTime.Add(20*time.Second), map[string]any{"code": "GDS_FIXTURE_BLOCKED"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPlan(ctx, original.PlanID, "approved", "failed"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.RecoverySnapshot(ctx, original.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.RecoverOperation(ctx, RecoveryMutation{
		Expected: snapshot, Mode: "release-stale-locks",
		Reason: "terminal-operation-stale-lock", RecoveredAt: testTime.Add(2 * time.Minute),
	})
	if err != nil || after.OperationStatus != "blocked" || after.PlanStatus != "failed" ||
		after.Steps[0].Status != "blocked" || len(after.Locks) != 0 {
		t.Fatalf("after=%#v err=%v", after, err)
	}
}
