package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
)

const harnessPlanLifetime = 15 * time.Minute

type HarnessOperationOptions struct {
	ProjectionOperationOptions
	HarnessID          string
	TargetRoot         string
	RollbackSourceRoot string
	SkillProfile       string
	Scope              string
}

type HarnessPlanData struct {
	Plan      operations.Plan     `json:"plan"`
	StatePath string              `json:"state_path"`
	Adapter   harness.AdapterPlan `json:"adapter"`
}

type harnessOperationContext struct {
	root         string
	repositoryID string
	targetRoot   string
	candidate    harness.AdapterCandidate
	previous     harness.AdapterCandidate
	adapterPlan  harness.AdapterPlan
	observation  operations.Observation
	action       string
}

type harnessObserver struct {
	services  *Services
	path      string
	operation string
	options   HarnessOperationOptions
}

func (observer harnessObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.harnessOperationContext(
		ctx, observer.path, observer.operation, observer.options,
	)
	if len(findings) != 0 {
		return operations.Observation{}, fmt.Errorf(
			"harness precondition returned %d findings", len(findings),
		)
	}
	if current.repositoryID != repositoryID {
		return operations.Observation{}, fmt.Errorf(
			"repository identity changed from %s to %s", repositoryID, current.repositoryID,
		)
	}
	return current.observation, nil
}

func (services *Services) PlanHarnessOperation(
	ctx context.Context,
	path string,
	operation string,
	options HarnessOperationOptions,
) domain.Envelope {
	command := "gds harness " + operation + " plan"
	if finding := validateHarnessOperationInput(operation, options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, findings := services.harnessOperationContext(ctx, path, operation, options)
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
	plan, err := operations.NewPlan(
		planID, now, now.Add(harnessPlanLifetime), operations.PlanInput{
			Operation: "harness-" + operation,
			Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
			Preconditions: []operations.Precondition{{
				RepositoryID:        current.observation.RepositoryID,
				HeadOID:             current.observation.HeadOID,
				WorktreeFingerprint: current.observation.WorktreeFingerprint,
				ManifestDigest:      current.observation.ManifestDigest,
				PolicyDigest:        current.observation.PolicyDigest,
			}},
			Steps: []operations.Step{{
				StepID: operation + "-harness-adapter", RepositoryID: current.repositoryID,
				// Deliberately still approval-gated, unlike the projection write:
				// an adapter materializes into an arbitrary --target-root on the
				// device, outside this repository, over trees that hold user-owned
				// files. That is a different risk class from writing a digest-bound
				// projection inside the repository.
				Action: current.action, RequiresApproval: true,
				Compensation: operations.Compensation{Mode: "manual"},
				Parameters:   harness.AdapterParameters(current.adapterPlan),
			}},
			ApprovalClass: "local-harness-adapter-write",
		},
	)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		harnessObserver{services: services, path: path, operation: operation, options: options},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	envelope := domain.Success(command, HarnessPlanData{
		Plan: plan, StatePath: statePath, Adapter: current.adapterPlan,
	})
	envelope.Scope["repository_id"] = current.repositoryID
	envelope.Scope["harness"] = options.HarnessID
	return envelope
}

func (services *Services) ApplyHarnessOperation(
	ctx context.Context,
	path string,
	operation string,
	planID string,
	options HarnessOperationOptions,
) domain.Envelope {
	command := "gds harness " + operation + " apply"
	if strings.TrimSpace(planID) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_PLAN_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--apply requires an exact plan id.",
		})
	}
	if finding := validateHarnessOperationInput(operation, options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, findings := services.harnessOperationContext(ctx, path, operation, options)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	handler, err := harnessActionHandler(current)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		harnessObserver{services: services, path: path, operation: operation, options: options},
		map[string]operations.ActionHandler{current.action: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, planID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope(command, err)
		envelope.Data = result
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		envelope.OperationID = result.OperationID
		return envelope
	}
	envelope := domain.Success(command, result)
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.OperationID = result.OperationID
	envelope.Scope["repository_id"] = current.repositoryID
	envelope.Scope["harness"] = options.HarnessID
	return envelope
}

