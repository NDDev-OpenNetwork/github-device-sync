package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

const (
	recoveryAction       = "recover-interrupted-operation"
	recoveryPlanLifetime = 15 * time.Minute
)

type RecoveryOperationOptions struct {
	StatePath         string
	DeviceID          string
	SessionID         string
	ApprovalReference string
}

type RecoveryOwnerEvidence struct {
	LockID       string `json:"lock_id"`
	DeviceID     string `json:"device_id"`
	PID          int    `json:"pid"`
	LeaseState   string `json:"lease_state"`
	ProcessState string `json:"process_state"`
}

type RecoveryDecision struct {
	Classification string                  `json:"classification"`
	Mode           string                  `json:"mode,omitempty"`
	Reason         string                  `json:"reason"`
	Automatable    bool                    `json:"automatable"`
	StateDigest    string                  `json:"state_digest"`
	DecisionDigest string                  `json:"decision_digest"`
	Owners         []RecoveryOwnerEvidence `json:"owners"`
	Compensations  []RecoveryCompensation  `json:"compensations"`
	Blockers       []string                `json:"blockers"`
}

type RecoveryCompensation struct {
	StepID string `json:"step_id"`
	Mode   string `json:"mode"`
	Action string `json:"action,omitempty"`
	Status string `json:"status"`
}

type RecoveryPlanData struct {
	OriginalOperationID string                 `json:"original_operation_id"`
	StatePath           string                 `json:"state_path"`
	Decision            RecoveryDecision       `json:"decision"`
	Snapshot            state.RecoverySnapshot `json:"snapshot"`
	Plan                *operations.Plan       `json:"plan,omitempty"`
}

type recoveryContext struct {
	root         string
	repositoryID string
	observation  operations.Observation
	snapshot     state.RecoverySnapshot
	plan         operations.Plan
}

type recoveryObserver struct {
	services    *Services
	root        string
	store       *state.Store
	operationID string
}

func (observer recoveryObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.recoveryOperationContext(
		ctx, observer.root, observer.store, observer.operationID,
	)
	if len(findings) != 0 {
		return operations.Observation{}, fmt.Errorf(
			"recovery precondition returned %d findings", len(findings),
		)
	}
	if current.repositoryID != repositoryID {
		return operations.Observation{}, fmt.Errorf(
			"repository identity changed from %s to %s", repositoryID, current.repositoryID,
		)
	}
	return current.observation, nil
}

type recoveryHandler struct {
	store *state.Store
	now   func() time.Time
}

func (handler recoveryHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := recoveryParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	before, err := handler.store.RecoverySnapshot(ctx, parameters.OperationID)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if before.Digest != parameters.StateDigest || len(before.Locks) != parameters.LockCount {
		return operations.ApplyEvidence{Before: before}, state.ErrStateConflict
	}
	mutation := state.RecoveryMutation{
		Expected: before, Mode: parameters.Mode, Reason: parameters.Reason,
		RecoveredAt: handler.now().UTC(),
	}
	switch parameters.Mode {
	case "abort-interrupted":
		mutation.NextOperationStatus = "failed"
		mutation.NextPlanStatus = "failed"
	case "close-partial":
		mutation.NextOperationStatus = "partial"
		mutation.NextPlanStatus = "partial"
	case "release-stale-locks":
	default:
		return operations.ApplyEvidence{Before: before}, fmt.Errorf(
			"unsupported recovery mode %q", parameters.Mode,
		)
	}
	after, err := handler.store.RecoverOperation(ctx, mutation)
	return operations.ApplyEvidence{Before: before, After: after}, err
}

func (handler recoveryHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := recoveryParameters(step)
	if err != nil {
		return err
	}
	var expected state.RecoverySnapshot
	if err := json.Unmarshal(afterRaw, &expected); err != nil {
		return fmt.Errorf("decode recovery after evidence: %w", err)
	}
	current, err := handler.store.RecoverySnapshot(ctx, parameters.OperationID)
	if err != nil {
		return err
	}
	if len(current.Locks) != 0 || current.Digest != expected.Digest {
		return fmt.Errorf("recovered operation state differs from exact after evidence")
	}
	switch parameters.Mode {
	case "abort-interrupted":
		if current.OperationStatus != "failed" || current.PlanStatus != "failed" {
			return fmt.Errorf("aborted operation did not reach failed terminal state")
		}
	case "close-partial":
		if current.OperationStatus != "partial" || current.PlanStatus != "partial" {
			return fmt.Errorf("partial operation did not reach partial terminal state")
		}
	case "release-stale-locks":
		if current.OperationStatus == "applying" {
			return fmt.Errorf("stale-lock-only recovery left an applying operation")
		}
	}
	return nil
}

