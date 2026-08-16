package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	approvalcontract "github.com/NDDev-OpenNetwork/github-device-sync/core/approval"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

const (
	StateInitializeAction = "initialize-state"
	StateMigrateAction    = "migrate-state"
)

type StateLifecyclePlan struct {
	SchemaVersion   int    `json:"schema_version"`
	PlanID          string `json:"plan_id"`
	Action          string `json:"action"`
	StatePath       string `json:"state_path"`
	ExpectedState   string `json:"expected_state"`
	ExpectedVersion int    `json:"expected_version"`
	ExpectedDigest  string `json:"expected_digest,omitempty"`
	TargetVersion   int    `json:"target_version"`
	BackupPath      string `json:"backup_path,omitempty"`
	Required        bool   `json:"required"`
	PlanDigest      string `json:"plan_digest"`
}

type StateLifecycleResult struct {
	Plan         StateLifecyclePlan      `json:"plan"`
	Snapshot     state.LifecycleSnapshot `json:"snapshot,omitempty"`
	Migration    *state.MigrationReport  `json:"migration,omitempty"`
	Evidence     state.LifecycleEvidence `json:"evidence,omitempty"`
	KillSwitches operations.KillSwitches `json:"kill_switches"`
	Status       string                  `json:"status"`
}

func (services *Services) PlanStateInitialize(
	_ context.Context,
	requestedPath string,
) domain.Envelope {
	plan, err := buildStateLifecyclePlan(StateInitializeAction, requestedPath)
	if err != nil {
		return stateLifecycleError("gds state initialize plan", requestedPath, err)
	}
	return domain.Success("gds state initialize plan", StateLifecycleResult{
		Plan: plan, Status: "planned",
	})
}

func (services *Services) PlanStateMigration(
	ctx context.Context,
	requestedPath string,
) domain.Envelope {
	plan, err := buildStateMigrationPlan(ctx, requestedPath)
	if err != nil {
		return stateLifecycleError("gds state migrate plan", requestedPath, err)
	}
	status := "planned"
	if !plan.Required {
		status = "already-current"
	}
	return domain.Success("gds state migrate plan", StateLifecycleResult{
		Plan: plan, Status: status,
	})
}

