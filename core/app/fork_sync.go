package app

import (
	"context"
	"errors"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	forkworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/fork"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type ForkSyncOptions struct {
	ProjectionOperationOptions
}

type ForkSyncPlanData struct {
	Plan      operations.Plan              `json:"plan"`
	StatePath string                       `json:"state_path"`
	Evidence  gitprovider.ForkSyncEvidence `json:"evidence"`
}

type forkSyncContext struct {
	root        string
	anchor      domain.RepositoryAnchor
	evidence    gitprovider.ForkSyncEvidence
	observation operations.Observation
}

type forkSyncObserver struct {
	services *Services
	root     string
}

func (observer forkSyncObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.forkSyncContext(ctx, observer.root)
	if len(findings) != 0 || current.anchor.Repository.ID != repositoryID {
		return operations.Observation{}, errors.New("fork sync precondition is no longer proven")
	}
	return current.observation, nil
}

func (services *Services) PlanForkSync(
	ctx context.Context,
	path string,
	options ForkSyncOptions,
) domain.Envelope {
	const command = "gds fork sync plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, findings := services.forkSyncContext(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	if current.evidence.OriginOID == current.evidence.UpstreamOID {
		envelope := domain.Success(command, map[string]any{
			"status": "up-to-date", "evidence": current.evidence, "plan_created": false,
		})
		envelope.Scope["repository_id"] = current.anchor.Repository.ID
		return envelope
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError(command, err)
	}
	plan, err := operations.NewPlan(planID, now, now.Add(projectionPlanLifetime), operations.PlanInput{
		Operation: "sync-fork",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			WorktreeFingerprint:  current.observation.WorktreeFingerprint,
			RemoteDefaultOID:     current.observation.RemoteDefaultOID,
			RemoteEvidenceDigest: current.observation.RemoteEvidenceDigest,
			ManifestDigest:       current.observation.ManifestDigest, PolicyDigest: current.observation.PolicyDigest,
		}},
		Steps: []operations.Step{{
			StepID: "sync-fork-fast-forward", RepositoryID: current.observation.RepositoryID,
			Action: forkworkflow.SyncAction, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "explicit-plan", Action: "restore-fork-sync"},
			Parameters: forkworkflow.SyncStepParameters(
				current.evidence, current.anchor.Fork.Policy, current.anchor.Fork.PreserveForkCommits,
			),
		}},
		ApprovalClass: "sync-fork-fast-forward",
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, forkSyncObserver{services: services, root: current.root},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	envelope := domain.Success(command, ForkSyncPlanData{
		Plan: plan, StatePath: statePath, Evidence: current.evidence,
	})
	envelope.Scope["repository_id"] = current.anchor.Repository.ID
	return envelope
}

func (services *Services) ApplyForkSync(
	ctx context.Context,
	planID string,
	options ForkSyncOptions,
) domain.Envelope {
	const command = "gds fork sync apply"
	if strings.TrimSpace(planID) == "" {
		return syncIdentifierRequired(command, "plan", "--apply")
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	plan, parameters, err := loadForkSyncPlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return forkSyncPlanInvalid(command)
	}
	handler, err := forkworkflow.NewSyncHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		forkSyncObserver{services: services, root: parameters.WorktreeRoot},
		map[string]operations.ActionHandler{forkworkflow.SyncAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, plan.PlanID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope(command, err)
		envelope.Data = result
		envelope.OperationID = result.OperationID
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		return envelope
	}
	envelope := domain.Success(command, result)
	envelope.OperationID = result.OperationID
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.Scope["repository_id"] = plan.Scope.Repositories[0]
	return envelope
}

func (services *Services) VerifyForkSync(
	ctx context.Context,
	operationID string,
	options ForkSyncOptions,
) domain.Envelope {
	const command = "gds fork sync verify"
	if strings.TrimSpace(operationID) == "" {
		return syncIdentifierRequired(command, "operation", "--verify")
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	operation, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return forkSyncPlanInvalid(command)
	}
	plan, _, err := loadForkSyncPlan(ctx, store, operation.PlanID, services.Schemas)
	if err != nil {
		return forkSyncPlanInvalid(command)
	}
	handler, err := forkworkflow.NewSyncHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, forkSyncObserver{},
		map[string]operations.ActionHandler{forkworkflow.SyncAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope(command, err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success(command, result)
	envelope.OperationID = operationID
	envelope.Scope["repository_id"] = plan.Scope.Repositories[0]
	return envelope
}

func (services *Services) forkSyncContext(
	ctx context.Context,
	path string,
) (forkSyncContext, []domain.Finding) {
	estateRoot, repositoryAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return forkSyncContext{}, findings
	}
	if repositoryAnchor.Fork == nil ||
		(repositoryAnchor.Fork.Policy != "upstream-tracking" && repositoryAnchor.Fork.Policy != "maintained-patch") ||
		!repositoryAnchor.Fork.PreserveForkCommits {
		return forkSyncContext{}, []domain.Finding{{
			Code: "GDS_FORK_SYNC_POLICY_BLOCKED", Severity: domain.SeverityHigh,
			Message: "Automatic fork sync requires upstream-tracking or maintained-patch with commit preservation.",
		}}
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return forkSyncContext{}, []domain.Finding{{
			Code: "GDS_FORK_SYNC_BOUNDARY_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	evidence, err := services.GitMutations.ObserveForkFastForward(
		ctx, info.WorktreeRoot, "refs/heads/"+repositoryAnchor.Fork.SyncBranch,
	)
	if err != nil {
		return forkSyncContext{}, []domain.Finding{{
			Code: "GDS_FORK_SYNC_UNSAFE", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	compiled := services.Compiler.CompileDirectory(
		estateRoot, repositoryAnchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return forkSyncContext{}, compiled.Findings
	}
	observedAnchor, err := anchor.Observe(info.WorktreeRoot)
	if err != nil || observedAnchor.File.State != "regular" {
		return forkSyncContext{}, []domain.Finding{{
			Code: "GDS_FORK_SYNC_ANCHOR_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "Fork repository anchor could not be proven.",
		}}
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Evidence gitprovider.ForkSyncEvidence `json:"evidence"`
		Policy   domain.ForkPolicy            `json:"policy"`
	}{evidence, *repositoryAnchor.Fork})
	if err != nil {
		return forkSyncContext{}, []domain.Finding{{
			Code: "GDS_FORK_SYNC_FINGERPRINT_FAILED", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	return forkSyncContext{
		root: info.WorktreeRoot, anchor: repositoryAnchor, evidence: evidence,
		observation: operations.Observation{
			RepositoryID: repositoryAnchor.Repository.ID, HeadOID: evidence.HeadOID,
			WorktreeFingerprint: fingerprint, RemoteDefaultOID: evidence.OriginOID,
			RemoteEvidenceDigest: fingerprint, ManifestDigest: observedAnchor.File.ContentDigest,
			PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func loadForkSyncPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, forkworkflow.SyncParameters, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, forkworkflow.SyncParameters{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != "sync-fork" || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) != 1 || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, forkworkflow.SyncParameters{}, errors.New("stored plan is not a valid fork sync")
	}
	parameters, err := forkworkflow.StepSyncParameters(plan.Steps[0])
	return plan, parameters, err
}

func forkSyncPlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_FORK_SYNC_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable fork sync.",
	})
}
