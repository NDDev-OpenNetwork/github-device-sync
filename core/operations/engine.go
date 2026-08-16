package operations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	approvalcontract "github.com/NDDev-OpenNetwork/github-device-sync/core/approval"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/telemetry"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type Observation struct {
	RepositoryID         string `json:"repository_id"`
	HeadOID              string `json:"head_oid"`
	WorktreeFingerprint  string `json:"worktree_fingerprint,omitempty"`
	IndexTreeOID         string `json:"index_tree_oid,omitempty"`
	UpstreamOID          string `json:"upstream_oid,omitempty"`
	RemoteDefaultOID     string `json:"remote_default_oid,omitempty"`
	RemoteEvidenceDigest string `json:"remote_evidence_digest,omitempty"`
	ManifestDigest       string `json:"manifest_digest"`
	PolicyDigest         string `json:"policy_digest"`
}

type PreconditionChecker interface {
	Observe(context.Context, string) (Observation, error)
}

type ApplyEvidence struct {
	Before any `json:"before,omitempty"`
	After  any `json:"after,omitempty"`
}

type ActionHandler interface {
	// ActionHandler applies and verifies one operation step type. Apply must be
	// externally idempotent under the step's stable parameters: the engine will
	// not call Apply twice for one operation/step, but recovery of a partial
	// operation proceeds by filing a new approved plan, which will invoke Apply
	// again against repositories the failed operation may have already mutated.
	Apply(context.Context, Step) (ApplyEvidence, error)
	Verify(context.Context, Step, json.RawMessage) error
}

type IDGenerator func(prefix string, now time.Time) (string, error)

type Engine struct {
	Store                  *state.Store
	Schemas                *validation.Set
	Checker                PreconditionChecker
	Handlers               map[string]ActionHandler
	Now                    func() time.Time
	NewID                  IDGenerator
	DeviceID               string
	SessionID              string
	PID                    int
	Lease                  time.Duration
	LockScope              string
	KillSwitches           KillSwitches
	KillSwitchError        error
	ApprovalVerifier       *approvalcontract.Verifier
	RequireSignedApprovals bool
	Telemetry              *telemetry.Exporter
}

type ApplyResult struct {
	PlanID            string            `json:"plan_id"`
	OperationID       string            `json:"operation_id,omitempty"`
	Status            string            `json:"status"`
	MutationAttempted bool              `json:"mutation_attempted"`
	MutationCompleted bool              `json:"mutation_completed"`
	IdempotentReplay  bool              `json:"idempotent_replay"`
	KillSwitches      KillSwitches      `json:"kill_switches"`
	Approval          *ApprovalEvidence `json:"approval,omitempty"`
}

type VerifyResult struct {
	PlanID       string       `json:"plan_id"`
	OperationID  string       `json:"operation_id"`
	Status       string       `json:"status"`
	Steps        int          `json:"steps"`
	KillSwitches KillSwitches `json:"kill_switches"`
}

func (engine *Engine) PutPlan(ctx context.Context, plan Plan) error {
	if err := engine.validateConfiguration(false); err != nil {
		return err
	}
	if findings := plan.Validate(engine.Schemas); len(findings) != 0 {
		return planValidationError(
			"GDS_PLAN_INVALID", "Plan failed schema or semantic validation.", findings,
		)
	}
	body, err := plan.Marshal()
	if err != nil {
		return err
	}
	return engine.Store.PutPlan(ctx, state.PlanRecord{
		PlanID: plan.PlanID, Operation: plan.Operation, PlanDigest: plan.PlanDigest,
		Body: body, Status: "planned", CreatedAt: plan.CreatedAt,
		ExpiresAt: plan.ExpiresAt, InsertedAt: engine.now(),
	})
}

func (engine *Engine) Apply(
	ctx context.Context,
	planID string,
	approvalReference string,
) (result ApplyResult, returnError error) {
	if engine.RequireSignedApprovals && approvalReference != "" {
		record, err := loadSignedApprovalFile(approvalReference)
		if err != nil {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_SIGNED_APPROVAL_READ_FAILED", domain.ExitApproval,
				"Approval input must be a bounded regular JSON file containing a signed exact-plan record.", err,
			)
		}
		return engine.apply(ctx, planID, record.ExternalReference, &record)
	}
	return engine.apply(ctx, planID, approvalReference, nil)
}

func loadSignedApprovalFile(path string) (approvalcontract.Record, error) {
	return approvalcontract.LoadRecord(path)
}

// ApplySigned is the normative mutation entrypoint. The separate exact-plan
// enablement must already exist and is consumed atomically with operation start.
func (engine *Engine) ApplySigned(ctx context.Context, planID string, signed approvalcontract.Record) (ApplyResult, error) {
	return engine.apply(ctx, planID, signed.ExternalReference, &signed)
}

// EnableSigned creates the separate transparent one-shot local mutation gate.
// ApplySigned never creates this record implicitly.
func (engine *Engine) EnableSigned(ctx context.Context, planID string, signed approvalcontract.Record) (state.PlanEnablement, error) {
	if err := engine.validateConfiguration(false); err != nil {
		return state.PlanEnablement{}, err
	}
	_, plan, err := engine.loadPlan(ctx, planID)
	if err != nil {
		return state.PlanEnablement{}, err
	}
	if engine.ApprovalVerifier == nil {
		return state.PlanEnablement{}, newError("GDS_APPROVAL_VERIFIER_UNAVAILABLE", domain.ExitSecurity, "Signed approval trust policy is unavailable.", nil)
	}
	scope, err := approvalEvidence(plan, signed.ApprovalID)
	if err != nil {
		return state.PlanEnablement{}, err
	}
	if err := engine.ApprovalVerifier.Verify(signed, approvalcontract.Expectation{PlanID: plan.PlanID,
		PlanDigest: plan.PlanDigest, ApprovalClass: plan.ApprovalClass, ScopeDigest: scope.ScopeDigest,
		RequiredRole: "mutation-approver"}); err != nil {
		return state.PlanEnablement{}, newError("GDS_APPROVAL_SIGNATURE_INVALID", domain.ExitApproval, "Signed approval does not authorize the exact immutable plan.", err)
	}
	digest, err := signed.Digest()
	if err != nil {
		return state.PlanEnablement{}, err
	}
	now := engine.now()
	expires := signed.ExpiresAt
	if plan.ExpiresAt.Before(expires) {
		expires = plan.ExpiresAt
	}
	value := state.PlanEnablement{EnablementID: "enablement:" + signed.ApprovalID, PlanID: plan.PlanID,
		PlanDigest: plan.PlanDigest, ApprovalID: signed.ApprovalID, ApprovalDigest: digest,
		DeviceID: engine.DeviceID, SessionID: engine.SessionID, CreatedAt: now,
		ExpiresAt: expires, MaximumStarts: 1, Status: "active"}
	if err := engine.Store.CreatePlanEnablement(ctx, value); err != nil {
		return state.PlanEnablement{}, newError("GDS_PLAN_ENABLEMENT_FAILED", domain.ExitConflict,
			"Exact-plan one-shot mutation enablement could not be created.", err)
	}
	return value, nil
}