func (services *Services) VerifyHarnessOperation(
	ctx context.Context,
	path string,
	operation string,
	operationID string,
	options HarnessOperationOptions,
) domain.Envelope {
	command := "gds harness " + operation + " verify"
	if strings.TrimSpace(operationID) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_OPERATION_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--verify requires an exact operation id.",
		})
	}
	if finding := validateHarnessOperationInput(operation, options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	record, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return envelopeForError(command, operationID, err)
	}
	planRecord, err := store.GetPlan(ctx, record.PlanID)
	if err != nil {
		return envelopeForError(command, record.PlanID, err)
	}
	plan, err := operations.DecodePlan(planRecord.Body)
	if err != nil {
		return envelopeForError(command, record.PlanID, fmt.Errorf("decode harness plan: %w", err))
	}
	if plan.Operation != "harness-"+operation || len(plan.Steps) != 1 {
		return domain.NewEnvelope(command, domain.ExitConflict, nil, domain.Finding{
			Code: "GDS_HARNESS_VERIFY_OPERATION_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Stored operation does not match the requested harness lifecycle operation.",
		})
	}
	step := plan.Steps[0]
	parameters, err := harness.DecodeAdapterParameters(step)
	absoluteTarget, pathErr := filepath.Abs(options.TargetRoot)
	absoluteSource := ""
	var sourceErr error
	if operation == "rollback" {
		absoluteSource, sourceErr = filepath.Abs(options.RollbackSourceRoot)
	}
	if err != nil || pathErr != nil || parameters.Harness != options.HarnessID ||
		parameters.TargetRoot != absoluteTarget || sourceErr != nil || parameters.SourceRoot != absoluteSource {
		return domain.NewEnvelope(command, domain.ExitConflict, nil, domain.Finding{
			Code: "GDS_HARNESS_VERIFY_SCOPE_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Stored operation does not match the requested harness and target root.",
		})
	}
	candidate := harness.CandidateFromParameters(parameters)
	previous := harness.PreviousCandidateFromParameters(parameters)
	var handler operations.ActionHandler
	if step.Action == harness.RemoveAdapterAction {
		handler, err = harness.NewAdapterRemover(parameters.TargetRoot, candidate)
	} else {
		adapter, envelope := services.resolveHarnessAdapter(ctx, path, options.HarnessID, command)
		if envelope != nil {
			return *envelope
		}
		var rendered harness.AdapterCandidate
		var renderFindings []domain.Finding
		if operation == "rollback" {
			rendered, renderFindings = adapter.LoadInstalled(
				parameters.SourceRoot,
				harness.RenderRequest{SkillProfile: options.SkillProfile, Scope: options.Scope},
			)
		} else {
			rendered, renderFindings = adapter.Render(harness.RenderRequest{
				SkillProfile: options.SkillProfile, Scope: options.Scope,
			})
		}
		if len(renderFindings) != 0 || rendered.CandidateDigest != parameters.CandidateDigest ||
			!slices.Equal(rendered.Files, parameters.Files) {
			return domain.NewEnvelope(command, domain.ExitStale, nil, domain.Finding{
				Code: "GDS_HARNESS_VERIFY_CANDIDATE_STALE", Severity: domain.SeverityHigh,
				Message: "Current canonical adapter candidate differs from the stored operation.",
			})
		}
		if operation == "install" {
			handler, err = harness.NewAdapterMaterializer(parameters.TargetRoot, rendered)
		} else {
			handler, err = harness.NewAdapterUpdater(
				parameters.TargetRoot, parameters.SourceRoot, operation, rendered, previous,
			)
		}
	}
	if err != nil {
		return domain.InternalError(command, err)
	}
	checker := harnessObserver{services: services, path: path, operation: operation, options: options}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, checker,
		map[string]operations.ActionHandler{step.Action: handler},
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
	envelope.Scope["harness"] = options.HarnessID
	return envelope
}

