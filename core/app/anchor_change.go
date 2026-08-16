package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type AnchorChangePlanData struct {
	Plan      operations.Plan  `json:"plan"`
	StatePath string           `json:"state_path"`
	Target    string           `json:"target"`
	Candidate anchor.Candidate `json:"candidate"`
}

type anchorChangeContext struct {
	root        string
	candidate   anchor.Candidate
	observation operations.Observation
	file        anchor.FileObservation
}

type anchorChangeObserver struct {
	services  *Services
	root      string
	candidate anchor.Candidate
}

func (observer anchorChangeObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.anchorChangeContext(ctx, observer.root, observer.candidate)
	if len(findings) != 0 || current.candidate.Anchor.Repository.ID != repositoryID {
		return operations.Observation{}, errors.New("repository anchor change precondition is no longer proven")
	}
	return current.observation, nil
}

func (services *Services) planAnchorChange(
	ctx context.Context,
	command string,
	operation string,
	approvalClass string,
	path string,
	candidate anchor.Candidate,
	options ProjectionOperationOptions,
) domain.Envelope {
	if finding := validateLocalOperationIdentity(options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, findings := services.anchorChangeContext(ctx, path, candidate)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
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
		Operation: operation,
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			WorktreeFingerprint: current.observation.WorktreeFingerprint,
			ManifestDigest:      current.observation.ManifestDigest, PolicyDigest: current.observation.PolicyDigest,
		}},
		Steps: []operations.Step{{
			StepID: "materialize-repository-anchor", RepositoryID: current.observation.RepositoryID,
			Action: anchor.MaterializeAction, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "manual"},
			Parameters:   anchor.Parameters(current.root, current.file, candidate),
		}},
		ApprovalClass: approvalClass,
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		anchorChangeObserver{services: services, root: current.root, candidate: candidate},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	envelope := domain.Success(command, AnchorChangePlanData{
		Plan: plan, StatePath: statePath, Target: current.root, Candidate: candidate,
	})
	envelope.Scope["repository_id"] = current.observation.RepositoryID
	return envelope
}

func (services *Services) applyAnchorChange(
	ctx context.Context,
	command string,
	operation string,
	planID string,
	options ProjectionOperationOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return syncIdentifierRequired(command, "plan", "--apply")
	}
	if finding := validateLocalOperationIdentity(options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	plan, root, candidate, err := loadAnchorChangePlan(ctx, store, planID, operation, services.Schemas)
	if err != nil {
		return anchorChangePlanInvalid(command)
	}
	_, findings := services.anchorChangeContext(ctx, root, candidate)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	handler, err := anchor.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		anchorChangeObserver{services: services, root: root, candidate: candidate},
		map[string]operations.ActionHandler{anchor.MaterializeAction: handler},
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
	envelope.Scope["repository_id"] = candidate.Anchor.Repository.ID
	return envelope
}

func (services *Services) verifyAnchorChange(
	ctx context.Context,
	command string,
	operation string,
	operationID string,
	options ProjectionOperationOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return syncIdentifierRequired(command, "operation", "--verify")
	}
	if finding := validateLocalOperationIdentity(options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	operationRecord, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return anchorChangePlanInvalid(command)
	}
	plan, _, candidate, err := loadAnchorChangePlan(
		ctx, store, operationRecord.PlanID, operation, services.Schemas,
	)
	if err != nil {
		return anchorChangePlanInvalid(command)
	}
	handler, err := anchor.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, anchorChangeObserver{},
		map[string]operations.ActionHandler{anchor.MaterializeAction: handler},
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
	envelope.Scope["repository_id"] = candidate.Anchor.Repository.ID
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) anchorChangeContext(
	ctx context.Context,
	path string,
	candidate anchor.Candidate,
) (anchorChangeContext, []domain.Finding) {
	root, currentAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return anchorChangeContext{}, findings
	}
	if currentAnchor.Repository.ID != candidate.Anchor.Repository.ID {
		return anchorChangeContext{}, []domain.Finding{anchorChangeFinding(
			"GDS_ANCHOR_CHANGE_IDENTITY_MISMATCH", "Repository anchor changes cannot replace stable identity.", path,
		)}
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return anchorChangeContext{}, []domain.Finding{anchorChangeFinding(
			"GDS_ANCHOR_CHANGE_BOUNDARY_NOT_PROVEN", err.Error(), path,
		)}
	}
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil || status.Head.Mode != "branch" || status.Head.OID == "" ||
		!checkoutStatusIsClean(status) {
		return anchorChangeContext{}, []domain.Finding{anchorChangeFinding(
			"GDS_ANCHOR_CHANGE_GIT_STATE_UNSAFE",
			"Repository anchor changes require a clean attached checkout.", info.WorktreeRoot,
		)}
	}
	observed, err := anchor.Observe(info.WorktreeRoot)
	if err != nil || observed.File.State != "regular" {
		return anchorChangeContext{}, []domain.Finding{anchorChangeFinding(
			"GDS_ANCHOR_CHANGE_SOURCE_NOT_PROVEN", "Current repository anchor is unavailable.", info.WorktreeRoot,
		)}
	}
	currentCompiled := services.Compiler.CompileDirectory(root, currentAnchor, compiler.DevelopmentBundleVersion)
	if len(currentCompiled.Findings) != 0 {
		return anchorChangeContext{}, currentCompiled.Findings
	}
	candidateCompiled := services.Compiler.CompileDirectory(root, candidate.Anchor, compiler.DevelopmentBundleVersion)
	if len(candidateCompiled.Findings) != 0 {
		return anchorChangeContext{}, candidateCompiled.Findings
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Head      gitprovider.HeadState   `json:"head"`
		Branch    gitprovider.BranchState `json:"branch"`
		Changes   gitprovider.ChangeState `json:"changes"`
		Anchor    anchor.FileObservation  `json:"anchor"`
		Candidate string                  `json:"candidate"`
	}{status.Head, status.Branch, status.Changes, observed.File, candidate.Digest})
	if err != nil {
		return anchorChangeContext{}, []domain.Finding{anchorChangeFinding(
			"GDS_ANCHOR_CHANGE_FINGERPRINT_FAILED", err.Error(), info.WorktreeRoot,
		)}
	}
	return anchorChangeContext{
		root: info.WorktreeRoot, candidate: candidate, file: observed.File,
		observation: operations.Observation{
			RepositoryID: currentAnchor.Repository.ID, HeadOID: status.Head.OID,
			WorktreeFingerprint: fingerprint, ManifestDigest: observed.File.ContentDigest,
			PolicyDigest: currentCompiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func loadAnchorChangePlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	operation string,
	schemas *validation.Set,
) (operations.Plan, string, anchor.Candidate, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, "", anchor.Candidate{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != operation || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) != 1 || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, "", anchor.Candidate{}, fmt.Errorf("stored plan is not a valid %s plan", operation)
	}
	root, candidate, err := anchor.StepCandidate(plan.Steps[0], schemas)
	return plan, root, candidate, err
}

func anchorChangePlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_ANCHOR_CHANGE_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable repository anchor change.",
	})
}

func anchorChangeFinding(code string, message string, path string) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: map[string]any{"path": path},
	}
}