func (engine *Engine) apply(
	ctx context.Context,
	planID string,
	approvalReference string,
	signed *approvalcontract.Record,
) (result ApplyResult, returnError error) {
	defer func() {
		result.KillSwitches = engine.KillSwitches
		if result.PlanID == "" {
			result.PlanID = planID
		}
	}()
	if err := engine.validateConfiguration(true); err != nil {
		return ApplyResult{}, err
	}
	if engine.KillSwitchError != nil {
		return ApplyResult{PlanID: planID, Status: "blocked"}, newError(
			"GDS_KILL_SWITCH_INVALID", domain.ExitSecurity,
			"A kill-switch value is invalid; mutations fail closed.", engine.KillSwitchError,
		)
	}
	if engine.KillSwitches.MutationsDisabled {
		return ApplyResult{PlanID: planID, Status: "blocked"}, newError(
			"GDS_MUTATIONS_DISABLED", domain.ExitPolicy,
			"Global GDS mutations are disabled; no operation or handler was started.", nil,
		)
	}
	record, plan, err := engine.loadPlan(ctx, planID)
	if err != nil {
		return ApplyResult{}, err
	}
	now := engine.now()
	if !plan.ExpiresAt.After(now) {
		if transitionErr := engine.Store.TransitionPlan(ctx, planID, "planned", "stale"); transitionErr != nil {
			return ApplyResult{}, newError(
				"GDS_PLAN_EXPIRY_RECORD_FAILED", domain.ExitInternal,
				"Expired plan could not be marked stale.", transitionErr,
			)
		}
		return ApplyResult{PlanID: planID, Status: "stale"}, newError(
			"GDS_PLAN_EXPIRED", domain.ExitStale,
			"Plan expired before apply and no action handler was called.", nil,
		)
	}
	if plan.RequiresApproval() {
		if engine.RequireSignedApprovals && signed == nil {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_SIGNED_APPROVAL_REQUIRED", domain.ExitApproval,
				"This mutation requires a signed approval bound to the exact plan.", nil,
			)
		}
		if signed == nil && validateApprovalReference(approvalReference) != nil {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_APPROVAL_REQUIRED", domain.ExitApproval,
				"This plan requires approval before apply.", nil,
			)
		}
	}
	var signedDigest string
	if signed != nil {
		if engine.ApprovalVerifier == nil {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_APPROVAL_VERIFIER_UNAVAILABLE", domain.ExitSecurity,
				"Signed approval trust policy is unavailable.", nil,
			)
		}
		scope, scopeErr := approvalEvidence(plan, signed.ApprovalID)
		if scopeErr != nil {
			return ApplyResult{}, scopeErr
		}
		if verifyErr := engine.ApprovalVerifier.Verify(*signed, approvalcontract.Expectation{
			PlanID: plan.PlanID, PlanDigest: plan.PlanDigest, ApprovalClass: plan.ApprovalClass,
			ScopeDigest: scope.ScopeDigest, RequiredRole: "mutation-approver",
		}); verifyErr != nil {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_APPROVAL_SIGNATURE_INVALID", domain.ExitApproval,
				"Signed approval does not authorize the exact immutable plan.", verifyErr,
			)
		}
		signedDigest, err = signed.Digest()
		if err != nil {
			return ApplyResult{}, err
		}
		enablement, enableErr := engine.Store.GetPlanEnablement(ctx, "enablement:"+signed.ApprovalID)
		if enableErr != nil || enablement.Status != "active" || enablement.PlanID != plan.PlanID ||
			enablement.PlanDigest != plan.PlanDigest || enablement.ApprovalID != signed.ApprovalID ||
			enablement.ApprovalDigest != signedDigest || enablement.DeviceID != engine.DeviceID ||
			enablement.SessionID != engine.SessionID || !enablement.ExpiresAt.After(now) {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_PLAN_ENABLEMENT_REQUIRED", domain.ExitApproval,
				"Create a separate active one-shot enablement for this exact signed plan before apply.", enableErr,
			)
		}
	}
	if existing, loadErr := engine.Store.GetOperationByPlan(ctx, planID); loadErr == nil {
		return engine.replayApply(ctx, existing)
	} else if !errors.Is(loadErr, state.ErrNotFound) {
		return ApplyResult{}, newError(
			"GDS_OPERATION_LOOKUP_FAILED", domain.ExitInternal,
			"Existing operation state could not be inspected.", loadErr,
		)
	}
	if record.Status != "planned" {
		return ApplyResult{}, newError(
			"GDS_PLAN_STATE_CONFLICT", domain.ExitConflict,
			fmt.Sprintf("Plan is in %q state and cannot start a new apply.", record.Status), nil,
		)
	}
	for _, step := range plan.Steps {
		if _, found := engine.Handlers[step.Action]; !found {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_ACTION_HANDLER_MISSING", domain.ExitUnsupported,
				fmt.Sprintf("No action handler is registered for %q.", step.Action), nil,
			)
		}
		if step.Compensation.Mode == "automatic" {
			if !step.Compensation.Reversible || !step.Compensation.Idempotent {
				return ApplyResult{PlanID: planID, Status: "planned"}, newError(
					"GDS_COMPENSATION_PROOF_REQUIRED", domain.ExitPolicy,
					fmt.Sprintf("Automatic compensation for step %q requires explicit reversible and idempotent proofs.", step.StepID), nil,
				)
			}
			if _, found := engine.Handlers[step.Compensation.Action]; !found {
				return ApplyResult{PlanID: planID, Status: "planned"}, newError(
					"GDS_COMPENSATION_HANDLER_MISSING", domain.ExitUnsupported,
					fmt.Sprintf("No action handler is registered for automatic compensation %q.", step.Compensation.Action), nil,
				)
			}
		}
	}
	operationID, err := engine.NewID("op", now)
	if err != nil {
		return ApplyResult{}, err
	}
	actor, err := json.Marshal(plan.Actor)
	if err != nil {
		return ApplyResult{}, newError(
			"GDS_OPERATION_ACTOR_ENCODING_FAILED", domain.ExitInternal,
			"Operation actor evidence could not be encoded.", err,
		)
	}
	steps := make([]state.StepRecord, 0, len(plan.Steps))
	for index, step := range plan.Steps {
		idempotencyKey, keyErr := StepIdempotencyKey(plan, step)
		if keyErr != nil {
			return ApplyResult{}, newError(
				"GDS_IDEMPOTENCY_KEY_FAILED", domain.ExitInternal,
				"The exact operation step idempotency key could not be derived.", keyErr,
			)
		}
		steps = append(steps, state.StepRecord{
			OperationID: operationID, StepID: step.StepID, RepositoryID: step.RepositoryID,
			Action: step.Action, IdempotencyKey: idempotencyKey,
			Sequence: index, Status: "pending",
		})
	}
	var approval *ApprovalEvidence
	var approvalEvent *state.OperationEvent
	if approvalReference != "" || signed != nil {
		if signed != nil {
			approvalReference = signed.ApprovalID
		}
		evidence, evidenceErr := approvalEvidence(plan, approvalReference)
		if evidenceErr != nil {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_APPROVAL_EVIDENCE_FAILED", domain.ExitInternal,
				"Approval evidence could not be bound to the exact plan; no operation was started.",
				evidenceErr,
			)
		}
		approval = &evidence
		approvalEvent = &state.OperationEvent{
			EventType: "approval-recorded", OccurredAt: now, Payload: evidence,
		}
	}
	locks, err := engine.prepareLocks(operationID, planWriteSet(plan), now)
	if err != nil {
		return ApplyResult{PlanID: planID, Status: "planned"}, newError(
			"GDS_OPERATION_LOCK_ID_FAILED", domain.ExitInternal,
			"The complete repository lock set could not be prepared; no operation was started.", err,
		)
	}
	locks, err = engine.Store.StartApprovedOperation(ctx, state.ApprovedOperationStart{
		Operation: state.OperationRecord{
			OperationID: operationID, PlanID: planID, Operation: plan.Operation,
			Status: "applying", Actor: actor, StartedAt: now,
		},
		Steps: steps, Locks: locks, Approval: approvalEvent,
		Enablement: func() *state.EnablementConsumption {
			if signed == nil {
				return nil
			}
			return &state.EnablementConsumption{EnablementID: "enablement:" + signed.ApprovalID,
				PlanDigest: plan.PlanDigest, ApprovalID: signed.ApprovalID, ApprovalDigest: signedDigest,
				DeviceID: engine.DeviceID, SessionID: engine.SessionID, ConsumedAt: now}
		}(),
	})
	if err != nil {
		if existing, loadErr := engine.Store.GetOperationByPlan(ctx, planID); loadErr == nil {
			return engine.replayApply(ctx, existing)
		} else if !errors.Is(loadErr, state.ErrNotFound) {
			return ApplyResult{}, newError(
				"GDS_OPERATION_LOOKUP_FAILED", domain.ExitInternal,
				"Concurrent operation state could not be inspected.", loadErr,
			)
		}
		if errors.Is(err, state.ErrLockHeld) {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_REPOSITORY_LOCKED", domain.ExitConflict,
				"The complete repository lock set could not be acquired; no operation or action handler was started.",
				err,
			)
		}
		if errors.Is(err, state.ErrStateConflict) {
			return ApplyResult{PlanID: planID, Status: "planned"}, newError(
				"GDS_PLAN_APPROVAL_STATE_FAILED", domain.ExitConflict,
				"Plan state changed before the approved operation could start.", err,
			)
		}
		return ApplyResult{PlanID: planID, Status: "planned"}, newError(
			"GDS_OPERATION_START_FAILED", domain.ExitInternal,
			"Approved operation, approval evidence, and lock set could not be committed atomically.", err,
		)
	}
	result = ApplyResult{
		PlanID: planID, OperationID: operationID, Status: "applying",
		KillSwitches: engine.KillSwitches, Approval: approval,
	}
	engine.emitOperationTelemetry(ctx, operationID, plan, "operation.started", now, map[string]any{"step_count": len(plan.Steps)})

	mismatches, observeErr := engine.checkPreconditions(ctx, plan.Preconditions)
	if observeErr != nil || len(mismatches) != 0 {
		cause := observeErr
		if cause == nil {
			cause = fmt.Errorf("%d precondition fields changed", len(mismatches))
		}
		_, _ = engine.Store.AppendEvent(
			ctx, operationID, planID, "", "preconditions-stale", engine.now(),
			map[string]any{"mismatches": mismatches},
		)
		return engine.blockBeforeMutation(
			ctx, plan, operationID, locks, "GDS_STALE_PLAN",
			"Observed repository state no longer matches the plan; no action handler was called.",
			cause,
		)
	}
	if err := engine.Store.TransitionPlan(ctx, planID, "approved", "applying"); err != nil {
		return engine.blockBeforeMutation(
			ctx, plan, operationID, locks, "GDS_PLAN_APPLY_STATE_FAILED",
			"Plan state changed before action execution; no action handler was called.", err,
		)
	}
	if _, err := engine.Store.AppendEvent(
		ctx, operationID, planID, "", "preconditions-verified", engine.now(),
		map[string]any{"repositories": len(plan.Preconditions)},
	); err != nil {
		return engine.failBeforeHandler(ctx, plan, result, locks, 0, Step{}, err)
	}

	succeeded := 0
	recheckedRepositories := make(map[string]struct{}, len(plan.Preconditions))
	preconditionsByRepository := make(map[string]Precondition, len(plan.Preconditions))
	for _, precondition := range plan.Preconditions {
		preconditionsByRepository[precondition.RepositoryID] = precondition
	}
	for _, step := range plan.Steps {
		if _, rechecked := recheckedRepositories[step.RepositoryID]; !rechecked {
			expected := preconditionsByRepository[step.RepositoryID]
			mismatches, observeErr := engine.checkPreconditions(ctx, []Precondition{expected})
			if observeErr != nil || len(mismatches) != 0 {
				cause := observeErr
				if cause == nil {
					cause = fmt.Errorf("%d precondition fields changed", len(mismatches))
				}
				_, _ = engine.Store.AppendEvent(
					ctx, operationID, planID, step.StepID, "step-preconditions-stale", engine.now(),
					map[string]any{
						"repository_id": step.RepositoryID,
						"mismatches":    mismatches,
					},
				)
				return engine.blockOnStepPreconditionDrift(
					ctx, plan, result, locks, succeeded, step, cause,
				)
			}
			recheckedRepositories[step.RepositoryID] = struct{}{}
			if _, err := engine.Store.AppendEvent(
				ctx, operationID, planID, step.StepID, "step-preconditions-verified", engine.now(),
				map[string]any{"repository_id": step.RepositoryID},
			); err != nil {
				return engine.failBeforeHandler(ctx, plan, result, locks, succeeded, Step{}, err)
			}
		}
		if err := engine.Store.TransitionStep(
			ctx, operationID, step.StepID, "pending", "applying", engine.now(), nil, nil, "",
		); err != nil {
			return engine.failBeforeHandler(ctx, plan, result, locks, succeeded, step, err)
		}
		if _, err := engine.Store.AppendEvent(
			ctx, operationID, planID, step.StepID, "step-applying", engine.now(),
			map[string]any{"action": step.Action, "repository_id": step.RepositoryID},
		); err != nil {
			return engine.failBeforeHandler(ctx, plan, result, locks, succeeded, step, err)
		}
		result.MutationAttempted = true
		if err := engine.heartbeatLocks(ctx, locks); err != nil {
			return engine.failBeforeHandler(ctx, plan, result, locks, succeeded, step, err)
		}
		evidence, applyErr := engine.Handlers[step.Action].Apply(ctx, step)
		if applyErr != nil {
			return engine.failOperation(
				ctx, plan, result, locks, succeeded, step, true,
				evidence.Before, evidence.After, applyErr,
			)
		}
		if err := engine.heartbeatLocks(ctx, locks); err != nil {
			return engine.failOperation(ctx, plan, result, locks, succeeded, step, true, evidence.Before, evidence.After, err)
		}
		afterRaw, encodeErr := json.Marshal(evidence.After)
		if encodeErr != nil {
			return engine.failOperation(
				ctx, plan, result, locks, succeeded, step, true,
				evidence.Before, evidence.After, encodeErr,
			)
		}
		if verifyErr := engine.Handlers[step.Action].Verify(ctx, step, afterRaw); verifyErr != nil {
			return engine.failOperation(
				ctx, plan, result, locks, succeeded, step, true,
				evidence.Before, evidence.After, verifyErr,
			)
		}
		if err := engine.Store.TransitionStep(
			ctx, operationID, step.StepID, "applying", "succeeded", engine.now(),
			evidence.Before, evidence.After, "",
		); err != nil {
			return engine.failOperation(
				ctx, plan, result, locks, succeeded, step, true,
				evidence.Before, evidence.After, err,
			)
		}
		if _, err := engine.Store.AppendEvent(
			ctx, operationID, planID, step.StepID, "step-succeeded", engine.now(),
			map[string]any{"action": step.Action},
		); err != nil {
			return engine.failAfterAppliedStep(ctx, plan, result, locks, succeeded+1, step, err)
		}
		succeeded++
	}
	finished := engine.now()
	if err := engine.Store.FinalizeOperation(
		context.WithoutCancel(ctx),
		state.OperationFinalization{
			OperationID: operationID, PlanID: planID,
			ExpectedOperationStatus: "applying", ExpectedPlanStatus: "applying",
			OperationStatus: "succeeded", PlanStatus: "succeeded", FinishedAt: finished,
			Result: map[string]any{"steps_succeeded": succeeded}, Locks: locks,
		},
	); err != nil {
		operationError := newError(
			"GDS_OPERATION_FINALIZATION_FAILED", domain.ExitPartial,
			"Applied steps could not be atomically finalized with terminal evidence and exact lock release.",
			err,
		)
		operationError.OperationID = operationID
		operationError.MutationAttempted = true
		return result, operationError
	}
	result.Status = "succeeded"
	result.MutationCompleted = true
	engine.emitOperationTelemetry(context.WithoutCancel(ctx), operationID, plan, "operation.succeeded", finished, map[string]any{"steps_succeeded": succeeded})
	return result, nil
}

