package app

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/discovery"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/workspace"
)

type WorkspaceMaterializeOptions struct {
	ProjectionOperationOptions
	AnchorPath string
	DevicePath string
	SourcePath string
}

type WorkspacePlacementOptions struct {
	AnchorPath string
	DevicePath string
}

type WorkspaceMaterializePlanData struct {
	Plan      operations.Plan              `json:"plan"`
	StatePath string                       `json:"state_path"`
	Placement workspace.Placement          `json:"placement"`
	Source    gitprovider.LocalCloneSource `json:"source"`
	Filter    string                       `json:"filter"`
}

func (services *Services) ResolveWorkspacePlacement(
	ctx context.Context,
	options WorkspacePlacementOptions,
) domain.Envelope {
	const command = "gds workspace plan"
	candidate, findings := services.loadAnchorCandidate(options.AnchorPath)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	device, findings := workspace.LoadDeviceCandidate(options.DevicePath, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	environment, err := workspace.CurrentEnvironment()
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitNotProven, nil, domain.Finding{
			Code: "GDS_WORKSPACE_ENVIRONMENT_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: err.Error(),
		})
	}
	if finding := services.embeddedOnlyFinding(
		ctx, device.Descriptor, environment, candidate.Anchor.Repository.ID,
	); finding != nil {
		return domain.NewEnvelope(command, classifyFindings([]domain.Finding{*finding}), nil, *finding)
	}
	placement, findings := workspace.ResolvePlacement(device.Descriptor, candidate.Anchor, environment)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	estateRoot, estateFindings := services.lifecycleEstateRoot(ctx)
	if len(estateFindings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(estateFindings), nil, estateFindings...)
	}
	compiled := services.Compiler.CompileDirectory(
		estateRoot, candidate.Anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(compiled.Findings), nil, compiled.Findings...)
	}
	envelope := domain.Success(command, map[string]any{
		"placement": placement, "device_descriptor": device,
		"repository_anchor_digest": candidate.Digest,
		"policy_digest":            compiled.Document.CompiledPolicy.Digest,
	})
	envelope.Scope["repository_id"] = candidate.Anchor.Repository.ID
	return envelope
}

type workspaceMaterializeContext struct {
	candidate   anchor.Candidate
	device      workspace.DeviceCandidate
	placement   workspace.Placement
	source      gitprovider.LocalCloneSource
	filter      string
	observation operations.Observation
}

type workspaceMaterializeObserver struct {
	services   *Services
	candidate  anchor.Candidate
	devicePath string
	sourcePath string
}

func (observer workspaceMaterializeObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.workspaceMaterializeContext(
		ctx, observer.candidate, observer.devicePath, observer.sourcePath,
	)
	if len(findings) != 0 || current.candidate.Anchor.Repository.ID != repositoryID {
		return operations.Observation{}, errors.New("workspace materialization precondition is no longer proven")
	}
	return current.observation, nil
}

func (services *Services) PlanWorkspaceMaterialize(
	ctx context.Context,
	options WorkspaceMaterializeOptions,
) domain.Envelope {
	const command = "gds repository materialize plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	candidate, findings := services.loadAnchorCandidate(options.AnchorPath)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	current, findings := services.workspaceMaterializeContext(
		ctx, candidate, options.DevicePath, options.SourcePath,
	)
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
		Operation: "materialize-workspace-checkout",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			WorktreeFingerprint: current.observation.WorktreeFingerprint,
			RemoteDefaultOID:    current.observation.RemoteDefaultOID,
			ManifestDigest:      current.observation.ManifestDigest, PolicyDigest: current.observation.PolicyDigest,
		}},
		Steps: []operations.Step{{
			StepID: "materialize-workspace-checkout", RepositoryID: current.observation.RepositoryID,
			Action: workspace.MaterializeCheckoutAction, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "explicit-plan", Action: "quarantine-checkout"},
			Parameters: workspace.MaterializeStepParameters(
				current.placement, current.source, current.filter, string(current.candidate.Raw),
				current.candidate.Digest, current.device,
			),
		}},
		ApprovalClass: "materialize-local-checkout",
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		workspaceMaterializeObserver{
			services: services, candidate: current.candidate,
			devicePath: current.device.Path, sourcePath: current.source.Path,
		}, nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	envelope := domain.Success(command, WorkspaceMaterializePlanData{
		Plan: plan, StatePath: statePath, Placement: current.placement,
		Source: current.source, Filter: current.filter,
	})
	envelope.Scope["repository_id"] = current.candidate.Anchor.Repository.ID
	return envelope
}

