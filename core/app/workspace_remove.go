package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	"github.com/NDDev-OpenNetwork/github-device-sync/core/workspace"
)

type WorkspaceRemoveOptions struct {
	ProjectionOperationOptions
	DevicePath string
}

type WorkspaceRemovePlanData struct {
	Plan           operations.Plan                        `json:"plan"`
	StatePath      string                                 `json:"state_path"`
	Placement      workspace.Placement                    `json:"placement"`
	QuarantinePath string                                 `json:"quarantine_path"`
	Evidence       gitprovider.CheckoutQuarantineEvidence `json:"evidence"`
}

type workspaceRemoveContext struct {
	root           string
	anchor         domain.RepositoryAnchor
	device         workspace.DeviceCandidate
	placement      workspace.Placement
	quarantinePath string
	evidence       gitprovider.CheckoutQuarantineEvidence
	observation    operations.Observation
}

type workspaceRemoveObserver struct {
	services   *Services
	root       string
	devicePath string
}

func (observer workspaceRemoveObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.workspaceRemoveContext(ctx, observer.root, observer.devicePath)
	if len(findings) != 0 || current.anchor.Repository.ID != repositoryID {
		return operations.Observation{}, errors.New("checkout removal precondition is no longer proven")
	}
	return current.observation, nil
}

func (services *Services) PlanWorkspaceRemove(
	ctx context.Context,
	path string,
	options WorkspaceRemoveOptions,
) domain.Envelope {
	const command = "gds repository remove-checkout plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, findings := services.workspaceRemoveContext(ctx, path, options.DevicePath)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	if current.device.Descriptor.Device.ID != options.DeviceID {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_WORKSPACE_DEVICE_ID_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Operation device identity differs from the selected device descriptor.",
		})
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
		Operation: "remove-workspace-checkout",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			WorktreeFingerprint: current.observation.WorktreeFingerprint,
			RemoteDefaultOID:    current.observation.RemoteDefaultOID,
			ManifestDigest:      current.observation.ManifestDigest, PolicyDigest: current.observation.PolicyDigest,
		}},
		Steps: []operations.Step{{
			StepID: "quarantine-checkout", RepositoryID: current.observation.RepositoryID,
			Action: workspace.QuarantineCheckoutAction, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "explicit-plan", Action: "restore-quarantined-checkout"},
			Parameters: workspace.QuarantineStepParameters(
				current.placement, current.quarantinePath, current.evidence.HeadOID,
				current.evidence.BranchRef, current.evidence.AnchorDigest, current.device,
			),
		}},
		ApprovalClass: "remove-local-checkout",
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		workspaceRemoveObserver{services: services, root: current.root, devicePath: current.device.Path},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	envelope := domain.Success(command, WorkspaceRemovePlanData{
		Plan: plan, StatePath: statePath, Placement: current.placement,
		QuarantinePath: current.quarantinePath, Evidence: current.evidence,
	})
	envelope.Scope["repository_id"] = current.anchor.Repository.ID
	return envelope
}

func (services *Services) ApplyWorkspaceRemove(
	ctx context.Context,
	planID string,
	options WorkspaceRemoveOptions,
) domain.Envelope {
	const command = "gds repository remove-checkout apply"
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
	plan, parameters, err := loadWorkspaceRemovePlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return workspaceRemovePlanInvalid(command)
	}
	device, findings := workspace.LoadDeviceCandidate(parameters.DevicePath, services.Schemas)
	if len(findings) != 0 || device.Digest != parameters.DeviceDigest {
		return domain.NewEnvelope(command, domain.ExitStale, nil, domain.Finding{
			Code: "GDS_WORKSPACE_DEVICE_DESCRIPTOR_STALE", Severity: domain.SeverityHigh,
			Message: "Device descriptor differs from the stored checkout removal plan.",
		})
	}
	handler, err := workspace.NewQuarantineHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		workspaceRemoveObserver{services: services, root: parameters.CheckoutPath, devicePath: parameters.DevicePath},
		map[string]operations.ActionHandler{workspace.QuarantineCheckoutAction: handler},
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
	envelope.Scope["repository_id"] = parameters.RepositoryID
	return envelope
}

