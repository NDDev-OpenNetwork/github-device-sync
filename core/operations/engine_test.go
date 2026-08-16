package operations

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	approvalcontract "github.com/NDDev-OpenNetwork/github-device-sync/core/approval"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type fakeChecker struct {
	observations map[string]Observation
	err          error
	calls        int
}

func TestApplySignedVerifiesAndConsumesExactPlanEnablement(t *testing.T) {
	engine, store, _, handler, plan := testEngine(t)
	now := engine.Now()
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	engine.RequireSignedApprovals = true
	verifier := approvalcontract.Verifier{Now: engine.Now, MaximumTTL: time.Hour, MaximumFuture: time.Minute,
		Trust: trust.Verifier{Policy: trust.Policy{SchemaVersion: 1, PolicyID: "approval-test", Identities: []trust.Identity{{
			ActorID: "owner:test", Roles: []string{"mutation-approver"}, Keys: []trust.Key{{Algorithm: trust.Ed25519,
				KeyID: "key-1", PublicKey: base64.RawURLEncoding.EncodeToString(public),
				ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), Status: "active"}},
		}}}}}
	engine.ApprovalVerifier = &verifier
	scope, err := approvalEvidence(plan, "approval:test")
	if err != nil {
		t.Fatal(err)
	}
	record := approvalcontract.Record{SchemaVersion: 1, ApprovalID: "approval_01K20J6M6E6M2YAHG8W0W8N4AN", PlanID: plan.PlanID,
		PlanDigest: plan.PlanDigest, ActorID: "owner:test", ActorType: "owner", IssuedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(10 * time.Minute), ApprovalClass: plan.ApprovalClass, ScopeDigest: scope.ScopeDigest}
	raw, _ := trust.SigningBytes(approvalcontract.SignatureDomain, record.Payload())
	record.Signature = trust.Signature{Algorithm: trust.Ed25519, KeyID: "key-1", Value: base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, raw))}
	if _, err := engine.ApplySigned(context.Background(), plan.PlanID, record); err == nil {
		t.Fatal("apply without separate enablement succeeded")
	} else if operationErr := new(Error); !errors.As(err, &operationErr) || operationErr.Code != "GDS_PLAN_ENABLEMENT_REQUIRED" {
		t.Fatalf("apply without separate enablement err=%v", err)
	}
	if _, err := engine.EnableSigned(context.Background(), plan.PlanID, record); err != nil {
		t.Fatal(err)
	}
	result, err := engine.ApplySigned(context.Background(), plan.PlanID, record)
	if err != nil || result.Status != "succeeded" || handler.applyCalls != 1 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, handler.applyCalls)
	}
	enablement, err := store.GetPlanEnablement(context.Background(), "enablement:"+record.ApprovalID)
	if err != nil || enablement.Status != "consumed" || enablement.OperationID != result.OperationID || enablement.Starts != 1 {
		t.Fatalf("enablement=%#v err=%v", enablement, err)
	}
	tampered := record
	tampered.PlanDigest = "sha256:tampered"
	if _, err := engine.ApplySigned(context.Background(), plan.PlanID, tampered); err == nil {
		t.Fatal("tampered signed approval was accepted")
	}
}

func (checker *fakeChecker) Observe(_ context.Context, repositoryID string) (Observation, error) {
	checker.calls++
	if checker.err != nil {
		return Observation{}, checker.err
	}
	return checker.observations[repositoryID], nil
}

type fakeHandler struct {
	applyCalls  int
	verifyCalls int
	applyErr    error
	verifyErr   error
	evidence    ApplyEvidence
	onApply     func(Step)
}

type safeFixtureError struct{}

func (safeFixtureError) Error() string { return "unsafe detail must not persist" }

func (safeFixtureError) SafeOperationFailureEvidence() map[string]any {
	return map[string]any{"provider": "fixture", "status_code": 503}
}

func (handler *fakeHandler) Apply(_ context.Context, step Step) (ApplyEvidence, error) {
	handler.applyCalls++
	if handler.onApply != nil {
		handler.onApply(step)
	}
	return handler.evidence, handler.applyErr
}

func (handler *fakeHandler) Verify(context.Context, Step, json.RawMessage) error {
	handler.verifyCalls++
	return handler.verifyErr
}

