package state

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func approvedOperationStartFixture() (PlanRecord, ApprovedOperationStart) {
	plan := testPlanRecord()
	operationID := "op_01KX7BV07RHD6KRA4Z4J0KCHGZ"
	return plan, ApprovedOperationStart{
		Operation: OperationRecord{
			OperationID: operationID, PlanID: plan.PlanID, Operation: plan.Operation,
			Status: "applying", Actor: json.RawMessage(`{"type":"agent-session"}`),
			StartedAt: testTime,
		},
		Steps: []StepRecord{{
			OperationID: operationID, StepID: "step-001",
			RepositoryID: "repo_01JEXAMPZ0000000000000000C", Action: "fixture-action",
			IdempotencyKey: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Sequence:       0, Status: "pending",
		}},
		Locks: []Lock{{
			Scope: "repository", ScopeID: "repo_01JEXAMPZ0000000000000000C",
			LockID: "lock_01KX7BV07RHD6KRA4Z4J0KCHGZ", OperationID: operationID,
			DeviceID: "device:test", SessionID: "session-test", PID: 123,
			AcquiredAt: testTime, LeaseExpiresAt: testTime.Add(5 * time.Minute),
		}},
		Approval: &OperationEvent{
			EventType: "approval-recorded", OccurredAt: testTime,
			Payload: map[string]any{"reference_digest": "sha256:approval"},
		},
	}
}

func startApprovedOperationFixture(t *testing.T) (*Store, PlanRecord, ApprovedOperationStart, []Lock) {
	t.Helper()
	store := newTestStore(t)
	plan, start := approvedOperationStartFixture()
	if err := store.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	locks, err := store.StartApprovedOperation(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	return store, plan, start, locks
}

func TestStartApprovedOperationCommitsCompleteRecoverableState(t *testing.T) {
	store, plan, start, locks := startApprovedOperationFixture(t)
	ctx := context.Background()

	loadedPlan, err := store.GetPlan(ctx, plan.PlanID)
	if err != nil || loadedPlan.Status != "approved" {
		t.Fatalf("plan=%#v err=%v", loadedPlan, err)
	}
	operation, err := store.GetOperation(ctx, start.Operation.OperationID)
	if err != nil || operation.Status != "applying" {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
	steps, err := store.ListSteps(ctx, start.Operation.OperationID)
	if err != nil || len(steps) != 1 || steps[0].Status != "pending" {
		t.Fatalf("steps=%#v err=%v", steps, err)
	}
	events, err := store.ListEvents(ctx, start.Operation.OperationID)
	if err != nil || len(events) != 2 || events[0].EventType != "operation-started" ||
		events[1].EventType != "approval-recorded" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if len(locks) != 1 || locks[0].FencingToken <= 0 ||
		!locks[0].HeartbeatAt.Equal(locks[0].AcquiredAt) {
		t.Fatalf("locks=%#v", locks)
	}
	durableLocks, err := store.ListLocksByOperation(ctx, start.Operation.OperationID)
	if err != nil || len(durableLocks) != 1 || durableLocks[0].FencingToken != locks[0].FencingToken {
		t.Fatalf("durable locks=%#v err=%v", durableLocks, err)
	}
}

func TestStartApprovedOperationRollsBackEveryBoundary(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{
			name: "operation journal",
			trigger: `CREATE TRIGGER fail_operation_start BEFORE INSERT ON operations
                BEGIN SELECT RAISE(ABORT, 'injected operation failure'); END`,
		},
		{
			name: "approval event",
			trigger: `CREATE TRIGGER fail_approval_event BEFORE INSERT ON operation_events
                WHEN NEW.event_type = 'approval-recorded'
                BEGIN SELECT RAISE(ABORT, 'injected approval failure'); END`,
		},
		{
			name: "lock insert",
			trigger: `CREATE TRIGGER fail_operation_lock BEFORE INSERT ON locks
                BEGIN SELECT RAISE(ABORT, 'injected lock failure'); END`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			plan, start := approvedOperationStartFixture()
			if err := store.PutPlan(ctx, plan); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, test.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := store.StartApprovedOperation(ctx, start); err == nil {
				t.Fatal("injected start failure was accepted")
			}
			assertNoApprovedOperationState(t, store, plan, start.Operation.OperationID)
		})
	}
}

func TestStartApprovedOperationLockConflictLeavesPlanReusable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	plan, start := approvedOperationStartFixture()
	if err := store.PutPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	competing, err := store.AcquireLock(ctx, Lock{
		Scope: start.Locks[0].Scope, ScopeID: start.Locks[0].ScopeID,
		LockID: "lock_competing", OperationID: "op_competing",
		DeviceID: "device:other", SessionID: "session-other", PID: 456,
		AcquiredAt: testTime, LeaseExpiresAt: testTime.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartApprovedOperation(ctx, start); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("error=%v, want ErrLockHeld", err)
	}
	assertNoApprovedOperationState(t, store, plan, start.Operation.OperationID)
	loaded, err := store.GetLock(ctx, competing.Scope, competing.ScopeID)
	if err != nil || loaded.LockID != competing.LockID {
		t.Fatalf("competing lock=%#v err=%v", loaded, err)
	}
}