func (engine *Engine) emitOperationTelemetry(ctx context.Context, operationID string, plan Plan, name string, occurredAt time.Time, extra map[string]any) {
	if engine.Telemetry == nil {
		return
	}
	attributes := map[string]any{"operation_id": operationID, "plan_id": plan.PlanID, "device_id": engine.DeviceID,
		"session_id": engine.SessionID, "operation": plan.Operation}
	for key, value := range extra {
		attributes[key] = value
	}
	for _, signal := range []string{"log", "metric", "trace"} {
		eventName := name
		if signal == "metric" {
			eventName = "gds.operation.events"
			attributes["event_name"] = name
		}
		_ = engine.Telemetry.Emit(ctx, telemetry.Event{EventID: operationID + ":" + name + ":" + signal,
			SignalType: signal, Name: eventName, OccurredAt: occurredAt, Attributes: attributes})
	}
}

func (engine *Engine) replayApply(
	ctx context.Context,
	existing state.OperationRecord,
) (ApplyResult, error) {
	mutationAttempted := false
	switch existing.Status {
	case "succeeded", "partial":
		mutationAttempted = true
	case "failed":
		var recorded struct {
			MutationAttempted *bool `json:"mutation_attempted"`
		}
		if json.Unmarshal(existing.Result, &recorded) == nil && recorded.MutationAttempted != nil {
			mutationAttempted = *recorded.MutationAttempted
		} else {
			mutationAttempted = true
		}
	case "applying":
		steps, err := engine.Store.ListSteps(ctx, existing.OperationID)
		if err != nil {
			return ApplyResult{}, newError(
				"GDS_OPERATION_LOOKUP_FAILED", domain.ExitInternal,
				"Existing operation steps could not be inspected for replay.", err,
			)
		}
		for _, step := range steps {
			if step.Status == "applying" || step.Status == "succeeded" || step.Status == "failed" ||
				step.Status == "compensating" || step.Status == "compensated" {
				mutationAttempted = true
				break
			}
		}
	}
	replay := ApplyResult{
		PlanID: existing.PlanID, OperationID: existing.OperationID, Status: existing.Status,
		MutationAttempted: mutationAttempted, MutationCompleted: existing.Status == "succeeded",
		IdempotentReplay: true,
	}
	if existing.Status == "succeeded" {
		if _, _, err := engine.succeededJournal(ctx, existing); err != nil {
			return replay, err
		}
		return replay, nil
	}
	return replay, operationReplayError(existing.Status)
}