func testEngine(t *testing.T) (*Engine, *state.Store, *fakeChecker, *fakeHandler, Plan) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	plan, err := NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGV",
		created,
		created.Add(15*time.Minute),
		validPlanInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	expected := plan.Preconditions[0]
	checker := &fakeChecker{observations: map[string]Observation{
		expected.RepositoryID: {
			RepositoryID: expected.RepositoryID, HeadOID: expected.HeadOID,
			WorktreeFingerprint: expected.WorktreeFingerprint,
			IndexTreeOID:        expected.IndexTreeOID, UpstreamOID: expected.UpstreamOID,
			RemoteDefaultOID:     expected.RemoteDefaultOID,
			RemoteEvidenceDigest: expected.RemoteEvidenceDigest,
			ManifestDigest:       expected.ManifestDigest, PolicyDigest: expected.PolicyDigest,
		},
	}}
	handler := &fakeHandler{evidence: ApplyEvidence{
		Before: map[string]any{"value": "before"},
		After:  map[string]any{"value": "after"},
	}}
	counter := 0
	engine := &Engine{
		Store: store, Schemas: schemas, Checker: checker,
		Handlers: map[string]ActionHandler{"fixture-action": handler},
		Now:      func() time.Time { return created.Add(time.Minute) },
		NewID: func(prefix string, _ time.Time) (string, error) {
			counter++
			return fmt.Sprintf("%s_test_%d", prefix, counter), nil
		},
		DeviceID: "device:test", SessionID: "session-test", PID: 123,
		Lease: 5 * time.Minute,
	}
	store.SetNow(engine.Now)
	if err := engine.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	return engine, store, checker, handler, plan
}

func operationError(t *testing.T, err error, code string, class domain.ExitClass) *Error {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("expected operation error, got %T: %v", err, err)
	}
	if typed.Code != code || typed.Class != class {
		t.Fatalf("unexpected operation error: %+v", typed)
	}
	return typed
}

func TestApplyRequiresApprovalBeforeJournalOrHandler(t *testing.T) {
	engine, store, checker, handler, plan := testEngine(t)
	result, err := engine.Apply(context.Background(), plan.PlanID, "")
	operationError(t, err, "GDS_APPROVAL_REQUIRED", domain.ExitApproval)
	if result.Status != "planned" || checker.calls != 0 || handler.applyCalls != 0 {
		t.Fatalf("unexpected pre-approval activity: result=%+v checker=%d apply=%d", result, checker.calls, handler.applyCalls)
	}
	if _, err := store.GetOperationByPlan(context.Background(), plan.PlanID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("approval failure created an operation: %v", err)
	}
	record, err := store.GetPlan(context.Background(), plan.PlanID)
	if err != nil || record.Status != "planned" {
		t.Fatalf("plan state changed before approval: %+v %v", record, err)
	}
}

func TestApplyAutomaticallyCompensatesOnlyProvenSucceededSteps(t *testing.T) {
	engine, store, checker, _, base := testEngine(t)
	input := validPlanInput()
	input.Steps = []Step{
		{StepID: "step-001", RepositoryID: base.Preconditions[0].RepositoryID, Action: "create-value", RequiresApproval: true,
			Compensation: Compensation{Mode: "automatic", Action: "delete-value", Reversible: true, Idempotent: true}},
		{StepID: "step-002", RepositoryID: base.Preconditions[0].RepositoryID, Action: "fail-value", RequiresApproval: true,
			Compensation: Compensation{Mode: "manual"}},
	}
	plan, err := NewPlan("plan_01KX7BV07RHD6KRA4Z4J0KCHGW", base.CreatedAt, base.ExpiresAt, input)
	if err != nil {
		t.Fatal(err)
	}
	create := &fakeHandler{evidence: ApplyEvidence{Before: "absent", After: "present"}}
	remove := &fakeHandler{evidence: ApplyEvidence{Before: "present", After: "absent"}}
	failing := &fakeHandler{applyErr: errors.New("planned failure")}
	engine.Handlers = map[string]ActionHandler{"create-value": create, "delete-value": remove, "fail-value": failing}
	checker.observations[plan.Preconditions[0].RepositoryID] = checker.observations[base.Preconditions[0].RepositoryID]
	if err := engine.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:compensation")
	operationError(t, err, "GDS_OPERATION_STEP_FAILED", domain.ExitPartial)
	if result.Status != "partial" || create.applyCalls != 1 || remove.applyCalls != 1 || remove.verifyCalls != 1 || failing.applyCalls != 1 {
		t.Fatalf("result=%+v create=%d remove=%d/%d fail=%d", result, create.applyCalls, remove.applyCalls, remove.verifyCalls, failing.applyCalls)
	}
	steps, err := store.ListSteps(context.Background(), result.OperationID)
	if err != nil || len(steps) != 2 || steps[0].Status != "compensated" || steps[1].Status != "failed" {
		t.Fatalf("steps=%+v err=%v", steps, err)
	}
	events, err := store.ListEvents(context.Background(), result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.StepID == "step-001" && event.EventType == "step-compensated" {
			found = true
		}
	}
	if !found {
		t.Fatal("durable step-compensated event missing")
	}
}