func TestConcurrentApprovedOperationsOnSameScopeReturnDeterministicLockConflict(t *testing.T) {
	store := newTestStore(t)
	peer, err := Open(context.Background(), store.Path())
	if err != nil {
		t.Fatal(err)
	}
	peer.SetNow(func() time.Time { return testTime })
	t.Cleanup(func() { _ = peer.Close() })

	firstPlan, firstStart := approvedOperationStartFixture()
	secondPlan, secondStart := approvedOperationStartFixture()
	secondPlan.PlanID = "plan_01KX7BV07RHD6KRA4Z4J0KCHGW"
	secondPlan.PlanDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	secondPlan.Body = json.RawMessage(`{"plan_id":"plan_01KX7BV07RHD6KRA4Z4J0KCHGW"}`)
	secondStart.Operation.PlanID = secondPlan.PlanID
	secondStart.Operation.OperationID = "op_01KX7BV07RHD6KRA4Z4J0KCHGW"
	secondStart.Steps[0].OperationID = secondStart.Operation.OperationID
	secondStart.Steps[0].IdempotencyKey =
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	secondStart.Locks[0].OperationID = secondStart.Operation.OperationID
	secondStart.Locks[0].LockID = "lock_01KX7BV07RHD6KRA4Z4J0KCHGW"

	ctx := context.Background()
	for _, plan := range []PlanRecord{firstPlan, secondPlan} {
		if err := store.PutPlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	type startResult struct {
		planID      string
		operationID string
		err         error
	}
	ready := make(chan struct{})
	results := make(chan startResult, 2)
	for index, candidate := range []struct {
		store *Store
		start ApprovedOperationStart
	}{
		{store: store, start: firstStart},
		{store: peer, start: secondStart},
	} {
		go func(index int, candidateStore *Store, start ApprovedOperationStart) {
			<-ready
			_, startErr := candidateStore.StartApprovedOperation(ctx, start)
			results <- startResult{
				planID: start.Operation.PlanID, operationID: start.Operation.OperationID,
				err: startErr,
			}
		}(index, candidate.store, candidate.start)
	}
	close(ready)

	var winner, loser startResult
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			if winner.planID != "" {
				t.Fatalf("multiple operation starts succeeded: %#v and %#v", winner, result)
			}
			winner = result
		case errors.Is(result.err, ErrLockHeld):
			loser = result
		default:
			t.Fatalf("unexpected concurrent start error: %v", result.err)
		}
	}
	if winner.planID == "" || loser.planID == "" {
		t.Fatalf("winner=%#v loser=%#v", winner, loser)
	}
	loserPlan, err := store.GetPlan(ctx, loser.planID)
	if err != nil || loserPlan.Status != "planned" {
		t.Fatalf("loser plan=%#v err=%v", loserPlan, err)
	}
	if _, err := store.GetOperation(ctx, loser.operationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("loser operation error=%v, want ErrNotFound", err)
	}
}