func (services *Services) ApplyStateLifecycle(
	ctx context.Context,
	action string,
	requestedPath string,
	expectedPlanDigest string,
	approvalPath string,
	enablementPlanID string,
) domain.Envelope {
	command := "gds state " + strings.TrimSuffix(action, "-state") + " apply"
	switches, switchErr := operations.LoadKillSwitches(os.LookupEnv)
	if switchErr != nil {
		return domain.NewEnvelope(command, domain.ExitSecurity, StateLifecycleResult{
			KillSwitches: switches, Status: "blocked",
		}, domain.Finding{
			Code: "GDS_KILL_SWITCH_INVALID", Severity: domain.SeverityCritical,
			Message: "A kill-switch value is invalid; state mutations fail closed.",
		})
	}
	if switches.MutationsDisabled {
		return domain.NewEnvelope(command, domain.ExitPolicy, StateLifecycleResult{
			KillSwitches: switches, Status: "blocked",
		}, domain.Finding{
			Code: "GDS_MUTATIONS_DISABLED", Severity: domain.SeverityCritical,
			Message: "Global GDS mutations are disabled; state was not changed.",
		})
	}
	approvalRecord, approvalErr := approvalcontract.LoadRecord(approvalPath)
	if approvalErr != nil {
		return domain.NewEnvelope(command, domain.ExitApproval, StateLifecycleResult{
			KillSwitches: switches, Status: "planned",
		}, domain.Finding{
			Code: "GDS_SIGNED_APPROVAL_REQUIRED", Severity: domain.SeverityHigh,
			Message: "State lifecycle apply requires a signed exact-plan approval JSON file.",
		})
	}
	if !strings.HasPrefix(expectedPlanDigest, "sha256:") {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_STATE_PLAN_DIGEST_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--apply requires the exact SHA-256 digest returned by the plan command.",
		})
	}
	var plan StateLifecyclePlan
	var planErr error
	if action == StateInitializeAction {
		plan, planErr = buildStateLifecyclePlan(action, requestedPath)
	} else if action == StateMigrateAction {
		plan, planErr = buildStateMigrationPlan(ctx, requestedPath)
	} else {
		planErr = fmt.Errorf("unsupported state lifecycle action %q", action)
	}
	if planErr != nil {
		return stateLifecycleError(command, requestedPath, planErr)
	}
	if !plan.Required || plan.PlanDigest != expectedPlanDigest {
		return domain.NewEnvelope(command, domain.ExitStale, StateLifecycleResult{
			Plan: plan, KillSwitches: switches, Status: "stale",
		}, domain.Finding{
			Code: "GDS_STALE_PLAN", Severity: domain.SeverityHigh,
			Message: "Current state no longer matches the exact lifecycle plan; no state mutation occurred.",
			Evidence: map[string]any{
				"expected_plan_digest": expectedPlanDigest,
				"observed_plan_digest": plan.PlanDigest,
			},
		})
	}
	if enablementPlanID != plan.PlanID {
		return domain.NewEnvelope(command, domain.ExitApproval, StateLifecycleResult{
			Plan: plan, KillSwitches: switches, Status: "planned",
		}, domain.Finding{Code: "GDS_PLAN_ENABLEMENT_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--enable must explicitly name the exact current state lifecycle plan ID."})
	}
	policy, err := trust.LoadPolicy(os.Getenv("GDS_TRUST_POLICY_FILE"))
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitSecurity, StateLifecycleResult{
			Plan: plan, KillSwitches: switches, Status: "planned",
		}, domain.Finding{Code: "GDS_APPROVAL_VERIFIER_UNAVAILABLE", Severity: domain.SeverityHigh,
			Message: "Offline approval trust policy is unavailable."})
	}
	scopeDigest, err := stateLifecycleScopeDigest(plan)
	if err != nil {
		return domain.InternalError(command, err)
	}
	verifier := approvalcontract.Verifier{Trust: trust.Verifier{Policy: policy}, MaximumTTL: 24 * time.Hour,
		MaximumFuture: 5 * time.Minute, Now: services.Now}
	if err := verifier.Verify(approvalRecord, approvalcontract.Expectation{PlanID: plan.PlanID,
		PlanDigest: plan.PlanDigest, ApprovalClass: "state-lifecycle", ScopeDigest: scopeDigest,
		RequiredRole: "mutation-approver"}); err != nil {
		return domain.NewEnvelope(command, domain.ExitApproval, StateLifecycleResult{
			Plan: plan, KillSwitches: switches, Status: "planned",
		}, domain.Finding{Code: "GDS_APPROVAL_SIGNATURE_INVALID", Severity: domain.SeverityHigh,
			Message: "Signed approval does not authorize the exact state lifecycle plan."})
	}
	approvalDigest, err := approvalRecord.Digest()
	if err != nil {
		return domain.InternalError(command, err)
	}
	evidence := state.LifecycleEvidence{
		Action: action, PlanDigest: plan.PlanDigest,
		ApprovalDigest: approvalDigest,
		AppliedAt:      services.Now().UTC(),
	}
	result := StateLifecycleResult{
		Plan: plan, Evidence: evidence, KillSwitches: switches, Status: "applying",
	}
	if action == StateInitializeAction {
		store, err := state.InitializeWithEvidence(ctx, plan.StatePath, evidence)
		if err != nil {
			return stateLifecycleApplyError(command, result, err, false)
		}
		if err := store.Close(); err != nil {
			return stateLifecycleApplyError(command, result, err, true)
		}
		snapshot, err := state.Snapshot(ctx, plan.StatePath)
		if err != nil {
			return stateLifecycleApplyError(command, result, err, true)
		}
		result.Snapshot = snapshot
		result.Evidence.FromVersion = 0
		result.Evidence.ToVersion = state.CurrentSchemaVersion()
		result.Status = "succeeded"
		envelope := domain.Success(command, result)
		envelope.Mutation.Attempted = true
		envelope.Mutation.Completed = true
		return envelope
	}
	before := state.LifecycleSnapshot{
		Path: plan.StatePath, SchemaVersion: plan.ExpectedVersion,
		LogicalDigest: plan.ExpectedDigest, Integrity: "pass",
	}
	report, err := state.Migrate(ctx, plan.StatePath, before, plan.BackupPath, evidence)
	if err != nil {
		return stateLifecycleApplyError(command, result, err, true)
	}
	result.Migration = &report
	result.Snapshot = report.After
	result.Evidence = report.Evidence
	result.Status = "succeeded"
	envelope := domain.Success(command, result)
	envelope.Mutation.Attempted = true
	envelope.Mutation.Completed = true
	return envelope
}

