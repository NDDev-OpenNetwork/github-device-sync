package state

import (
	"context"
	"testing"
	"time"
)

func enablementFixture(plan PlanRecord) PlanEnablement {
	return PlanEnablement{
		EnablementID: "enablement_01KX7BV07RHD6KRA4Z4J0KCHGZ",
		PlanID:       plan.PlanID, PlanDigest: plan.PlanDigest,
		ApprovalID:     "approval_01KX7BV07RHD6KRA4Z4J0KCHGZ",
		ApprovalDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		DeviceID:       "device:test", SessionID: "session-test",
		CreatedAt: testTime.Add(-time.Minute), ExpiresAt: testTime.Add(time.Minute),
		MaximumStarts: 1, Status: "active",
	}
}

func bindEnablement(start *ApprovedOperationStart, value PlanEnablement) {
	start.Enablement = &EnablementConsumption{
		EnablementID: value.EnablementID, PlanDigest: value.PlanDigest,
		ApprovalID: value.ApprovalID, ApprovalDigest: value.ApprovalDigest,
		DeviceID: value.DeviceID, SessionID: value.SessionID, ConsumedAt: testTime,
	}
}

func TestPlanEnablementIsConsumedAtomicallyWithOperationStart(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	plan, start := approvedOperationStartFixture()
	if err := store.PutPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	enablement := enablementFixture(plan)
	if err := store.CreatePlanEnablement(ctx, enablement); err != nil {
		t.Fatal(err)
	}
	bindEnablement(&start, enablement)
	if _, err := store.StartApprovedOperation(ctx, start); err != nil {
		t.Fatal(err)
	}
	consumed, err := store.GetPlanEnablement(ctx, enablement.EnablementID)
	if err != nil || consumed.Status != "consumed" || consumed.Starts != 1 ||
		consumed.OperationID != start.Operation.OperationID || !consumed.ConsumedAt.Equal(testTime) {
		t.Fatalf("consumed=%#v err=%v", consumed, err)
	}
	if _, err := store.StartApprovedOperation(ctx, start); err == nil {
		t.Fatal("consumed enablement authorized a second operation start")
	}
}

func TestPlanEnablementMismatchAndExpiryRollBackPlanTransition(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ApprovedOperationStart, *PlanEnablement)
	}{
		{"wrong-plan-digest", func(start *ApprovedOperationStart, _ *PlanEnablement) {
			start.Enablement.PlanDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		}},
		{"wrong-approval", func(start *ApprovedOperationStart, _ *PlanEnablement) {
			start.Enablement.ApprovalID += "-other"
		}},
		{"wrong-device", func(start *ApprovedOperationStart, _ *PlanEnablement) {
			start.Enablement.DeviceID = "device:other"
		}},
		{"expired", func(start *ApprovedOperationStart, value *PlanEnablement) {
			start.Enablement.ConsumedAt = value.ExpiresAt
		}},
	}
	for _, item := range tests {
		item := item
		t.Run(item.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			plan, start := approvedOperationStartFixture()
			if err := store.PutPlan(ctx, plan); err != nil {
				t.Fatal(err)
			}
			enablement := enablementFixture(plan)
			if err := store.CreatePlanEnablement(ctx, enablement); err != nil {
				t.Fatal(err)
			}
			bindEnablement(&start, enablement)
			item.mutate(&start, &enablement)
			if _, err := store.StartApprovedOperation(ctx, start); err == nil {
				t.Fatal("invalid enablement authorized operation")
			}
			loadedPlan, err := store.GetPlan(ctx, plan.PlanID)
			if err != nil || loadedPlan.Status != "planned" {
				t.Fatalf("plan=%#v err=%v", loadedPlan, err)
			}
			loadedEnablement, err := store.GetPlanEnablement(ctx, enablement.EnablementID)
			if err != nil || loadedEnablement.Status != "active" || loadedEnablement.Starts != 0 {
				t.Fatalf("enablement=%#v err=%v", loadedEnablement, err)
			}
		})
	}
}

func TestPlanEnablementRollsBackWhenLaterStartBoundaryFails(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	plan, start := approvedOperationStartFixture()
	if err := store.PutPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	enablement := enablementFixture(plan)
	if err := store.CreatePlanEnablement(ctx, enablement); err != nil {
		t.Fatal(err)
	}
	bindEnablement(&start, enablement)
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_after_enablement
        BEFORE INSERT ON operations BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartApprovedOperation(ctx, start); err == nil {
		t.Fatal("injected start failure was accepted")
	}
	loaded, err := store.GetPlanEnablement(ctx, enablement.EnablementID)
	if err != nil || loaded.Status != "active" || loaded.Starts != 0 {
		t.Fatalf("enablement=%#v err=%v", loaded, err)
	}
}
