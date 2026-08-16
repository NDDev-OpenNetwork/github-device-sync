package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var testTime = time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Initialize(
		context.Background(), filepath.Join(t.TempDir(), "gds-state", "state.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return testTime }
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testPlanRecord() PlanRecord {
	body := json.RawMessage(`{"plan_id":"plan_01KX7BV07RHD6KRA4Z4J0KCHGV"}`)
	return PlanRecord{
		PlanID: "plan_01KX7BV07RHD6KRA4Z4J0KCHGV", Operation: "fixture-operation",
		PlanDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Body:       body, Status: "planned", CreatedAt: testTime,
		ExpiresAt: testTime.Add(15 * time.Minute), InsertedAt: testTime,
	}
}

func TestPlanStorageIsIdempotentAndImmutable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	record := testPlanRecord()
	if err := store.PutPlan(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPlan(ctx, record); err != nil {
		t.Fatalf("idempotent insert failed: %v", err)
	}
	conflict := record
	conflict.Body = json.RawMessage(`{"different":true}`)
	if err := store.PutPlan(ctx, conflict); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("conflicting plan error = %v, want ErrPlanConflict", err)
	}
	if _, err := store.db.ExecContext(
		ctx, `UPDATE plans SET body_json = '{"tampered":true}' WHERE plan_id = ?`, record.PlanID,
	); err == nil {
		t.Fatal("immutable plan trigger allowed body update")
	}
	if err := store.TransitionPlan(ctx, record.PlanID, "planned", "approved"); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPlan(ctx, record.PlanID, "planned", "applying"); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale plan transition error = %v, want ErrStateConflict", err)
	}
}