func (services *Services) harnessOperationContext(
	ctx context.Context,
	path string,
	operation string,
	options HarnessOperationOptions,
) (harnessOperationContext, []domain.Finding) {
	root, anchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return harnessOperationContext{}, findings
	}
	absoluteTarget, err := filepath.Abs(options.TargetRoot)
	if err != nil {
		return harnessOperationContext{}, []domain.Finding{dependencyFinding(options.TargetRoot, err)}
	}
	engineRoot := projections.ResolveDevelopmentSourceLayout(root).EngineRoot
	adapter, adapterFindings := harness.NewAdapter(engineRoot, options.HarnessID, services.Schemas)
	if len(adapterFindings) != 0 {
		return harnessOperationContext{}, adapterFindings
	}
	request := harness.RenderRequest{SkillProfile: options.SkillProfile, Scope: options.Scope}
	var adapterPlan harness.AdapterPlan
	switch operation {
	case "install":
		adapterPlan, findings = adapter.PlanInstall(absoluteTarget, request)
	case "update":
		adapterPlan, findings = adapter.PlanUpdate(absoluteTarget, request)
	case "rollback":
		absoluteSource, sourceErr := filepath.Abs(options.RollbackSourceRoot)
		if sourceErr != nil {
			return harnessOperationContext{}, []domain.Finding{dependencyFinding(
				options.RollbackSourceRoot, sourceErr,
			)}
		}
		prior, priorFindings := adapter.LoadInstalled(absoluteSource, request)
		if len(priorFindings) != 0 {
			return harnessOperationContext{}, priorFindings
		}
		adapterPlan, findings = adapter.PlanRollback(absoluteTarget, absoluteSource, prior)
	case "remove":
		adapterPlan, findings = adapter.PlanRemove(absoluteTarget, request)
	default:
		return harnessOperationContext{}, []domain.Finding{{
			Code: "GDS_HARNESS_OPERATION_INVALID", Severity: domain.SeverityHigh,
			Message: "Harness operation must be install, update, rollback, or remove.",
		}}
	}
	if len(findings) != 0 {
		return harnessOperationContext{}, findings
	}
	if _, err := services.Git.CommittedSourceOID(ctx, engineRoot, []string{
		"harnesses", "skills", "core/harness", "core/skills",
		"schemas/v1/harness-profile.schema.json", "schemas/v1/skill-registry.schema.json",
		"schemas/v1/plan.schema.json",
	}); err != nil {
		return harnessOperationContext{}, []domain.Finding{dependencyFinding(engineRoot, err)}
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return harnessOperationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	headOID, err := services.Git.HeadOID(ctx, info.WorktreeRoot)
	if err != nil {
		return harnessOperationContext{}, []domain.Finding{dependencyFinding(root, err)}
	}
	manifestDigest, err := fileDigest(filepath.Join(info.WorktreeRoot, ".gds", "repository.yaml"))
	if err != nil {
		return harnessOperationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	action := harness.MaterializeAdapterAction
	switch operation {
	case "update":
		action = harness.UpdateAdapterAction
	case "rollback":
		action = harness.RollbackAdapterAction
	case "remove":
		action = harness.RemoveAdapterAction
	}
	return harnessOperationContext{
		root: engineRoot, repositoryID: anchor.Repository.ID, targetRoot: absoluteTarget,
		candidate: adapterPlan.Candidate(), previous: adapterPlan.PreviousCandidate(),
		adapterPlan: adapterPlan, action: action,
		observation: operations.Observation{
			RepositoryID: anchor.Repository.ID, HeadOID: headOID,
			WorktreeFingerprint: adapterPlan.BeforeFingerprint,
			ManifestDigest:      manifestDigest, PolicyDigest: adapterPlan.CandidateDigest,
		},
	}, nil
}

func harnessActionHandler(current harnessOperationContext) (operations.ActionHandler, error) {
	if current.action == harness.RemoveAdapterAction {
		return harness.NewAdapterRemover(current.targetRoot, current.candidate)
	}
	if current.action == harness.UpdateAdapterAction || current.action == harness.RollbackAdapterAction {
		return harness.NewAdapterUpdater(
			current.targetRoot, current.adapterPlan.SourceRoot, current.adapterPlan.Operation,
			current.candidate, current.previous,
		)
	}
	return harness.NewAdapterMaterializer(current.targetRoot, current.candidate)
}

func validateHarnessOperationInput(
	operation string,
	options HarnessOperationOptions,
) *domain.Finding {
	if operation != "install" && operation != "update" && operation != "rollback" && operation != "remove" {
		return &domain.Finding{
			Code: "GDS_HARNESS_OPERATION_INVALID", Severity: domain.SeverityHigh,
			Message: "Harness operation must be install, update, rollback, or remove.",
		}
	}
	if strings.TrimSpace(options.HarnessID) == "" || options.HarnessID == "all" ||
		strings.TrimSpace(options.TargetRoot) == "" || strings.TrimSpace(options.SkillProfile) == "" ||
		strings.TrimSpace(options.Scope) == "" {
		return &domain.Finding{
			Code: "GDS_HARNESS_OPERATION_SCOPE_REQUIRED", Severity: domain.SeverityHigh,
			Message: "One harness, target root, skill profile, and projection scope are required.",
		}
	}
	if operation == "rollback" && strings.TrimSpace(options.RollbackSourceRoot) == "" {
		return &domain.Finding{
			Code: "GDS_HARNESS_ROLLBACK_SOURCE_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Rollback requires --rollback-source with an exact prior installed projection.",
		}
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return finding
	}
	return nil
}