func (services *Services) VerifyStateLifecycle(
	ctx context.Context,
	requestedPath string,
	expectedPlanDigest string,
) domain.Envelope {
	command := "gds state lifecycle verify"
	path, err := resolveStatePath(requestedPath)
	if err != nil {
		return stateLifecycleError(command, requestedPath, err)
	}
	store, err := state.Open(ctx, path)
	if err != nil {
		return stateLifecycleError(command, path, err)
	}
	defer store.Close()
	evidence, err := store.LifecycleEvidence(ctx)
	if err != nil {
		return stateLifecycleError(command, path, err)
	}
	if evidence.PlanDigest != expectedPlanDigest {
		return domain.NewEnvelope(command, domain.ExitStale, StateLifecycleResult{
			Evidence: evidence, Status: "stale",
		}, domain.Finding{
			Code: "GDS_STATE_LIFECYCLE_EVIDENCE_MISMATCH", Severity: domain.SeverityHigh,
			Message: "The latest durable lifecycle evidence does not match the requested plan digest.",
		})
	}
	snapshot, err := state.Snapshot(ctx, path)
	if err != nil {
		return stateLifecycleError(command, path, err)
	}
	switches, switchErr := operations.LoadKillSwitches(os.LookupEnv)
	if switchErr != nil {
		return stateLifecycleError(command, path, switchErr)
	}
	return domain.Success(command, StateLifecycleResult{
		Snapshot: snapshot, Evidence: evidence, KillSwitches: switches, Status: "verified",
	})
}