func TestApplyRejectsStalePlanBeforeHandler(t *testing.T) {
	engine, store, checker, handler, plan := testEngine(t)
	observed := checker.observations[plan.Preconditions[0].RepositoryID]
	observed.HeadOID = "1111111111111111111111111111111111111111"
	checker.observations[observed.RepositoryID] = observed
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:1")
	typed := operationError(t, err, "GDS_STALE_PLAN", domain.ExitStale)
	if typed.MutationAttempted || result.MutationAttempted || handler.applyCalls != 0 {
		t.Fatalf("stale plan reached mutation handler: result=%+v calls=%d", result, handler.applyCalls)
	}
	operation, err := store.GetOperation(context.Background(), result.OperationID)
	if err != nil || operation.Status != "blocked" {
		t.Fatalf("stale operation not durably blocked: %+v %v", operation, err)
	}
	record, _ := store.GetPlan(context.Background(), plan.PlanID)
	if record.Status != "stale" {
		t.Fatalf("stale plan status = %s", record.Status)
	}
	steps, err := store.ListSteps(context.Background(), result.OperationID)
	if err != nil || len(steps) != 1 || steps[0].Status != "blocked" {
		t.Fatalf("stale steps not blocked: %+v %v", steps, err)
	}
	if _, err := store.GetLock(context.Background(), "repository", observed.RepositoryID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("stale operation retained lock: %v", err)
	}
}

func TestApplyRechecksEachRepositoryImmediatelyBeforeItsFirstMutation(t *testing.T) {
	engine, store, checker, handler, _ := testEngine(t)
	created := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	firstID := "repo_01JEXAMPZ0000000000000000C"
	secondID := "repo_01JEXAMPZ0000000000000000D"
	second := Precondition{
		RepositoryID:   secondID,
		HeadOID:        "1123456789abcdef0123456789abcdef01234567",
		ManifestDigest: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		PolicyDigest:   "sha256:3333333333333333333333333333333333333333333333333333333333333333",
	}
	input := validPlanInput()
	input.Preconditions = append(input.Preconditions, second)
	input.Steps = append(input.Steps, Step{
		StepID: "step-002", RepositoryID: secondID,
		Action: "fixture-action", RequiresApproval: true,
		Compensation: Compensation{Mode: "explicit-plan", Action: "fixture-restore"},
	})
	plan, err := NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGW",
		created, created.Add(15*time.Minute), input,
	)
	if err != nil {
		t.Fatal(err)
	}
	checker.observations[secondID] = Observation{
		RepositoryID: second.RepositoryID, HeadOID: second.HeadOID,
		ManifestDigest: second.ManifestDigest, PolicyDigest: second.PolicyDigest,
	}
	handler.onApply = func(step Step) {
		if step.RepositoryID != firstID {
			return
		}
		changed := checker.observations[secondID]
		changed.HeadOID = "ffffffffffffffffffffffffffffffffffffffff"
		checker.observations[secondID] = changed
	}
	if err := engine.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:multi-boundary")
	typed := operationError(t, err, "GDS_STALE_PLAN", domain.ExitPartial)
	if !typed.MutationAttempted || !result.MutationAttempted || result.Status != "partial" {
		t.Fatalf("cross-boundary stale classification = result=%+v error=%+v", result, typed)
	}
	if handler.applyCalls != 1 || handler.verifyCalls != 1 {
		t.Fatalf("stale second repository reached handler: apply=%d verify=%d", handler.applyCalls, handler.verifyCalls)
	}
	steps, err := store.ListSteps(context.Background(), result.OperationID)
	if err != nil || len(steps) != 2 || steps[0].Status != "succeeded" || steps[1].Status != "blocked" {
		t.Fatalf("unexpected durable step states: %+v %v", steps, err)
	}
	operation, err := store.GetOperation(context.Background(), result.OperationID)
	if err != nil || operation.Status != "partial" {
		t.Fatalf("operation not partial: %+v %v", operation, err)
	}
	record, err := store.GetPlan(context.Background(), plan.PlanID)
	if err != nil || record.Status != "partial" {
		t.Fatalf("plan not partial: %+v %v", record, err)
	}
}