func TestFinalizeOperationCommitsTerminalJournalAndExactLockRelease(t *testing.T) {
	store, plan, start, locks := startApprovedOperationFixture(t)
	ctx := context.Background()
	if err := store.TransitionPlan(ctx, plan.PlanID, "approved", "applying"); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionStep(
		ctx, start.Operation.OperationID, "step-001", "pending", "applying",
		testTime.Add(time.Second), nil, nil, "",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionStep(
		ctx, start.Operation.OperationID, "step-001", "applying", "succeeded",
		testTime.Add(2*time.Second), map[string]any{"before": true},
		map[string]any{"after": true}, "",
	); err != nil {
		t.Fatal(err)
	}
	finalization := successfulFinalization(plan, start, locks)
	if err := store.FinalizeOperation(ctx, finalization); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.RecoverySnapshot(ctx, start.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.OperationStatus != "succeeded" || snapshot.PlanStatus != "succeeded" ||
		len(snapshot.Locks) != 0 ||
		snapshot.Events[len(snapshot.Events)-1].EventType != "operation-succeeded" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if err := store.FinalizeOperation(ctx, finalization); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("second finalization error=%v, want ErrStateConflict", err)
	}
}

func TestFinalizeOperationRollsBackEveryTerminalBoundary(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{
			name: "operation status",
			trigger: `CREATE TRIGGER fail_terminal_operation BEFORE UPDATE OF status ON operations
                WHEN NEW.status = 'succeeded'
                BEGIN SELECT RAISE(ABORT, 'injected operation terminal failure'); END`,
		},
		{
			name: "plan status",
			trigger: `CREATE TRIGGER fail_terminal_plan BEFORE UPDATE OF status ON plans
                WHEN NEW.status = 'succeeded'
                BEGIN SELECT RAISE(ABORT, 'injected plan terminal failure'); END`,
		},
		{
			name: "terminal event",
			trigger: `CREATE TRIGGER fail_terminal_event BEFORE INSERT ON operation_events
                WHEN NEW.event_type = 'operation-succeeded'
                BEGIN SELECT RAISE(ABORT, 'injected terminal event failure'); END`,
		},
		{
			name: "lock release",
			trigger: `CREATE TRIGGER fail_terminal_unlock BEFORE DELETE ON locks
                BEGIN SELECT RAISE(ABORT, 'injected terminal unlock failure'); END`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, plan, start, locks := startApprovedOperationFixture(t)
			ctx := context.Background()
			if err := store.TransitionPlan(ctx, plan.PlanID, "approved", "applying"); err != nil {
				t.Fatal(err)
			}
			if err := store.TransitionStep(
				ctx, start.Operation.OperationID, "step-001", "pending", "applying",
				testTime.Add(time.Second), nil, nil, "",
			); err != nil {
				t.Fatal(err)
			}
			if err := store.TransitionStep(
				ctx, start.Operation.OperationID, "step-001", "applying", "succeeded",
				testTime.Add(2*time.Second), nil, map[string]any{"after": true}, "",
			); err != nil {
				t.Fatal(err)
			}
			before, err := store.RecoverySnapshot(ctx, start.Operation.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, test.trigger); err != nil {
				t.Fatal(err)
			}
			if err := store.FinalizeOperation(ctx, successfulFinalization(plan, start, locks)); err == nil {
				t.Fatal("injected finalization failure was accepted")
			}
			after, err := store.RecoverySnapshot(ctx, start.Operation.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Digest != before.Digest || len(after.Locks) != 1 ||
				after.OperationStatus != "applying" || after.PlanStatus != "applying" {
				t.Fatalf("transaction was not rolled back: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestFinalizeOperationRejectsInexactLockEvidenceWithoutMutation(t *testing.T) {
	store, plan, start, locks := startApprovedOperationFixture(t)
	ctx := context.Background()
	if err := store.TransitionPlan(ctx, plan.PlanID, "approved", "applying"); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionStep(
		ctx, start.Operation.OperationID, "step-001", "pending", "applying",
		testTime.Add(time.Second), nil, nil, "",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionStep(
		ctx, start.Operation.OperationID, "step-001", "applying", "succeeded",
		testTime.Add(2*time.Second), nil, map[string]any{"after": true}, "",
	); err != nil {
		t.Fatal(err)
	}
	before, err := store.RecoverySnapshot(ctx, start.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	wrong := append([]Lock(nil), locks...)
	wrong[0].FencingToken++
	if err := store.FinalizeOperation(ctx, successfulFinalization(plan, start, wrong)); !errors.Is(err, ErrLockOwnership) {
		t.Fatalf("error=%v, want ErrLockOwnership", err)
	}
	after, err := store.RecoverySnapshot(ctx, start.Operation.OperationID)
	if err != nil || after.Digest != before.Digest {
		t.Fatalf("state changed after inexact finalization: %#v err=%v", after, err)
	}
}

func successfulFinalization(
	plan PlanRecord,
	start ApprovedOperationStart,
	locks []Lock,
) OperationFinalization {
	return OperationFinalization{
		OperationID: start.Operation.OperationID, PlanID: plan.PlanID,
		ExpectedOperationStatus: "applying", ExpectedPlanStatus: "applying",
		OperationStatus: "succeeded", PlanStatus: "succeeded",
		FinishedAt: testTime.Add(3 * time.Second),
		Result:     map[string]any{"steps_succeeded": 1}, Locks: locks,
	}
}

func assertNoApprovedOperationState(
	t *testing.T,
	store *Store,
	plan PlanRecord,
	operationID string,
) {
	t.Helper()
	ctx := context.Background()
	loadedPlan, err := store.GetPlan(ctx, plan.PlanID)
	if err != nil || loadedPlan.Status != "planned" {
		t.Fatalf("plan=%#v err=%v", loadedPlan, err)
	}
	if _, err := store.GetOperation(ctx, operationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("operation lookup error=%v, want ErrNotFound", err)
	}
	locks, err := store.ListLocksByOperation(ctx, operationID)
	if err != nil || len(locks) != 0 {
		t.Fatalf("locks=%#v err=%v", locks, err)
	}
	summary, err := store.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Operations != 0 || summary.Steps != 0 || summary.Events != 0 {
		t.Fatalf("partial start state persisted: %#v", summary)
	}
}