func buildStateLifecyclePlan(action string, requestedPath string) (StateLifecyclePlan, error) {
	path, err := resolveStatePath(requestedPath)
	if err != nil {
		return StateLifecyclePlan{}, err
	}
	if action != StateInitializeAction {
		return StateLifecyclePlan{}, fmt.Errorf("unsupported state lifecycle action %q", action)
	}
	if _, err := os.Lstat(path); err == nil {
		return StateLifecyclePlan{}, fmt.Errorf("state database already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return StateLifecyclePlan{}, fmt.Errorf("inspect state path: %w", err)
	}
	plan := StateLifecyclePlan{
		SchemaVersion: 1, Action: action, StatePath: path,
		ExpectedState: "missing", TargetVersion: state.CurrentSchemaVersion(), Required: true,
	}
	return digestStateLifecyclePlan(plan)
}

func buildStateMigrationPlan(
	ctx context.Context,
	requestedPath string,
) (StateLifecyclePlan, error) {
	path, err := resolveStatePath(requestedPath)
	if err != nil {
		return StateLifecyclePlan{}, err
	}
	snapshot, err := state.Snapshot(ctx, path)
	if err != nil {
		return StateLifecyclePlan{}, err
	}
	if snapshot.SchemaVersion > state.CurrentSchemaVersion() {
		return StateLifecyclePlan{}, fmt.Errorf(
			"state schema version %d is newer than supported %d",
			snapshot.SchemaVersion, state.CurrentSchemaVersion(),
		)
	}
	plan := StateLifecyclePlan{
		SchemaVersion: 1, Action: StateMigrateAction, StatePath: path,
		ExpectedState: "present", ExpectedVersion: snapshot.SchemaVersion,
		ExpectedDigest: snapshot.LogicalDigest, TargetVersion: state.CurrentSchemaVersion(),
		Required: snapshot.SchemaVersion < state.CurrentSchemaVersion(),
	}
	if plan.Required {
		plan.BackupPath = state.DefaultBackupPath(snapshot)
	}
	return digestStateLifecyclePlan(plan)
}

func digestStateLifecyclePlan(plan StateLifecyclePlan) (StateLifecyclePlan, error) {
	plan.PlanID = ""
	plan.PlanDigest = ""
	seed, err := canonicaljson.Digest(plan)
	if err != nil {
		return StateLifecyclePlan{}, fmt.Errorf("seed state lifecycle plan identity: %w", err)
	}
	plan.PlanID = "plan_0" + strings.ToUpper(strings.TrimPrefix(seed, "sha256:"))[:25]
	digest, err := canonicaljson.Digest(struct {
		SchemaVersion   int    `json:"schema_version"`
		PlanID          string `json:"plan_id"`
		Action          string `json:"action"`
		StatePath       string `json:"state_path"`
		ExpectedState   string `json:"expected_state"`
		ExpectedVersion int    `json:"expected_version"`
		ExpectedDigest  string `json:"expected_digest,omitempty"`
		TargetVersion   int    `json:"target_version"`
		BackupPath      string `json:"backup_path,omitempty"`
		Required        bool   `json:"required"`
	}{
		plan.SchemaVersion, plan.PlanID, plan.Action, plan.StatePath, plan.ExpectedState,
		plan.ExpectedVersion, plan.ExpectedDigest, plan.TargetVersion,
		plan.BackupPath, plan.Required,
	})
	if err != nil {
		return StateLifecyclePlan{}, fmt.Errorf("digest state lifecycle plan: %w", err)
	}
	plan.PlanDigest = digest
	return plan, nil
}

func stateLifecycleScopeDigest(plan StateLifecyclePlan) (string, error) {
	return canonicaljson.Digest(struct {
		Action          string `json:"action"`
		StatePath       string `json:"state_path"`
		ExpectedState   string `json:"expected_state"`
		ExpectedVersion int    `json:"expected_version"`
		ExpectedDigest  string `json:"expected_digest,omitempty"`
		TargetVersion   int    `json:"target_version"`
	}{plan.Action, plan.StatePath, plan.ExpectedState, plan.ExpectedVersion, plan.ExpectedDigest, plan.TargetVersion})
}

func stateLifecycleError(command, path string, err error) domain.Envelope {
	class := domain.ExitInput
	code := "GDS_STATE_LIFECYCLE_INVALID"
	if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") {
		class = domain.ExitNotProven
		code = "GDS_STATE_NOT_INITIALIZED"
	}
	if errors.Is(err, state.ErrStateConflict) {
		class = domain.ExitStale
		code = "GDS_STALE_PLAN"
	}
	return domain.NewEnvelope(command, class, map[string]any{"path": path}, domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: err.Error(),
	})
}

func stateLifecycleApplyError(
	command string,
	result StateLifecycleResult,
	err error,
	mutationAttempted bool,
) domain.Envelope {
	class := domain.ExitInternal
	if errors.Is(err, state.ErrStateConflict) {
		class = domain.ExitStale
	}
	if mutationAttempted {
		class = domain.ExitPartial
	}
	envelope := domain.NewEnvelope(command, class, result, domain.Finding{
		Code: "GDS_STATE_LIFECYCLE_APPLY_FAILED", Severity: domain.SeverityCritical,
		Message: err.Error(),
	})
	envelope.Mutation.Attempted = mutationAttempted
	return envelope
}