func TestApplyVerifyAndReplayAreIdempotent(t *testing.T) {
	engine, store, _, handler, plan := testEngine(t)
	approval := "approval:owner:success"
	result, err := engine.Apply(context.Background(), plan.PlanID, approval)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || !result.MutationAttempted || !result.MutationCompleted {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	if handler.applyCalls != 1 || handler.verifyCalls != 1 {
		t.Fatalf("unexpected handler calls: apply=%d verify=%d", handler.applyCalls, handler.verifyCalls)
	}
	verified, err := engine.Verify(context.Background(), result.OperationID)
	if err != nil || verified.Status != "verified" || verified.Steps != 1 {
		t.Fatalf("verify failed: %+v %v", verified, err)
	}
	if handler.verifyCalls != 2 {
		t.Fatalf("explicit verify did not rerun verification: %d", handler.verifyCalls)
	}
	replay, err := engine.Apply(context.Background(), plan.PlanID, approval)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay || replay.OperationID != result.OperationID || handler.applyCalls != 1 {
		t.Fatalf("apply replay was not idempotent: %+v calls=%d", replay, handler.applyCalls)
	}
	events, err := store.ListEvents(context.Background(), result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), approval) || !strings.Contains(string(encoded), digestString(approval)) {
		t.Fatalf("approval reference was not reduced to a digest: %s", encoded)
	}
	if _, err := store.GetLock(context.Background(), "repository", plan.Scope.Repositories[0]); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("successful operation retained lock: %v", err)
	}
}

func TestApplyReplayPreservesNonSuccessOperationState(t *testing.T) {
	for _, test := range []struct {
		status          string
		code            string
		class           domain.ExitClass
		mutationAttempt bool
	}{
		{status: "applying", code: "GDS_OPERATION_REPLAY_BLOCKED", class: domain.ExitConflict},
		{status: "blocked", code: "GDS_OPERATION_REPLAY_BLOCKED", class: domain.ExitConflict},
		{status: "failed", code: "GDS_OPERATION_REPLAY_REQUIRES_RECOVERY", class: domain.ExitPartial, mutationAttempt: true},
		{status: "partial", code: "GDS_OPERATION_REPLAY_REQUIRES_RECOVERY", class: domain.ExitPartial, mutationAttempt: true},
	} {
		t.Run(test.status, func(t *testing.T) {
			engine, store, _, handler, plan := testEngine(t)
			operationID := "op_replay_" + test.status
			if err := store.StartOperation(context.Background(), state.OperationRecord{
				OperationID: operationID, PlanID: plan.PlanID, Operation: plan.Operation,
				Status: "applying", Actor: json.RawMessage(`{}`), StartedAt: engine.now(),
			}, nil); err != nil {
				t.Fatal(err)
			}
			if test.status != "applying" {
				if err := store.FinishOperation(
					context.Background(), operationID, "applying", test.status, engine.now(), map[string]any{},
				); err != nil {
					t.Fatal(err)
				}
			}

			result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:replay")
			operationError(t, err, test.code, test.class)
			if !result.IdempotentReplay || result.Status != test.status ||
				result.MutationAttempted != test.mutationAttempt || result.MutationCompleted ||
				handler.applyCalls != 0 {
				t.Fatalf("replay=%#v handler_calls=%d", result, handler.applyCalls)
			}
		})
	}
}