func (engine *Engine) succeededJournal(
	ctx context.Context,
	operation state.OperationRecord,
) (Plan, []state.StepRecord, error) {
	planRecord, plan, err := engine.loadPlan(ctx, operation.PlanID)
	if err != nil {
		return Plan{}, nil, err
	}
	if operation.Status != "succeeded" || planRecord.Status != "succeeded" {
		return Plan{}, nil, newError(
			"GDS_OPERATION_TERMINAL_STATE_INVALID", domain.ExitInternal,
			fmt.Sprintf(
				"Terminal operation and plan states are %q and %q, not both succeeded.",
				operation.Status, planRecord.Status,
			), nil,
		)
	}
	steps, err := engine.Store.ListSteps(ctx, operation.OperationID)
	if err != nil {
		return Plan{}, nil, err
	}
	if len(steps) == 0 || len(steps) != len(plan.Steps) {
		return Plan{}, nil, newError(
			"GDS_OPERATION_STEP_STATE_INVALID", domain.ExitInternal,
			"Succeeded operation step journal does not match the immutable plan.", nil,
		)
	}
	for index, recorded := range steps {
		expected := plan.Steps[index]
		idempotencyKey, keyErr := StepIdempotencyKey(plan, expected)
		if keyErr != nil || recorded.Sequence != index || recorded.Status != "succeeded" ||
			recorded.StepID != expected.StepID || recorded.RepositoryID != expected.RepositoryID ||
			recorded.Action != expected.Action || recorded.IdempotencyKey != idempotencyKey {
			return Plan{}, nil, newError(
				"GDS_OPERATION_STEP_STATE_INVALID", domain.ExitInternal,
				fmt.Sprintf("Recorded step %q differs from the immutable plan.", recorded.StepID),
				keyErr,
			)
		}
	}
	events, err := engine.Store.ListEvents(ctx, operation.OperationID)
	if err != nil {
		return Plan{}, nil, err
	}
	terminalEvents := 0
	for _, event := range events {
		if event.EventType == "operation-succeeded" {
			terminalEvents++
		}
	}
	if terminalEvents != 1 {
		return Plan{}, nil, newError(
			"GDS_OPERATION_TERMINAL_EVENT_INVALID", domain.ExitInternal,
			"Succeeded operation requires exactly one terminal success event.", nil,
		)
	}
	locks, err := engine.Store.ListLocksByOperation(ctx, operation.OperationID)
	if err != nil {
		return Plan{}, nil, err
	}
	if len(locks) != 0 {
		return Plan{}, nil, newError(
			"GDS_OPERATION_TERMINAL_LOCK_INVALID", domain.ExitInternal,
			"Succeeded operation still owns repository locks.", nil,
		)
	}
	return plan, steps, nil
}