func (services *Services) ApplyWorkspaceMaterialize(
	ctx context.Context,
	planID string,
	options WorkspaceMaterializeOptions,
) domain.Envelope {
	const command = "gds repository materialize apply"
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
	plan, parameters, candidate, err := loadWorkspaceMaterializePlan(
		ctx, store, planID, services.Schemas,
	)
	if err != nil {
		return workspaceMaterializePlanInvalid(command)
	}
	device, findings := workspace.LoadDeviceCandidate(parameters.DevicePath, services.Schemas)
	if len(findings) != 0 || device.Digest != parameters.DeviceDigest {
		return domain.NewEnvelope(command, domain.ExitStale, nil, domain.Finding{
			Code: "GDS_WORKSPACE_DEVICE_DESCRIPTOR_STALE", Severity: domain.SeverityHigh,
			Message: "Device descriptor differs from the stored materialization plan.",
		})
	}
	handler, err := workspace.NewMaterializeHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		workspaceMaterializeObserver{
			services: services, candidate: candidate,
			devicePath: parameters.DevicePath, sourcePath: parameters.SourcePath,
		}, map[string]operations.ActionHandler{workspace.MaterializeCheckoutAction: handler},
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

func (services *Services) VerifyWorkspaceMaterialize(
	ctx context.Context,
	operationID string,
	options WorkspaceMaterializeOptions,
) domain.Envelope {
	const command = "gds repository materialize verify"
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
		return workspaceMaterializePlanInvalid(command)
	}
	plan, _, candidate, err := loadWorkspaceMaterializePlan(
		ctx, store, operation.PlanID, services.Schemas,
	)
	if err != nil {
		return workspaceMaterializePlanInvalid(command)
	}
	handler, err := workspace.NewMaterializeHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, workspaceMaterializeObserver{},
		map[string]operations.ActionHandler{workspace.MaterializeCheckoutAction: handler},
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

func (services *Services) workspaceMaterializeContext(
	ctx context.Context,
	candidate anchor.Candidate,
	devicePath string,
	sourcePath string,
) (workspaceMaterializeContext, []domain.Finding) {
	device, findings := workspace.LoadDeviceCandidate(devicePath, services.Schemas)
	if len(findings) != 0 {
		return workspaceMaterializeContext{}, findings
	}
	environment, err := workspace.CurrentEnvironment()
	if err != nil {
		return workspaceMaterializeContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_ENVIRONMENT_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	if finding := services.embeddedOnlyFinding(
		ctx, device.Descriptor, environment, candidate.Anchor.Repository.ID,
	); finding != nil {
		return workspaceMaterializeContext{}, []domain.Finding{*finding}
	}
	placement, findings := workspace.ResolvePlacement(device.Descriptor, candidate.Anchor, environment)
	if len(findings) != 0 {
		return workspaceMaterializeContext{}, findings
	}
	if placement.Mode == "absent" {
		return workspaceMaterializeContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_MATERIALIZATION_NOT_SELECTED", Severity: domain.SeverityHigh,
			Message: "Device policy selects absent materialization for this repository.",
		}}
	}
	rootInfo, err := os.Lstat(placement.WorkspaceRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return workspaceMaterializeContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_ROOT_NOT_READY", Severity: domain.SeverityHigh,
			Message:  "Selected workspace root must already be a real directory.",
			Evidence: map[string]any{"path": placement.WorkspaceRoot},
		}}
	}
	if _, err := os.Lstat(placement.TargetPath); !errors.Is(err, os.ErrNotExist) {
		return workspaceMaterializeContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_TARGET_EXISTS", Severity: domain.SeverityHigh,
			Message:  "Selected checkout target already exists.",
			Evidence: map[string]any{"path": placement.TargetPath},
		}}
	}
	source, err := services.GitMutations.ObserveLocalCloneSource(
		ctx, sourcePath, "refs/heads/"+candidate.Anchor.Git.DefaultBranch,
	)
	if err != nil {
		return workspaceMaterializeContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_LIVE_PROVIDER_DISABLED", Severity: domain.SeverityHigh,
			Message:  "Checkout materialization currently requires the verified isolated local provider.",
			Evidence: map[string]any{"error": err.Error()},
		}}
	}
	anchorDigest, err := services.GitMutations.ObserveLocalCloneFileDigest(ctx, source, anchor.Path)
	if err != nil || anchorDigest != candidate.Digest {
		return workspaceMaterializeContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_SOURCE_ANCHOR_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Source default branch does not contain the exact candidate repository anchor.",
		}}
	}
	estateRoot, estateFindings := services.lifecycleEstateRoot(ctx)
	if len(estateFindings) != 0 {
		return workspaceMaterializeContext{}, estateFindings
	}
	compiled := services.Compiler.CompileDirectory(
		estateRoot, candidate.Anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return workspaceMaterializeContext{}, compiled.Findings
	}
	filter := "full"
	if placement.Mode == "reference" || placement.Mode == "ephemeral" {
		filter = "blob-none"
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Placement workspace.Placement          `json:"placement"`
		Source    gitprovider.LocalCloneSource `json:"source"`
		Candidate string                       `json:"candidate"`
		Device    string                       `json:"device"`
		Filter    string                       `json:"filter"`
	}{placement, source, candidate.Digest, device.Digest, filter})
	if err != nil {
		return workspaceMaterializeContext{}, []domain.Finding{{
			Code: "GDS_WORKSPACE_FINGERPRINT_FAILED", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	return workspaceMaterializeContext{
		candidate: candidate, device: device, placement: placement, source: source, filter: filter,
		observation: operations.Observation{
			RepositoryID: candidate.Anchor.Repository.ID, HeadOID: source.HeadOID,
			WorktreeFingerprint: fingerprint, RemoteDefaultOID: source.HeadOID,
			ManifestDigest: candidate.Digest, PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func loadWorkspaceMaterializePlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, workspace.MaterializeParameters, anchor.Candidate, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, workspace.MaterializeParameters{}, anchor.Candidate{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != "materialize-workspace-checkout" ||
		plan.PlanDigest != record.PlanDigest || len(plan.Steps) != 1 || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, workspace.MaterializeParameters{}, anchor.Candidate{},
			errors.New("stored plan is not a valid workspace materialization")
	}
	parameters, err := workspace.StepMaterializeParameters(plan.Steps[0])
	if err != nil {
		return operations.Plan{}, workspace.MaterializeParameters{}, anchor.Candidate{}, err
	}
	candidate, findings := anchor.DecodeCandidate(anchor.Path, []byte(parameters.AnchorContent), schemas)
	if len(findings) != 0 || candidate.Digest != parameters.AnchorDigest ||
		candidate.Anchor.Repository.ID != parameters.RepositoryID {
		return operations.Plan{}, workspace.MaterializeParameters{}, anchor.Candidate{},
			errors.New("workspace materialization candidate is invalid")
	}
	return plan, parameters, candidate, nil
}

func workspaceMaterializePlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_WORKSPACE_MATERIALIZATION_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable workspace materialization.",
	})
}

// embeddedOnlyFinding refuses standalone placement for a repository that is the
// dependency of a resolved `git-submodule-consumer` relationship (ADR 0027).
//
// The incoming edge is observed by discovering the Git boundaries under the
// device's own declared workspace roots: a superproject that would embed this
// repository is by construction placed in one of them. Discovery failure is not
// silently treated as "no consumer" — it surfaces as a not-proven finding.
func (services *Services) embeddedOnlyFinding(
	ctx context.Context,
	descriptor workspace.DeviceDescriptor,
	environment workspace.Environment,
	repositoryID string,
) *domain.Finding {
	roots := make([]string, 0, len(descriptor.WorkspaceRoots))
	for _, portable := range descriptor.WorkspaceRoots {
		expanded, err := workspace.ExpandPortablePath(portable, environment)
		if err != nil {
			continue
		}
		if info, statErr := os.Lstat(expanded); statErr != nil || !info.IsDir() {
			continue
		}
		roots = append(roots, expanded)
	}
	sort.Strings(roots)
	roots = uniqueStrings(roots)

	loader := manifest.NewLoader(services.Schemas)
	anchors := make([]domain.RepositoryAnchor, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		result, err := services.Discovery.Discover(ctx, root, discovery.Options{})
		if err != nil {
			return &domain.Finding{
				Code: "GDS_WORKSPACE_EMBEDDED_ONLY_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  "Incoming submodule-consumer edges could not be observed for this repository.",
				Evidence: map[string]any{"root": root, "error": err.Error()},
			}
		}
		for _, boundary := range result.Boundaries {
			if boundary.AnchorState != "valid" || seen[boundary.Path] {
				continue
			}
			seen[boundary.Path] = true
			anchorValue, anchorFindings := loader.LoadRepository(boundary.Path)
			if len(anchorFindings) != 0 {
				continue
			}
			anchors = append(anchors, anchorValue)
		}
	}
	return workspace.EmbeddedOnlyFinding(
		repositoryID, workspace.EmbeddedConsumers(anchors, repositoryID),
	)
}