func TestJournalIsDurableOrderedAndAppendOnly(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	plan := testPlanRecord()
	if err := store.PutPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	operation := OperationRecord{
		OperationID: "op_01KX7BV07RHD6KRA4Z4J0KCHGV", PlanID: plan.PlanID,
		Operation: plan.Operation, Status: "applying",
		Actor:     json.RawMessage(`{"type":"agent-session","session_id":"test"}`),
		StartedAt: testTime,
	}
	steps := []StepRecord{{
		OperationID: operation.OperationID, StepID: "step-001",
		RepositoryID:   "repo_01JEXAMPZ0000000000000000C",
		Action:         "fixture-action",
		IdempotencyKey: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Sequence:       0, Status: "pending",
	}}
	if err := store.StartOperation(ctx, operation, steps); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionStep(
		ctx, operation.OperationID, "step-001", "pending", "applying",
		testTime.Add(time.Second), map[string]any{"head": "before"}, nil, "",
	); err != nil {
		t.Fatal(err)
	}
	event, err := store.AppendEvent(
		ctx, operation.OperationID, plan.PlanID, "step-001", "step-applied",
		testTime.Add(2*time.Second), map[string]any{"result": "ok"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if event.Sequence != 2 {
		t.Fatalf("event sequence = %d, want 2", event.Sequence)
	}
	if err := store.TransitionStep(
		ctx, operation.OperationID, "step-001", "applying", "succeeded",
		testTime.Add(3*time.Second), nil, map[string]any{"head": "after"}, "",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOperation(
		ctx, operation.OperationID, "applying", "succeeded", testTime.Add(4*time.Second),
		map[string]any{"verified": true},
	); err != nil {
		t.Fatal(err)
	}
	events, err := store.ListEvents(ctx, operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventType != "operation-started" ||
		events[1].EventType != "step-applied" {
		t.Fatalf("unexpected events: %+v", events)
	}
	if _, err := store.db.ExecContext(
		ctx, `UPDATE operation_events SET event_type = 'tampered' WHERE sequence = ?`, event.Sequence,
	); err == nil {
		t.Fatal("append-only journal allowed update")
	}
	if _, err := store.db.ExecContext(
		ctx, `DELETE FROM operation_events WHERE sequence = ?`, event.Sequence,
	); err == nil {
		t.Fatal("append-only journal allowed delete")
	}
	loaded, err := store.GetOperation(ctx, operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "succeeded" || loaded.FinishedAt == nil {
		t.Fatalf("unexpected loaded operation: %+v", loaded)
	}
}

func TestExpiredLockIsNotStolenAndFencingTokenIncreases(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first := Lock{
		Scope: "repository", ScopeID: "repo_01JEXAMPZ0000000000000000C",
		LockID:      "lock_01KX7BV07RHD6KRA4Z4J0KCHGV",
		OperationID: "op_01KX7BV07RHD6KRA4Z4J0KCHGV",
		DeviceID:    "device:test", SessionID: "session-one", PID: 100,
		AcquiredAt: testTime, LeaseExpiresAt: testTime.Add(time.Second),
	}
	acquired, err := store.AcquireLock(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.LockID = "lock_01KX7BV07RHD6KRA4Z4J0KCHGW"
	second.OperationID = "op_01KX7BV07RHD6KRA4Z4J0KCHGW"
	second.SessionID = "session-two"
	second.AcquiredAt = testTime.Add(time.Minute)
	second.LeaseExpiresAt = second.AcquiredAt.Add(time.Minute)
	if _, err := store.AcquireLock(ctx, second); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expired lock acquisition error = %v, want ErrLockHeld", err)
	}
	wrong := acquired
	wrong.FencingToken++
	if err := store.HeartbeatLock(
		ctx, wrong, testTime.Add(2*time.Minute), testTime.Add(3*time.Minute),
	); !errors.Is(err, ErrLockOwnership) {
		t.Fatalf("wrong heartbeat error = %v, want ErrLockOwnership", err)
	}
	if err := store.ReleaseLock(ctx, acquired); err != nil {
		t.Fatal(err)
	}
	secondAcquired, err := store.AcquireLock(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if secondAcquired.FencingToken <= acquired.FencingToken {
		t.Fatalf("fencing token did not increase: %d -> %d", acquired.FencingToken, secondAcquired.FencingToken)
	}
}

func TestExpiredLeaseBlocksNewAcquireUntilExplicitRelease(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first := Lock{
		Scope: "repository", ScopeID: "repo_stale_lease_test",
		LockID: "lock_stale", OperationID: "op_stale",
		DeviceID: "device:test", SessionID: "session-stale", PID: 100,
		AcquiredAt: testTime, LeaseExpiresAt: testTime.Add(time.Second),
	}
	acquired, err := store.AcquireLock(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	store.SetNow(func() time.Time { return testTime.Add(2 * time.Second) })
	second := first
	second.LockID = "lock_fresh"
	second.OperationID = "op_fresh"
	second.SessionID = "session-fresh"
	second.AcquiredAt = testTime.Add(2 * time.Second)
	second.LeaseExpiresAt = second.AcquiredAt.Add(time.Minute)
	if _, err := store.AcquireLock(ctx, second); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expired lock should block acquisition (no auto-reclaim): got %v, want ErrLockHeld", err)
	}
	if err := store.ReleaseLock(ctx, acquired); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLock(ctx, second); err != nil {
		t.Fatalf("acquire after explicit release failed: %v", err)
	}
}

func TestHeartbeatRejectsLeaseExceedingCeiling(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	lock := Lock{
		Scope: "repository", ScopeID: "repo_heartbeat_ceiling",
		LockID: "lock_hb", OperationID: "op_hb",
		DeviceID: "device:test", SessionID: "session-hb", PID: 100,
		AcquiredAt: testTime, LeaseExpiresAt: testTime.Add(5 * time.Minute),
	}
	acquired, err := store.AcquireLock(ctx, lock)
	if err != nil {
		t.Fatal(err)
	}
	err = store.HeartbeatLock(ctx, acquired, testTime.Add(time.Minute), testTime.Add(2*time.Hour))
	if err == nil {
		t.Fatal("heartbeat with 2h lease should have been rejected (ceiling is 1h)")
	}
}

func TestConcurrentLockAcquisitionHasOneWinner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const contenders = 8
	var wait sync.WaitGroup
	wait.Add(contenders)
	results := make(chan error, contenders)
	for index := 0; index < contenders; index++ {
		go func(index int) {
			defer wait.Done()
			_, err := store.AcquireLock(ctx, Lock{
				Scope: "repository", ScopeID: "repo_01JEXAMPZ0000000000000000C",
				LockID:      fmt.Sprintf("lock_01KX7BV07RHD6KRA4Z4J0KCHG%d", index),
				OperationID: fmt.Sprintf("op_01KX7BV07RHD6KRA4Z4J0KCHG%d", index),
				DeviceID:    "device:test", SessionID: fmt.Sprintf("session-%d", index), PID: 100 + index,
				AcquiredAt: testTime, LeaseExpiresAt: testTime.Add(time.Minute),
			})
			results <- err
		}(index)
	}
	wait.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrLockHeld) {
			t.Fatalf("unexpected lock error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("lock winners = %d, want 1", winners)
	}
}
