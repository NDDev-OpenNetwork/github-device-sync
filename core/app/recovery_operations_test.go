package app

import (
	"os"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

func recoveryDecisionFixture(now time.Time) state.RecoverySnapshot {
	return state.RecoverySnapshot{
		OperationID: "op_fixture", OperationStatus: "applying",
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Steps:  []state.RecoveryStepEvidence{{StepID: "step-1", Status: "pending"}},
		Locks: []state.Lock{{
			Scope: "repository", ScopeID: "repo_fixture", LockID: "lock_fixture",
			OperationID: "op_fixture", DeviceID: "device:test", PID: 2147483647,
			LeaseExpiresAt: now.Add(-time.Minute),
		}},
	}
}

func TestRecoveryDecisionAllowsOnlyExpiredDeadLocalOwner(t *testing.T) {
	now := time.Now().UTC()
	snapshot := recoveryDecisionFixture(now)
	plan := operations.Plan{Steps: []operations.Step{{
		StepID: "step-1", Compensation: operations.Compensation{Mode: "manual"},
	}}, Scope: operations.Scope{Repositories: []string{"repo_fixture"}}}
	decision := decideRecovery(snapshot, plan, now, "device:test")
	if !decision.Automatable || decision.Classification != "safe-abort" ||
		decision.Mode != "abort-interrupted" || decision.DecisionDigest == "" {
		t.Fatalf("decision=%#v", decision)
	}

	snapshot.Locks[0].PID = os.Getpid()
	active := decideRecovery(snapshot, plan, now, "device:test")
	if active.Automatable || !containsString(active.Blockers, "lock-owner-process-alive") {
		t.Fatalf("active decision=%#v", active)
	}

	snapshot = recoveryDecisionFixture(now)
	remote := decideRecovery(snapshot, plan, now, "device:other")
	if remote.Automatable || !containsString(remote.Blockers, "lock-owner-other-device") {
		t.Fatalf("remote decision=%#v", remote)
	}

	snapshot = recoveryDecisionFixture(now)
	snapshot.Steps[0].Status = "applying"
	unknown := decideRecovery(snapshot, plan, now, "device:test")
	if unknown.Automatable || !containsString(unknown.Blockers, "unknown-step-side-effects") {
		t.Fatalf("unknown decision=%#v", unknown)
	}
}

func TestRecoveryDecisionReportsCompensationWithoutApplyingIt(t *testing.T) {
	now := time.Now().UTC()
	snapshot := recoveryDecisionFixture(now)
	snapshot.Steps[0].Status = "succeeded"
	plan := operations.Plan{Steps: []operations.Step{{
		StepID: "step-1",
		Compensation: operations.Compensation{
			Mode: "explicit-plan", Action: "restore-fixture-state",
		},
	}}, Scope: operations.Scope{Repositories: []string{"repo_fixture"}}}
	decision := decideRecovery(snapshot, plan, now, "device:test")
	if !decision.Automatable || decision.Classification != "safe-close-partial" ||
		len(decision.Compensations) != 1 ||
		decision.Compensations[0].Status != "requires-new-approved-plan" ||
		decision.Compensations[0].Action != "restore-fixture-state" {
		t.Fatalf("decision=%#v", decision)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