type parsedRecoveryParameters struct {
	OperationID string
	StateDigest string
	Mode        string
	Reason      string
	LockCount   int
}

func recoveryParameters(step operations.Step) (parsedRecoveryParameters, error) {
	if step.Action != recoveryAction {
		return parsedRecoveryParameters{}, fmt.Errorf("unexpected recovery action %q", step.Action)
	}
	raw, ok := step.Parameters["recovery"].(map[string]any)
	if !ok {
		return parsedRecoveryParameters{}, errors.New("recovery parameters are missing")
	}
	result := parsedRecoveryParameters{}
	result.OperationID, _ = raw["operation_id"].(string)
	result.StateDigest, _ = raw["state_digest"].(string)
	result.Mode, _ = raw["mode"].(string)
	result.Reason, _ = raw["reason"].(string)
	switch value := raw["lock_count"].(type) {
	case int:
		result.LockCount = value
	case float64:
		result.LockCount = int(value)
	}
	if result.OperationID == "" || result.StateDigest == "" || result.Mode == "" ||
		result.Reason == "" || result.LockCount <= 0 {
		return parsedRecoveryParameters{}, errors.New("recovery parameters are incomplete")
	}
	return result, nil
}

func (services *Services) PlanOperationRecovery(
	ctx context.Context,
	path string,
	operationID string,
	options RecoveryOperationOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return domain.NewEnvelope("gds recover operation plan", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_OPERATION_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "An exact interrupted operation id is required.",
		})
	}
	if finding := validateLocalOperationIdentity(ProjectionOperationOptions{
		DeviceID: options.DeviceID, SessionID: options.SessionID,
	}); finding != nil {
		return domain.NewEnvelope("gds recover operation plan", domain.ExitInput, nil, *finding)
	}
	statePath, store, finding := openOperationState(ctx, options.StatePath)
	if finding != nil {
		return domain.NewEnvelope("gds recover operation plan", domain.ExitInput, nil, *finding)
	}
	defer store.Close()
	current, findings := services.recoveryOperationContext(ctx, path, store, operationID)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds recover operation plan", classifyFindings(findings), nil, findings...,
		)
	}
	decision := decideRecovery(
		current.snapshot, current.plan, services.Now().UTC(), options.DeviceID,
	)
	data := RecoveryPlanData{
		OriginalOperationID: operationID, StatePath: statePath,
		Decision: decision, Snapshot: current.snapshot,
	}
	if !decision.Automatable {
		return domain.NewEnvelope(
			"gds recover operation plan", domain.ExitNotProven, data, domain.Finding{
				Code: "GDS_RECOVERY_MANUAL_ONLY", Severity: domain.SeverityHigh,
				Message:  "Recovery cannot be automated from current evidence; the explicit manual plan and blockers are reported without mutation.",
				Evidence: map[string]any{"blockers": decision.Blockers},
			},
		)
	}
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError("gds recover operation plan", err)
	}
	plan, err := operations.NewPlan(
		planID, now, now.Add(recoveryPlanLifetime), operations.PlanInput{
			Operation: "recover-operation",
			Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
			Preconditions: []operations.Precondition{{
				RepositoryID:        current.repositoryID,
				HeadOID:             current.observation.HeadOID,
				WorktreeFingerprint: current.observation.WorktreeFingerprint,
				ManifestDigest:      current.observation.ManifestDigest,
				PolicyDigest:        current.observation.PolicyDigest,
			}},
			Steps: []operations.Step{{
				StepID: "recover-operation", RepositoryID: current.repositoryID,
				Action: recoveryAction, RequiresApproval: true,
				Compensation: operations.Compensation{Mode: "manual"},
				Parameters: map[string]any{"recovery": map[string]any{
					"operation_id": operationID, "state_digest": current.snapshot.Digest,
					"mode": decision.Mode, "reason": decision.Reason,
					"lock_count": len(current.snapshot.Locks),
				}},
			}},
			ApprovalClass: "operation-recovery",
		},
	)
	if err != nil {
		return domain.InternalError("gds recover operation plan", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		recoveryObserver{services: services, root: current.root, store: store, operationID: operationID},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	engine.LockScope = "operation-recovery"
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope("gds recover operation plan", err)
	}
	data.Plan = &plan
	envelope := domain.Success("gds recover operation plan", data)
	envelope.Scope["repository_id"] = current.repositoryID
	envelope.OperationID = operationID
	return envelope
}