func operationReplayError(status string) error {
	switch status {
	case "failed", "partial":
		return newError(
			"GDS_OPERATION_REPLAY_REQUIRES_RECOVERY", domain.ExitPartial,
			fmt.Sprintf("Existing operation is %q and requires explicit recovery; apply was not retried.", status), nil,
		)
	case "applying", "blocked":
		return newError(
			"GDS_OPERATION_REPLAY_BLOCKED", domain.ExitConflict,
			fmt.Sprintf("Existing operation is %q; apply was not retried.", status), nil,
		)
	default:
		return newError(
			"GDS_OPERATION_REPLAY_STATE_INVALID", domain.ExitInternal,
			fmt.Sprintf("Existing operation has unsupported status %q; apply was not retried.", status), nil,
		)
	}
}

func (engine *Engine) blockOnStepPreconditionDrift(
	ctx context.Context,
	plan Plan,
	result ApplyResult,
	locks []state.Lock,
	succeeded int,
	step Step,
	cause error,
) (ApplyResult, error) {
	now := engine.now()
	compensated, compensationErr := engine.compensateSucceeded(
		context.WithoutCancel(ctx), plan, result.OperationID, locks, succeeded,
	)
	status := "blocked"
	planStatus := "stale"
	class := domain.ExitStale
	message := fmt.Sprintf(
		"Repository %q changed before its first mutation step; its action handler was not called.",
		step.RepositoryID,
	)
	if succeeded > 0 {
		status = "partial"
		planStatus = "partial"
		class = domain.ExitPartial
		message = fmt.Sprintf(
			"Repository %q changed after %d earlier step(s) succeeded; remaining action handlers were not called.",
			step.RepositoryID, succeeded,
		)
	}
	transitions, transitionErr := engine.terminalStepTransitions(
		context.WithoutCancel(ctx), result.OperationID, "", "GDS_STALE_PLAN", nil, nil,
	)
	payload := map[string]any{
		"code": "GDS_STALE_PLAN", "stale_repository_id": step.RepositoryID,
		"steps_succeeded": succeeded, "steps_compensated": compensated,
	}
	if compensationErr != nil {
		payload["compensation_error"] = compensationErr.Error()
	}
	if transitionErr == nil {
		transitionErr = engine.Store.FinalizeOperation(
			context.WithoutCancel(ctx),
			state.OperationFinalization{
				OperationID: result.OperationID, PlanID: plan.PlanID,
				ExpectedOperationStatus: "applying", ExpectedPlanStatus: "applying",
				OperationStatus: status, PlanStatus: planStatus, FinishedAt: now,
				Result: payload, StepTransitions: transitions, Locks: locks,
			},
		)
	}
	if transitionErr != nil {
		return engine.finalizationFailure(result, succeeded > 0, cause, transitionErr)
	}
	result.Status = status
	result.MutationAttempted = succeeded > 0
	operationError := newError("GDS_STALE_PLAN", class, message, errors.Join(cause, compensationErr))
	operationError.OperationID = result.OperationID
	operationError.MutationAttempted = result.MutationAttempted
	return result, operationError
}

func (engine *Engine) Verify(
	ctx context.Context,
	operationID string,
) (result VerifyResult, returnError error) {
	defer func() { result.KillSwitches = engine.KillSwitches }()
	if err := engine.validateConfiguration(true); err != nil {
		return VerifyResult{}, err
	}
	if engine.KillSwitchError != nil {
		return VerifyResult{}, newError(
			"GDS_KILL_SWITCH_INVALID", domain.ExitSecurity,
			"A kill-switch value is invalid; verification journaling fails closed.",
			engine.KillSwitchError,
		)
	}
	if engine.KillSwitches.MutationsDisabled {
		return VerifyResult{}, newError(
			"GDS_MUTATIONS_DISABLED", domain.ExitPolicy,
			"Global GDS mutations are disabled; verification evidence was not journaled.", nil,
		)
	}
	operation, err := engine.Store.GetOperation(ctx, operationID)
	if err != nil {
		return VerifyResult{}, err
	}
	if operation.Status != "succeeded" {
		return VerifyResult{}, newError(
			"GDS_OPERATION_NOT_VERIFIABLE", domain.ExitConflict,
			fmt.Sprintf("Operation is in %q state, not succeeded.", operation.Status), nil,
		)
	}
	plan, steps, err := engine.succeededJournal(ctx, operation)
	if err != nil {
		return VerifyResult{}, err
	}
	planSteps := map[string]Step{}
	for _, step := range plan.Steps {
		planSteps[step.StepID] = step
	}
	for _, recorded := range steps {
		step, found := planSteps[recorded.StepID]
		if !found || recorded.Status != "succeeded" {
			return VerifyResult{}, newError(
				"GDS_OPERATION_STEP_STATE_INVALID", domain.ExitInternal,
				fmt.Sprintf("Recorded step %q is missing or not succeeded.", recorded.StepID), nil,
			)
		}
		handler, found := engine.Handlers[step.Action]
		if !found {
			return VerifyResult{}, newError(
				"GDS_ACTION_HANDLER_MISSING", domain.ExitUnsupported,
				fmt.Sprintf("No verification handler is registered for %q.", step.Action), nil,
			)
		}
		if err := handler.Verify(ctx, step, recorded.After); err != nil {
			_, _ = engine.Store.AppendEvent(
				ctx, operationID, operation.PlanID, step.StepID, "verification-failed", engine.now(),
				map[string]any{"code": "GDS_OPERATION_VERIFICATION_FAILED"},
			)
			return VerifyResult{}, newError(
				"GDS_OPERATION_VERIFICATION_FAILED", domain.ExitValidation,
				fmt.Sprintf("Post-apply verification failed for step %q.", step.StepID), err,
			)
		}
	}
	_, err = engine.Store.AppendEvent(
		ctx, operationID, operation.PlanID, "", "operation-verified", engine.now(),
		map[string]any{"steps": len(steps)},
	)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{
		PlanID: operation.PlanID, OperationID: operationID, Status: "verified", Steps: len(steps),
		KillSwitches: engine.KillSwitches,
	}, nil
}