func (services *Services) VerifyWorkspaceRemove(
	ctx context.Context,
	operationID string,
	options WorkspaceRemoveOptions,
) domain.Envelope {
	const command = "gds repository remove-checkout verify"
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
		return workspaceRemovePlanInvalid(command)
	}
	plan, parameters, err := loadWorkspaceRemovePlan(ctx, store, operation.PlanID, services.Schemas)
	if err != nil {
		return workspaceRemovePlanInvalid(command)
	}
	handler, err := workspace.NewQuarantineHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, workspaceRemoveObserver{},
		map[string]operations.ActionHandler{workspace.QuarantineCheckoutAction: handler},
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
	envelope.Scope["repository_id"] = parameters.RepositoryID
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) workspaceRemoveContext(
	ctx context.Context,
	path string,
	devicePath string,
) (workspaceRemoveContext, []domain.Finding) {
	estateRoot, repositoryAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return workspaceRemoveContext{}, findings
	}
	device, findings := workspace.LoadDeviceCandidate(devicePath, services.Schemas)
	if len(findings) != 0 {
		return workspaceRemoveContext{}, findings
	}
	environment, err := workspace.CurrentEnvironment()
	if err != nil {
		return workspaceRemoveContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_ENVIRONMENT_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	placement, findings := workspace.ResolvePlacement(device.Descriptor, repositoryAnchor, environment)
	if len(findings) != 0 {
		return workspaceRemoveContext{}, findings
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return workspaceRemoveContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_REMOVE_BOUNDARY_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	physicalTarget, err := filepath.EvalSymlinks(placement.TargetPath)
	if err != nil || filepath.Clean(physicalTarget) != info.WorktreeRoot {
		return workspaceRemoveContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_REMOVE_PLACEMENT_MISMATCH", Severity: domain.SeverityHigh,
			Message:  "Current checkout is not at its exact device-selected placement.",
			Evidence: map[string]any{"current": info.WorktreeRoot, "expected": placement.TargetPath},
		}}
	}
	stateInfo, err := os.Lstat(placement.StateRoot)
	if err != nil || !stateInfo.IsDir() || stateInfo.Mode()&os.ModeSymlink != 0 {
		return workspaceRemoveContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_STATE_ROOT_NOT_READY", Severity: domain.SeverityHigh,
			Message:  "Device state root must already be a real directory.",
			Evidence: map[string]any{"path": placement.StateRoot},
		}}
	}
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil || status.Head.OID == "" {
		return workspaceRemoveContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_REMOVE_GIT_STATE_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "Checkout Git state could not be proven.",
		}}
	}
	observedAnchor, err := anchor.Observe(info.WorktreeRoot)
	if err != nil || observedAnchor.File.State != "regular" {
		return workspaceRemoveContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_REMOVE_ANCHOR_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "Checkout repository anchor could not be proven.",
		}}
	}
	quarantinePath := filepath.Join(
		placement.StateRoot, "quarantine", "checkouts", repositoryAnchor.Repository.ID, status.Head.OID,
	)
	evidence, err := services.GitMutations.ObserveCheckoutQuarantine(
		ctx, placement.WorkspaceRoot, placement.TargetPath, placement.StateRoot,
		quarantinePath, status.Head.OID, "refs/heads/"+repositoryAnchor.Git.DefaultBranch,
		observedAnchor.File.ContentDigest,
	)
	if err != nil {
		return workspaceRemoveContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_REMOVE_UNSAFE", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	compiled := services.Compiler.CompileDirectory(
		estateRoot, repositoryAnchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return workspaceRemoveContext{}, compiled.Findings
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Evidence gitprovider.CheckoutQuarantineEvidence `json:"evidence"`
		Device   string                                 `json:"device"`
	}{evidence, device.Digest})
	if err != nil {
		return workspaceRemoveContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_REMOVE_FINGERPRINT_FAILED", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	return workspaceRemoveContext{
		root: info.WorktreeRoot, anchor: repositoryAnchor, device: device,
		placement: placement, quarantinePath: quarantinePath, evidence: evidence,
		observation: operations.Observation{
			RepositoryID: repositoryAnchor.Repository.ID, HeadOID: evidence.HeadOID,
			WorktreeFingerprint: fingerprint, RemoteDefaultOID: evidence.RemoteOID,
			ManifestDigest: observedAnchor.File.ContentDigest,
			PolicyDigest:   compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func loadWorkspaceRemovePlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, workspace.QuarantineParameters, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, workspace.QuarantineParameters{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != "remove-workspace-checkout" ||
		plan.PlanDigest != record.PlanDigest || len(plan.Steps) != 1 || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, workspace.QuarantineParameters{},
			errors.New("stored plan is not a valid checkout removal")
	}
	parameters, err := workspace.StepQuarantineParameters(plan.Steps[0])
	return plan, parameters, err
}

func workspaceRemovePlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_WORKSPACE_REMOVE_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable checkout removal.",
	})
}