func (services *Services) ApplyOperationRecovery(
	ctx context.Context,
	path string,
	planID string,
	options RecoveryOperationOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return domain.NewEnvelope("gds recover operation apply", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_PLAN_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--apply requires an exact recovery plan id.",
		})
	}
	if finding := validateLocalOperationIdentity(ProjectionOperationOptions{
		DeviceID: options.DeviceID, SessionID: options.SessionID,
	}); finding != nil {
		return domain.NewEnvelope("gds recover operation apply", domain.ExitInput, nil, *finding)
	}
	_, store, finding := openOperationState(ctx, options.StatePath)
	if finding != nil {
		return domain.NewEnvelope("gds recover operation apply", domain.ExitInput, nil, *finding)
	}
	defer store.Close()
	planRecord, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operationStateError("gds recover operation apply", planID, err)
	}
	plan, err := operations.DecodePlan(planRecord.Body)
	if err != nil || plan.Operation != "recover-operation" || len(plan.Steps) != 1 {
		return domain.NewEnvelope("gds recover operation apply", domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_RECOVERY_PLAN_INVALID", Severity: domain.SeverityCritical,
			Message: "Stored plan is not an exact operation-recovery plan.",
		})
	}
	parameters, err := recoveryParameters(plan.Steps[0])
	if err != nil {
		return domain.NewEnvelope("gds recover operation apply", domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_RECOVERY_PLAN_INVALID", Severity: domain.SeverityCritical,
			Message: err.Error(),
		})
	}
	current, findings := services.recoveryOperationContext(
		ctx, path, store, parameters.OperationID,
	)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds recover operation apply", classifyFindings(findings), nil, findings...,
		)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		recoveryObserver{services: services, root: current.root, store: store, operationID: parameters.OperationID},
		map[string]operations.ActionHandler{
			recoveryAction: recoveryHandler{store: store, now: services.Now},
		},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	engine.LockScope = "operation-recovery"
	result, err := engine.Apply(ctx, planID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope("gds recover operation apply", err)
		envelope.Data = result
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		envelope.OperationID = result.OperationID
		return envelope
	}
	envelope := domain.Success("gds recover operation apply", result)
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.OperationID = result.OperationID
	envelope.Scope["repository_id"] = current.repositoryID
	return envelope
}

func (services *Services) VerifyOperationRecovery(
	ctx context.Context,
	path string,
	recoveryOperationID string,
	options RecoveryOperationOptions,
) domain.Envelope {
	if strings.TrimSpace(recoveryOperationID) == "" {
		return domain.NewEnvelope("gds recover operation verify", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_OPERATION_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--verify requires the exact recovery operation id.",
		})
	}
	if finding := validateLocalOperationIdentity(ProjectionOperationOptions{
		DeviceID: options.DeviceID, SessionID: options.SessionID,
	}); finding != nil {
		return domain.NewEnvelope("gds recover operation verify", domain.ExitInput, nil, *finding)
	}
	_, store, finding := openOperationState(ctx, options.StatePath)
	if finding != nil {
		return domain.NewEnvelope("gds recover operation verify", domain.ExitInput, nil, *finding)
	}
	defer store.Close()
	recoveryOperation, err := store.GetOperation(ctx, recoveryOperationID)
	if err != nil {
		return operationStateError("gds recover operation verify", recoveryOperationID, err)
	}
	planRecord, err := store.GetPlan(ctx, recoveryOperation.PlanID)
	if err != nil {
		return operationStateError("gds recover operation verify", recoveryOperationID, err)
	}
	plan, err := operations.DecodePlan(planRecord.Body)
	if err != nil || plan.Operation != "recover-operation" || len(plan.Steps) != 1 {
		return domain.NewEnvelope("gds recover operation verify", domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_RECOVERY_PLAN_INVALID", Severity: domain.SeverityCritical,
			Message: "Recovery operation does not reference a valid recovery plan.",
		})
	}
	parameters, err := recoveryParameters(plan.Steps[0])
	if err != nil {
		return domain.NewEnvelope("gds recover operation verify", domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_RECOVERY_PLAN_INVALID", Severity: domain.SeverityCritical,
			Message: err.Error(),
		})
	}
	current, findings := services.recoveryOperationContext(
		ctx, path, store, parameters.OperationID,
	)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds recover operation verify", classifyFindings(findings), nil, findings...,
		)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		recoveryObserver{services: services, root: current.root, store: store, operationID: parameters.OperationID},
		map[string]operations.ActionHandler{
			recoveryAction: recoveryHandler{store: store, now: services.Now},
		},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	engine.LockScope = "operation-recovery"
	result, err := engine.Verify(ctx, recoveryOperationID)
	if err != nil {
		envelope := operationFailureEnvelope("gds recover operation verify", err)
		envelope.OperationID = recoveryOperationID
		return envelope
	}
	envelope := domain.Success("gds recover operation verify", result)
	envelope.OperationID = recoveryOperationID
	envelope.Scope["repository_id"] = current.repositoryID
	return envelope
}