func (engine *Engine) loadPlan(ctx context.Context, planID string) (state.PlanRecord, Plan, error) {
	record, err := engine.Store.GetPlan(ctx, planID)
	if err != nil {
		return state.PlanRecord{}, Plan{}, err
	}
	plan, err := DecodePlan(record.Body)
	if err != nil {
		return state.PlanRecord{}, Plan{}, newError(
			"GDS_PLAN_DECODE_FAILED", domain.ExitValidation,
			"Stored plan cannot be decoded.", err,
		)
	}
	findings := plan.Validate(engine.Schemas)
	// A record whose stored digest disagrees with its body is a different defect
	// from a schema violation, and reporting both as "N findings" hid which one
	// happened. The mismatch gets its own finding so the two are never confused:
	// a valid plan under a wrong digest is tampering or corruption, not a bad
	// field, and it is diagnosed by comparing the two digests rather than by
	// re-reading the schema.
	if plan.PlanDigest != record.PlanDigest {
		findings = append(findings, domain.Finding{
			Code: "GDS_PLAN_RECORD_DIGEST_MISMATCH", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf(
				"stored plan_digest %s does not match the digest of the stored plan body %s",
				record.PlanDigest, plan.PlanDigest,
			),
			Evidence: map[string]any{
				"plan_id": plan.PlanID, "record_plan_digest": record.PlanDigest,
				"body_plan_digest": plan.PlanDigest,
			},
		})
	}
	if len(findings) != 0 {
		return state.PlanRecord{}, Plan{}, planValidationError(
			"GDS_PLAN_INVALID",
			"Stored plan failed schema, semantic, or immutable digest validation.",
			findings,
		)
	}
	return record, plan, nil
}

func (engine *Engine) validateConfiguration(requireRuntime bool) error {
	if engine.Store == nil || engine.Schemas == nil {
		return newError(
			"GDS_OPERATION_ENGINE_INVALID", domain.ExitInternal,
			"Operation engine requires a state store and schema set.", nil,
		)
	}
	if !requireRuntime {
		return nil
	}
	if engine.Checker == nil || engine.Now == nil || engine.NewID == nil ||
		engine.DeviceID == "" || engine.SessionID == "" || engine.PID <= 0 || engine.Lease <= 0 {
		return newError(
			"GDS_OPERATION_ENGINE_INVALID", domain.ExitInternal,
			"Operation engine runtime identity, clock, checker, and lease must be configured.", nil,
		)
	}
	return nil
}

func (engine *Engine) now() time.Time {
	if engine.Now != nil {
		return engine.Now().UTC()
	}
	return time.Now().UTC()
}

func DefaultIDGenerator(prefix string, now time.Time) (string, error) {
	return identity.New(prefix, now, nil)
}

func (engine *Engine) prepareLocks(
	operationID string,
	writeTargets []string,
	now time.Time,
) ([]state.Lock, error) {
	ordered := append([]string(nil), writeTargets...)
	sort.Strings(ordered)
	locks := make([]state.Lock, 0, len(ordered))
	for _, target := range ordered {
		lockID, err := engine.NewID("lock", engine.now())
		if err != nil {
			return nil, err
		}
		locks = append(locks, state.Lock{
			Scope: engine.lockScope(), ScopeID: target, LockID: lockID,
			OperationID: operationID, DeviceID: engine.DeviceID, SessionID: engine.SessionID,
			PID: engine.PID, AcquiredAt: now, LeaseExpiresAt: now.Add(engine.Lease),
		})
	}
	return locks, nil
}