func TestHandlerFailurePreservesEvidenceAndReturnsPartial(t *testing.T) {
	engine, store, _, handler, plan := testEngine(t)
	secretError := "fixture mutation failed with token ghp_not-for-journal"
	handler.applyErr = errors.New(secretError)
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:partial")
	typed := operationError(t, err, "GDS_OPERATION_STEP_FAILED", domain.ExitPartial)
	if !typed.MutationAttempted || !result.MutationAttempted || result.MutationCompleted {
		t.Fatalf("partial mutation classification is wrong: %+v %+v", result, typed)
	}
	steps, err := store.ListSteps(context.Background(), result.OperationID)
	if err != nil || len(steps) != 1 || steps[0].Status != "failed" {
		t.Fatalf("failed step missing: %+v %v", steps, err)
	}
	if !strings.Contains(string(steps[0].Before), "before") || !strings.Contains(string(steps[0].After), "after") {
		t.Fatalf("partial evidence was lost: before=%s after=%s", steps[0].Before, steps[0].After)
	}
	operation, _ := store.GetOperation(context.Background(), result.OperationID)
	record, _ := store.GetPlan(context.Background(), plan.PlanID)
	if operation.Status != "partial" || record.Status != "partial" {
		t.Fatalf("partial status mismatch: operation=%s plan=%s", operation.Status, record.Status)
	}
	events, err := store.ListEvents(context.Background(), result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(struct {
		Steps  []state.StepRecord  `json:"steps"`
		Events []state.EventRecord `json:"events"`
		Result json.RawMessage     `json:"result"`
	}{steps, events, operation.Result})
	if strings.Contains(string(encoded), secretError) || strings.Contains(string(encoded), "ghp_not-for-journal") {
		t.Fatalf("raw handler error leaked into durable journal: %s", encoded)
	}
}

func TestHandlerFailurePersistsOnlyExplicitSafeEvidence(t *testing.T) {
	engine, store, _, handler, plan := testEngine(t)
	handler.applyErr = fmt.Errorf("wrapped: %w", safeFixtureError{})
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:safe-evidence")
	operationError(t, err, "GDS_OPERATION_STEP_FAILED", domain.ExitPartial)

	operation, getErr := store.GetOperation(context.Background(), result.OperationID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	encoded := string(operation.Result)
	if !strings.Contains(encoded, `"provider":"fixture"`) ||
		!strings.Contains(encoded, `"status_code":503`) ||
		strings.Contains(encoded, "unsafe detail") || strings.Contains(encoded, "wrapped") {
		t.Fatalf("unexpected durable failure evidence: %s", encoded)
	}
	events, listErr := store.ListEvents(context.Background(), result.OperationID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	eventRaw, _ := json.Marshal(events)
	if !strings.Contains(string(eventRaw), `"provider":"fixture"`) ||
		strings.Contains(string(eventRaw), "unsafe detail") {
		t.Fatalf("unexpected safe event evidence: %s", eventRaw)
	}
}

func TestApplyRetainsExactLocksWhenAtomicFinalizationRejectsState(t *testing.T) {
	engine, store, _, handler, plan := testEngine(t)
	var injected error
	handler.onApply = func(Step) {
		operation, err := store.GetOperationByPlan(context.Background(), plan.PlanID)
		if err != nil {
			injected = err
			return
		}
		_, injected = store.AcquireLock(context.Background(), state.Lock{
			Scope: "repository", ScopeID: "repo_unexpected",
			LockID: "lock_unexpected", OperationID: operation.OperationID,
			DeviceID: engine.DeviceID, SessionID: engine.SessionID, PID: engine.PID,
			AcquiredAt: engine.now(), LeaseExpiresAt: engine.now().Add(engine.Lease),
		})
	}

	result, err := engine.Apply(
		context.Background(), plan.PlanID, "approval:owner:finalization-fault",
	)
	typed := operationError(t, err, "GDS_OPERATION_FINALIZATION_FAILED", domain.ExitPartial)
	if injected != nil {
		t.Fatalf("inject extra operation lock: %v", injected)
	}
	if result.Status != "applying" || !result.MutationAttempted || result.MutationCompleted ||
		!typed.MutationAttempted {
		t.Fatalf("result=%#v error=%#v", result, typed)
	}
	operation, err := store.GetOperation(context.Background(), result.OperationID)
	if err != nil || operation.Status != "applying" {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
	planRecord, err := store.GetPlan(context.Background(), plan.PlanID)
	if err != nil || planRecord.Status != "applying" {
		t.Fatalf("plan=%#v err=%v", planRecord, err)
	}
	steps, err := store.ListSteps(context.Background(), result.OperationID)
	if err != nil || len(steps) != 1 || steps[0].Status != "succeeded" {
		t.Fatalf("steps=%#v err=%v", steps, err)
	}
	locks, err := store.ListLocksByOperation(context.Background(), result.OperationID)
	if err != nil || len(locks) != 2 {
		t.Fatalf("locks=%#v err=%v", locks, err)
	}
	events, err := store.ListEvents(context.Background(), result.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType == "operation-succeeded" {
			t.Fatalf("terminal event survived rejected finalization: %#v", events)
		}
	}
}

func TestVerifyRejectsInconsistentTerminalJournal(t *testing.T) {
	engine, store, _, handler, plan := testEngine(t)
	ctx := context.Background()
	operationID := "op_inconsistent_terminal"
	idempotencyKey, err := StepIdempotencyKey(plan, plan.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPlan(ctx, plan.PlanID, "planned", "approved"); err != nil {
		t.Fatal(err)
	}
	if err := store.StartOperation(ctx, state.OperationRecord{
		OperationID: operationID, PlanID: plan.PlanID, Operation: plan.Operation,
		Status: "applying", Actor: json.RawMessage(`{}`), StartedAt: engine.now(),
	}, []state.StepRecord{{
		OperationID: operationID, StepID: plan.Steps[0].StepID,
		RepositoryID: plan.Steps[0].RepositoryID, Action: plan.Steps[0].Action,
		IdempotencyKey: idempotencyKey,
		Sequence:       0, Status: "pending",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionStep(
		ctx, operationID, plan.Steps[0].StepID, "pending", "applying",
		engine.now(), nil, nil, "",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionStep(
		ctx, operationID, plan.Steps[0].StepID, "applying", "succeeded",
		engine.now(), nil, map[string]any{"after": true}, "",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOperation(
		ctx, operationID, "applying", "succeeded", engine.now(), map[string]any{},
	); err != nil {
		t.Fatal(err)
	}

	replay, err := engine.Apply(ctx, plan.PlanID, "approval:owner:inconsistent")
	operationError(t, err, "GDS_OPERATION_TERMINAL_STATE_INVALID", domain.ExitInternal)
	if !replay.IdempotentReplay || replay.OperationID != operationID || handler.applyCalls != 0 {
		t.Fatalf("inconsistent success replay=%#v handler_calls=%d", replay, handler.applyCalls)
	}
	_, err = engine.Verify(ctx, operationID)
	operationError(t, err, "GDS_OPERATION_TERMINAL_STATE_INVALID", domain.ExitInternal)
	if err := store.TransitionPlan(ctx, plan.PlanID, "approved", "succeeded"); err != nil {
		t.Fatal(err)
	}
	_, err = engine.Verify(ctx, operationID)
	operationError(t, err, "GDS_OPERATION_TERMINAL_EVENT_INVALID", domain.ExitInternal)
	if handler.verifyCalls != 0 {
		t.Fatalf("inconsistent terminal journal reached handler: %d", handler.verifyCalls)
	}
}

func TestHeldLockBlocksBeforeHandlerAndIsNotStolen(t *testing.T) {
	engine, store, _, handler, plan := testEngine(t)
	now := engine.now()
	existing, err := store.AcquireLock(context.Background(), state.Lock{
		Scope: "write-set", ScopeID: plan.Scope.Repositories[0] + ":repository", LockID: "lock_existing",
		OperationID: "op_existing", DeviceID: "device:other", SessionID: "session-other",
		PID: 456, AcquiredAt: now.Add(-10 * time.Minute), LeaseExpiresAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:locked")
	operationError(t, err, "GDS_REPOSITORY_LOCKED", domain.ExitConflict)
	if result.Status != "planned" || result.MutationAttempted || handler.applyCalls != 0 {
		t.Fatalf("held lock did not block mutation: %+v calls=%d", result, handler.applyCalls)
	}
	if _, err := store.GetOperationByPlan(context.Background(), plan.PlanID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("lock conflict created a partial operation: %v", err)
	}
	planRecord, err := store.GetPlan(context.Background(), plan.PlanID)
	if err != nil || planRecord.Status != "planned" {
		t.Fatalf("lock conflict changed plan state: %#v err=%v", planRecord, err)
	}
	observed, err := store.GetLock(context.Background(), existing.Scope, existing.ScopeID)
	if err != nil || observed.LockID != existing.LockID || observed.FencingToken != existing.FencingToken {
		t.Fatalf("expired lock was stolen: %+v %v", observed, err)
	}
}

func TestExpiredPlanBecomesStaleWithoutOperation(t *testing.T) {
	engine, store, _, handler, plan := testEngine(t)
	engine.Now = func() time.Time { return plan.ExpiresAt }
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:expired")
	operationError(t, err, "GDS_PLAN_EXPIRED", domain.ExitStale)
	if result.Status != "stale" || handler.applyCalls != 0 {
		t.Fatalf("expired plan reached handler: %+v calls=%d", result, handler.applyCalls)
	}
	if _, err := store.GetOperationByPlan(context.Background(), plan.PlanID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("expired plan created operation: %v", err)
	}
}