func (services *Services) recoveryOperationContext(
	ctx context.Context,
	path string,
	store *state.Store,
	operationID string,
) (recoveryContext, []domain.Finding) {
	root, anchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return recoveryContext{}, findings
	}
	compiled := services.Compiler.CompileDirectory(root, anchor, compiler.DevelopmentBundleVersion)
	if len(compiled.Findings) != 0 {
		return recoveryContext{}, compiled.Findings
	}
	repositoryInfo, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return recoveryContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	headOID, err := services.Git.HeadOID(ctx, repositoryInfo.WorktreeRoot)
	if err != nil {
		return recoveryContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	manifestDigest, err := fileDigest(repositoryInfo.WorktreeRoot + "/.gds/repository.yaml")
	if err != nil {
		return recoveryContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	snapshot, err := store.RecoverySnapshot(ctx, operationID)
	if err != nil {
		return recoveryContext{}, []domain.Finding{dependencyFinding(operationID, err)}
	}
	planRecord, err := store.GetPlan(ctx, snapshot.PlanID)
	if err != nil {
		return recoveryContext{}, []domain.Finding{dependencyFinding(operationID, err)}
	}
	plan, err := operations.DecodePlan(planRecord.Body)
	if err != nil {
		return recoveryContext{}, []domain.Finding{dependencyFinding(operationID, err)}
	}
	steps, err := store.ListSteps(ctx, operationID)
	if err != nil {
		return recoveryContext{}, []domain.Finding{dependencyFinding(operationID, err)}
	}
	events, err := store.ListEvents(ctx, operationID)
	if err != nil {
		return recoveryContext{}, []domain.Finding{dependencyFinding(operationID, err)}
	}
	findings = inspectOperationIntegrity(planRecord, plan, state.OperationRecord{
		OperationID: snapshot.OperationID, PlanID: snapshot.PlanID,
		Operation: snapshot.Operation, Status: snapshot.OperationStatus,
	}, steps, events, services)
	if len(plan.Scope.Repositories) != 1 || plan.Scope.Repositories[0] != anchor.Repository.ID {
		findings = append(findings, domain.Finding{
			Code: "GDS_RECOVERY_SCOPE_NOT_MATERIALIZED", Severity: domain.SeverityHigh,
			Message: "Recovery currently requires the exact affected repository as the current local scope.",
			Evidence: map[string]any{
				"current_repository_id":  anchor.Repository.ID,
				"operation_repositories": plan.Scope.Repositories,
			},
		})
	}
	return recoveryContext{
		root: repositoryInfo.WorktreeRoot, repositoryID: anchor.Repository.ID,
		snapshot: snapshot, plan: plan,
		observation: operations.Observation{
			RepositoryID: anchor.Repository.ID, HeadOID: headOID,
			WorktreeFingerprint: snapshot.Digest, ManifestDigest: manifestDigest,
			PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, findings
}

func decideRecovery(
	snapshot state.RecoverySnapshot,
	plan operations.Plan,
	now time.Time,
	deviceID string,
) RecoveryDecision {
	decision := RecoveryDecision{
		Classification: "manual-review-required", Reason: "evidence-insufficient",
		StateDigest: snapshot.Digest, Owners: []RecoveryOwnerEvidence{},
		Compensations: []RecoveryCompensation{}, Blockers: []string{},
	}
	if len(snapshot.Locks) == 0 {
		decision.Blockers = append(decision.Blockers, "owner-lock-missing")
	}
	allowedRepositories := map[string]struct{}{}
	for _, repositoryID := range plan.Scope.Repositories {
		allowedRepositories[repositoryID] = struct{}{}
	}
	for _, lock := range snapshot.Locks {
		owner := RecoveryOwnerEvidence{
			LockID: lock.LockID, DeviceID: lock.DeviceID, PID: lock.PID,
			LeaseState: "active", ProcessState: "not-inspected",
		}
		if lock.LeaseExpiresAt.Before(now) {
			owner.LeaseState = "expired"
		} else {
			decision.Blockers = append(decision.Blockers, "lock-lease-active")
		}
		if lock.Scope != "repository" {
			decision.Blockers = append(decision.Blockers, "unexpected-lock-scope")
		}
		if _, allowed := allowedRepositories[lock.ScopeID]; !allowed {
			decision.Blockers = append(decision.Blockers, "lock-scope-outside-plan")
		}
		if lock.DeviceID != deviceID {
			owner.ProcessState = "remote-not-proven"
			decision.Blockers = append(decision.Blockers, "lock-owner-other-device")
		} else {
			alive, err := processAlive(lock.PID)
			switch {
			case err != nil:
				owner.ProcessState = "unknown"
				decision.Blockers = append(decision.Blockers, "lock-owner-process-unknown")
			case alive:
				owner.ProcessState = "alive"
				decision.Blockers = append(decision.Blockers, "lock-owner-process-alive")
			default:
				owner.ProcessState = "dead"
			}
		}
		decision.Owners = append(decision.Owners, owner)
	}
	completedOrFailed := 0
	planSteps := map[string]operations.Step{}
	for _, step := range plan.Steps {
		planSteps[step.StepID] = step
	}
	for _, step := range snapshot.Steps {
		switch step.Status {
		case "applying", "compensating":
			decision.Blockers = append(decision.Blockers, "unknown-step-side-effects")
		case "succeeded", "failed", "compensated":
			completedOrFailed++
		}
		if step.Status != "succeeded" && step.Status != "failed" {
			continue
		}
		declared, found := planSteps[step.StepID]
		if !found {
			decision.Compensations = append(decision.Compensations, RecoveryCompensation{
				StepID: step.StepID, Mode: "unknown", Status: "manual-review",
			})
			continue
		}
		compensation := RecoveryCompensation{
			StepID: step.StepID, Mode: declared.Compensation.Mode,
			Action: declared.Compensation.Action,
		}
		switch declared.Compensation.Mode {
		case "explicit-plan":
			compensation.Status = "requires-new-approved-plan"
		case "manual":
			compensation.Status = "manual-only"
		default:
			compensation.Status = "not-available"
		}
		decision.Compensations = append(decision.Compensations, compensation)
	}
	if len(decision.Blockers) == 0 {
		switch {
		case snapshot.OperationStatus == "applying" && completedOrFailed == 0:
			decision.Classification = "safe-abort"
			decision.Mode = "abort-interrupted"
			decision.Reason = "owner-process-dead"
			decision.Automatable = true
		case snapshot.OperationStatus == "applying":
			decision.Classification = "safe-close-partial"
			decision.Mode = "close-partial"
			decision.Reason = "owner-process-dead"
			decision.Automatable = true
		case snapshot.OperationStatus == "succeeded" || snapshot.OperationStatus == "failed" ||
			snapshot.OperationStatus == "partial" || snapshot.OperationStatus == "blocked":
			decision.Classification = "safe-release-stale-locks"
			decision.Mode = "release-stale-locks"
			decision.Reason = "terminal-operation-stale-lock"
			decision.Automatable = true
		default:
			decision.Blockers = append(decision.Blockers, "operation-state-unsupported")
		}
	}
	digest, err := canonicaljson.Digest(struct {
		Classification string                  `json:"classification"`
		Mode           string                  `json:"mode,omitempty"`
		Reason         string                  `json:"reason"`
		Automatable    bool                    `json:"automatable"`
		StateDigest    string                  `json:"state_digest"`
		Owners         []RecoveryOwnerEvidence `json:"owners"`
		Compensations  []RecoveryCompensation  `json:"compensations"`
		Blockers       []string                `json:"blockers"`
	}{
		decision.Classification, decision.Mode, decision.Reason,
		decision.Automatable, decision.StateDigest, decision.Owners,
		decision.Compensations, decision.Blockers,
	})
	if err == nil {
		decision.DecisionDigest = digest
	}
	return decision
}

func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, fmt.Errorf("invalid process id %d", pid)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}