func planWriteSet(plan Plan) []string {
	set := map[string]struct{}{}
	for _, step := range plan.Steps {
		for _, target := range step.WriteSet {
			set[step.RepositoryID+":"+target] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for target := range set {
		result = append(result, target)
	}
	sort.Strings(result)
	return result
}

func (engine *Engine) heartbeatLocks(ctx context.Context, locks []state.Lock) error {
	heartbeatAt := engine.now()
	leaseExpiresAt := heartbeatAt.Add(engine.Lease)
	for _, lock := range locks {
		if err := engine.Store.HeartbeatLock(ctx, lock, heartbeatAt, leaseExpiresAt); err != nil {
			return fmt.Errorf("heartbeat lock %s/%s: %w", lock.Scope, lock.ScopeID, err)
		}
	}
	return nil
}

func (engine *Engine) lockScope() string {
	if engine.LockScope == "" {
		return "write-set"
	}
	return engine.LockScope
}

func (engine *Engine) checkPreconditions(
	ctx context.Context,
	preconditions []Precondition,
) ([]map[string]any, error) {
	mismatches := []map[string]any{}
	for _, expected := range preconditions {
		observed, err := engine.Checker.Observe(ctx, expected.RepositoryID)
		if err != nil {
			return mismatches, fmt.Errorf("observe %s: %w", expected.RepositoryID, err)
		}
		fields := []struct {
			name     string
			expected string
			observed string
		}{
			{"head_oid", expected.HeadOID, observed.HeadOID},
			{"worktree_fingerprint", expected.WorktreeFingerprint, observed.WorktreeFingerprint},
			{"index_tree_oid", expected.IndexTreeOID, observed.IndexTreeOID},
			{"upstream_oid", expected.UpstreamOID, observed.UpstreamOID},
			{"remote_default_oid", expected.RemoteDefaultOID, observed.RemoteDefaultOID},
			{"remote_evidence_digest", expected.RemoteEvidenceDigest, observed.RemoteEvidenceDigest},
			{"manifest_digest", expected.ManifestDigest, observed.ManifestDigest},
			{"policy_digest", expected.PolicyDigest, observed.PolicyDigest},
		}
		for _, field := range fields {
			if field.expected != field.observed {
				mismatches = append(mismatches, map[string]any{
					"repository_id": expected.RepositoryID, "field": field.name,
					"expected": field.expected, "observed": field.observed,
				})
			}
		}
	}
	return mismatches, nil
}

func (engine *Engine) blockBeforeMutation(
	ctx context.Context,
	plan Plan,
	operationID string,
	locks []state.Lock,
	code string,
	message string,
	cause error,
) (ApplyResult, error) {
	now := engine.now()
	result := ApplyResult{PlanID: plan.PlanID, OperationID: operationID, Status: "applying"}
	transitions, transitionErr := engine.terminalStepTransitions(
		context.WithoutCancel(ctx), operationID, "", code, nil, nil,
	)
	planStatus := "failed"
	if code == "GDS_STALE_PLAN" {
		planStatus = "stale"
	}
	if transitionErr == nil {
		transitionErr = engine.Store.FinalizeOperation(
			context.WithoutCancel(ctx),
			state.OperationFinalization{
				OperationID: operationID, PlanID: plan.PlanID,
				ExpectedOperationStatus: "applying", ExpectedPlanStatus: "approved",
				OperationStatus: "blocked", PlanStatus: planStatus, FinishedAt: now,
				Result: map[string]any{"code": code}, StepTransitions: transitions, Locks: locks,
			},
		)
	}
	if transitionErr != nil {
		return engine.finalizationFailure(result, false, cause, transitionErr)
	}
	operationError := newError(code, domain.ExitConflict, message, cause)
	if code == "GDS_STALE_PLAN" {
		operationError.Class = domain.ExitStale
	}
	operationError.OperationID = operationID
	result.Status = "blocked"
	return result, operationError
}

func (engine *Engine) failOperation(
	ctx context.Context,
	plan Plan,
	result ApplyResult,
	locks []state.Lock,
	succeeded int,
	step Step,
	mutationAttempted bool,
	before any,
	after any,
	cause error,
) (ApplyResult, error) {
	now := engine.now()
	compensated, compensationErr := engine.compensateSucceeded(
		context.WithoutCancel(ctx), plan, result.OperationID, locks, succeeded,
	)
	// Use WithoutCancel so that a SIGTERM during the post-apply heartbeat or
	// handler-failure window still produces a durable partial/failed journal
	// entry. terminalStepTransitions reads step state (ListSteps) before
	// FinalizeOperation; if the read is killed by cancellation, the finalize
	// never runs and the mutation evidence is lost from the journal.
	transitions, transitionErr := engine.terminalStepTransitions(
		context.WithoutCancel(ctx), result.OperationID, step.StepID, "GDS_OPERATION_STEP_FAILED", before, after,
	)
	status := "failed"
	planStatus := "failed"
	class := domain.ExitInternal
	if mutationAttempted || succeeded > 0 {
		status = "partial"
		planStatus = "partial"
		class = domain.ExitPartial
	}
	payload := map[string]any{
		"failed_step": step.StepID, "steps_succeeded": succeeded,
		"steps_compensated": compensated,
		"code":              "GDS_OPERATION_STEP_FAILED",
	}
	failureEvidence := safeFailureEvidence(cause)
	if failureEvidence != nil {
		payload["failure_evidence"] = failureEvidence
	}
	if compensationErr != nil {
		payload["compensation_error"] = compensationErr.Error()
	}
	events := []state.OperationEvent{}
	if hasFailedTransition(transitions, step.StepID) {
		eventPayload := map[string]any{"action": step.Action, "code": "GDS_OPERATION_STEP_FAILED"}
		if failureEvidence != nil {
			eventPayload["failure_evidence"] = failureEvidence
		}
		events = append(events, state.OperationEvent{
			StepID: step.StepID, EventType: "step-failed", OccurredAt: now,
			Payload: eventPayload,
		})
	}
	if transitionErr == nil {
		transitionErr = engine.Store.FinalizeOperation(
			context.WithoutCancel(ctx),
			state.OperationFinalization{
				OperationID: result.OperationID, PlanID: plan.PlanID,
				ExpectedOperationStatus: "applying", ExpectedPlanStatus: "applying",
				OperationStatus: status, PlanStatus: planStatus, FinishedAt: now,
				Result: payload, StepTransitions: transitions, Events: events, Locks: locks,
			},
		)
	}
	if transitionErr != nil {
		return engine.finalizationFailure(
			result, mutationAttempted || succeeded > 0, cause, transitionErr,
		)
	}
	result.Status = status
	result.MutationAttempted = mutationAttempted || succeeded > 0
	operationError := newError(
		"GDS_OPERATION_STEP_FAILED", class,
		fmt.Sprintf("Operation stopped at step %q; %d earlier step(s) were automatically compensated.", step.StepID, compensated), errors.Join(cause, compensationErr),
	)
	operationError.OperationID = result.OperationID
	operationError.MutationAttempted = result.MutationAttempted
	return result, operationError
}

// safeOperationFailure is implemented by provider errors whose bounded fields
// are explicitly safe to persist. Raw handler errors are never journaled: they
// can contain credentials, private repository names, response bodies, or other
// untrusted text.
type safeOperationFailure interface {
	SafeOperationFailureEvidence() map[string]any
}

func safeFailureEvidence(cause error) map[string]any {
	var safe safeOperationFailure
	if cause == nil || !errors.As(cause, &safe) {
		return nil
	}
	return safe.SafeOperationFailureEvidence()
}

// compensateSucceeded reverses only steps that reached verified succeeded
// state and explicitly carry both reversibility and idempotence proofs. The
// current failed/applying step is intentionally excluded because its partial
// side effects are not proven.
func (engine *Engine) compensateSucceeded(
	ctx context.Context,
	plan Plan,
	operationID string,
	locks []state.Lock,
	succeeded int,
) (int, error) {
	compensated := 0
	for index := succeeded - 1; index >= 0; index-- {
		original := plan.Steps[index]
		contract := original.Compensation
		if contract.Mode != "automatic" {
			continue
		}
		if !contract.Reversible || !contract.Idempotent {
			return compensated, fmt.Errorf("step %q lacks automatic compensation proofs", original.StepID)
		}
		handler, found := engine.Handlers[contract.Action]
		if !found {
			return compensated, fmt.Errorf("compensation handler %q is unavailable", contract.Action)
		}
		if err := engine.heartbeatLocks(ctx, locks); err != nil {
			return compensated, err
		}
		if err := engine.Store.TransitionStep(ctx, operationID, original.StepID, "succeeded", "compensating", engine.now(), nil, nil, ""); err != nil {
			return compensated, err
		}
		_, _ = engine.Store.AppendEvent(ctx, operationID, plan.PlanID, original.StepID, "step-compensating", engine.now(), map[string]any{
			"action": contract.Action, "original_action": original.Action,
		})
		compensationStep := original
		compensationStep.Action = contract.Action
		evidence, applyErr := handler.Apply(ctx, compensationStep)
		if applyErr == nil {
			afterRaw, encodeErr := json.Marshal(evidence.After)
			if encodeErr != nil {
				applyErr = encodeErr
			} else {
				applyErr = handler.Verify(ctx, compensationStep, afterRaw)
			}
		}
		if applyErr != nil {
			_ = engine.Store.TransitionStep(ctx, operationID, original.StepID, "compensating", "failed", engine.now(), evidence.Before, evidence.After, applyErr.Error())
			_, _ = engine.Store.AppendEvent(ctx, operationID, plan.PlanID, original.StepID, "step-compensation-failed", engine.now(), map[string]any{
				"action": contract.Action, "error": applyErr.Error(),
			})
			return compensated, fmt.Errorf("compensate step %q: %w", original.StepID, applyErr)
		}
		if err := engine.Store.TransitionStep(ctx, operationID, original.StepID, "compensating", "compensated", engine.now(), evidence.Before, evidence.After, ""); err != nil {
			return compensated, err
		}
		if _, err := engine.Store.AppendEvent(ctx, operationID, plan.PlanID, original.StepID, "step-compensated", engine.now(), map[string]any{
			"action": contract.Action,
		}); err != nil {
			return compensated, err
		}
		compensated++
	}
	return compensated, nil
}

func (engine *Engine) failBeforeHandler(
	ctx context.Context,
	plan Plan,
	result ApplyResult,
	locks []state.Lock,
	succeeded int,
	step Step,
	cause error,
) (ApplyResult, error) {
	now := engine.now()
	transitions, transitionErr := engine.terminalStepTransitions(
		context.WithoutCancel(ctx), result.OperationID, step.StepID, "GDS_OPERATION_JOURNAL_FAILED", nil, nil,
	)
	status := "failed"
	class := domain.ExitInternal
	mutationAttempted := false
	if succeeded > 0 {
		status = "partial"
		class = domain.ExitPartial
		mutationAttempted = true
	}
	payload := map[string]any{
		"code": "GDS_OPERATION_JOURNAL_FAILED", "mutation_attempted": mutationAttempted,
		"steps_succeeded": succeeded,
	}
	if transitionErr == nil {
		transitionErr = engine.Store.FinalizeOperation(
			context.WithoutCancel(ctx),
			state.OperationFinalization{
				OperationID: result.OperationID, PlanID: plan.PlanID,
				ExpectedOperationStatus: "applying", ExpectedPlanStatus: "applying",
				OperationStatus: status, PlanStatus: status, FinishedAt: now,
				Result: payload, StepTransitions: transitions, Locks: locks,
			},
		)
	}
	if transitionErr != nil {
		return engine.finalizationFailure(result, mutationAttempted, cause, transitionErr)
	}
	result.Status = status
	result.MutationAttempted = mutationAttempted
	operationError := newError(
		"GDS_OPERATION_JOURNAL_FAILED", class,
		"Operation stopped before calling an action handler because durable state could not advance.",
		cause,
	)
	operationError.OperationID = result.OperationID
	operationError.MutationAttempted = mutationAttempted
	return result, operationError
}

func (engine *Engine) failAfterAppliedStep(
	ctx context.Context,
	plan Plan,
	result ApplyResult,
	locks []state.Lock,
	succeeded int,
	step Step,
	cause error,
) (ApplyResult, error) {
	now := engine.now()
	transitions, transitionErr := engine.terminalStepTransitions(
		context.WithoutCancel(ctx), result.OperationID, "", "GDS_OPERATION_DURABILITY_FAILED", nil, nil,
	)
	payload := map[string]any{
		"code": "GDS_OPERATION_DURABILITY_FAILED", "steps_succeeded": succeeded,
		"last_step": step.StepID,
	}
	events := []state.OperationEvent{}
	if step.StepID != "" {
		events = append(events, state.OperationEvent{
			StepID: step.StepID, EventType: "step-succeeded", OccurredAt: now,
			Payload: map[string]any{"action": step.Action},
		})
	}
	if transitionErr == nil {
		transitionErr = engine.Store.FinalizeOperation(
			context.WithoutCancel(ctx),
			state.OperationFinalization{
				OperationID: result.OperationID, PlanID: plan.PlanID,
				ExpectedOperationStatus: "applying", ExpectedPlanStatus: "applying",
				OperationStatus: "partial", PlanStatus: "partial", FinishedAt: now,
				Result: payload, StepTransitions: transitions, Events: events, Locks: locks,
			},
		)
	}
	if transitionErr != nil {
		return engine.finalizationFailure(result, true, cause, transitionErr)
	}
	result.Status = "partial"
	result.MutationAttempted = true
	operationError := newError(
		"GDS_OPERATION_DURABILITY_FAILED", domain.ExitPartial,
		"Mutation completed at least one action, but final durable evidence or lock release failed.", cause,
	)
	operationError.OperationID = result.OperationID
	operationError.MutationAttempted = true
	return result, operationError
}

func (engine *Engine) terminalStepTransitions(
	ctx context.Context,
	operationID string,
	failedStepID string,
	reason string,
	before any,
	after any,
) ([]state.TerminalStepTransition, error) {
	recordedSteps, err := engine.Store.ListSteps(ctx, operationID)
	if err != nil {
		return nil, err
	}
	transitions := make([]state.TerminalStepTransition, 0, len(recordedSteps))
	for _, recorded := range recordedSteps {
		switch recorded.Status {
		case "pending":
			transitions = append(transitions, state.TerminalStepTransition{
				StepID: recorded.StepID, Expected: "pending", Next: "blocked", LastError: reason,
			})
		case "applying":
			if failedStepID == "" || recorded.StepID != failedStepID {
				return nil, fmt.Errorf(
					"step %q is applying without exact pre-handler failure evidence", recorded.StepID,
				)
			}
			transitions = append(transitions, state.TerminalStepTransition{
				StepID: recorded.StepID, Expected: "applying", Next: "failed",
				Before: before, After: after, LastError: reason,
			})
		case "succeeded", "failed", "blocked", "compensated":
			// Already-terminal step evidence is preserved.
		case "compensating":
			return nil, fmt.Errorf("step %q has an in-flight compensation", recorded.StepID)
		default:
			return nil, fmt.Errorf("step %q has unsupported status %q", recorded.StepID, recorded.Status)
		}
	}
	return transitions, nil
}

func hasFailedTransition(transitions []state.TerminalStepTransition, stepID string) bool {
	for _, transition := range transitions {
		if transition.StepID == stepID && transition.Next == "failed" {
			return true
		}
	}
	return false
}

func (engine *Engine) finalizationFailure(
	result ApplyResult,
	mutationAttempted bool,
	cause error,
	finalizationError error,
) (ApplyResult, error) {
	class := domain.ExitInternal
	if mutationAttempted {
		class = domain.ExitPartial
	}
	result.Status = "applying"
	result.MutationAttempted = mutationAttempted
	result.MutationCompleted = false
	operationError := newError(
		"GDS_OPERATION_FINALIZATION_FAILED", class,
		"Terminal state was not committed atomically; exact operation locks were retained for explicit recovery.",
		errors.Join(cause, finalizationError),
	)
	operationError.OperationID = result.OperationID
	operationError.MutationAttempted = mutationAttempted
	return result, operationError
}

func validateApprovalReference(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("approval reference is empty")
	}
	if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("approval reference must be a single line of at most 256 bytes")
	}
	return nil
}

func ValidateApprovalReference(value string) error {
	return validateApprovalReference(value)
}

func DigestReference(value string) string {
	return digestString(value)
}

func digestString(value string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value)))
}

func NewDefaultEngine(
	store *state.Store,
	schemas *validation.Set,
	checker PreconditionChecker,
	handlers map[string]ActionHandler,
	deviceID string,
	sessionID string,
) *Engine {
	killSwitches, killSwitchError := LoadKillSwitches(os.LookupEnv)
	engine := &Engine{
		Store: store, Schemas: schemas, Checker: checker, Handlers: handlers,
		Now: time.Now, NewID: DefaultIDGenerator, DeviceID: deviceID,
		SessionID: sessionID, PID: os.Getpid(), Lease: 5 * time.Minute,
		LockScope:    "repository",
		KillSwitches: killSwitches, KillSwitchError: killSwitchError,
		RequireSignedApprovals: true,
	}
	if path := os.Getenv("GDS_TRUST_POLICY_FILE"); path != "" {
		policy, err := trust.LoadPolicy(path)
		if err != nil {
			engine.KillSwitchError = errors.Join(engine.KillSwitchError, fmt.Errorf("load GDS trust policy: %w", err))
		} else {
			engine.ApprovalVerifier = &approvalcontract.Verifier{Trust: trust.Verifier{Policy: policy},
				MaximumTTL: 24 * time.Hour, MaximumFuture: 5 * time.Minute, Now: engine.Now}
		}
	}
	if exporter, err := telemetry.FromEnvironment(store); err != nil {
		engine.KillSwitchError = errors.Join(engine.KillSwitchError, fmt.Errorf("configure OTLP exporter: %w", err))
	} else {
		engine.Telemetry = exporter
	}
	return engine
}
