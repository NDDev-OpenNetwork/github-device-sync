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
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estateregistry"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitcontracts"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type RepositoryOnboardOptions struct {
	ProjectionOperationOptions
	AnchorPath string
}

type RepositoryOnboardPlanData struct {
	Plan      operations.Plan  `json:"plan"`
	StatePath string           `json:"state_path"`
	Target    string           `json:"target"`
	Candidate anchor.Candidate `json:"candidate"`
}

type repositoryOnboardContext struct {
	root        string
	candidate   anchor.Candidate
	observation operations.Observation
	file        anchor.FileObservation
}

type repositoryOnboardObserver struct {
	services  *Services
	root      string
	candidate anchor.Candidate
}

func (observer repositoryOnboardObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.repositoryOnboardContext(
		ctx, observer.root, observer.candidate,
	)
	if len(findings) != 0 || current.candidate.Anchor.Repository.ID != repositoryID {
		return operations.Observation{}, errors.New("repository onboarding precondition is no longer proven")
	}
	return current.observation, nil
}

func (services *Services) PlanRepositoryOnboard(
	ctx context.Context,
	path string,
	options RepositoryOnboardOptions,
) domain.Envelope {
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope("gds repository onboard plan", domain.ExitInput, nil, *finding)
	}
	candidate, findings := services.loadAnchorCandidate(options.AnchorPath)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds repository onboard plan", classifyFindings(findings), nil, findings...,
		)
	}
	current, findings := services.repositoryOnboardContext(ctx, path, candidate)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds repository onboard plan", classifyFindings(findings), nil, findings...,
		)
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds repository onboard plan", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError("gds repository onboard plan", err)
	}
	plan, err := operations.NewPlan(planID, now, now.Add(projectionPlanLifetime), operations.PlanInput{
		Operation: "onboard-repository",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			WorktreeFingerprint: current.observation.WorktreeFingerprint,
			ManifestDigest:      current.observation.ManifestDigest,
			PolicyDigest:        current.observation.PolicyDigest,
		}},
		Steps: []operations.Step{{
			StepID: "materialize-repository-anchor", RepositoryID: candidate.Anchor.Repository.ID,
			Action: anchor.MaterializeAction, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "manual"},
			Parameters:   anchor.Parameters(current.root, current.file, candidate),
		}},
		ApprovalClass: "onboard-local-repository",
	})
	if err != nil {
		return domain.InternalError("gds repository onboard plan", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		repositoryOnboardObserver{services: services, root: current.root, candidate: candidate},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope("gds repository onboard plan", err)
	}
	envelope := domain.Success("gds repository onboard plan", RepositoryOnboardPlanData{
		Plan: plan, StatePath: statePath, Target: current.root, Candidate: candidate,
	})
	envelope.Scope["repository_id"] = candidate.Anchor.Repository.ID
	return envelope
}

func (services *Services) ApplyRepositoryOnboard(
	ctx context.Context,
	planID string,
	options RepositoryOnboardOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return syncIdentifierRequired("gds repository onboard apply", "plan", "--apply")
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope("gds repository onboard apply", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds repository onboard apply", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	plan, root, candidate, err := loadRepositoryOnboardPlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return repositoryOnboardPlanInvalid("gds repository onboard apply")
	}
	current, findings := services.repositoryOnboardContext(ctx, root, candidate)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds repository onboard apply", classifyFindings(findings), nil, findings...,
		)
	}
	handler, err := anchor.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError("gds repository onboard apply", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		repositoryOnboardObserver{services: services, root: current.root, candidate: candidate},
		map[string]operations.ActionHandler{anchor.MaterializeAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, plan.PlanID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope("gds repository onboard apply", err)
		envelope.Data = result
		envelope.OperationID = result.OperationID
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		return envelope
	}
	envelope := domain.Success("gds repository onboard apply", result)
	envelope.OperationID = result.OperationID
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.Scope["repository_id"] = candidate.Anchor.Repository.ID
	return envelope
}

func (services *Services) VerifyRepositoryOnboard(
	ctx context.Context,
	operationID string,
	options RepositoryOnboardOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return syncIdentifierRequired("gds repository onboard verify", "operation", "--verify")
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope("gds repository onboard verify", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds repository onboard verify", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	operation, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return repositoryOnboardPlanInvalid("gds repository onboard verify")
	}
	plan, _, candidate, err := loadRepositoryOnboardPlan(
		ctx, store, operation.PlanID, services.Schemas,
	)
	if err != nil {
		return repositoryOnboardPlanInvalid("gds repository onboard verify")
	}
	handler, err := anchor.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError("gds repository onboard verify", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, repositoryOnboardObserver{},
		map[string]operations.ActionHandler{anchor.MaterializeAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope("gds repository onboard verify", err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success("gds repository onboard verify", result)
	envelope.OperationID = operationID
	envelope.Scope["repository_id"] = candidate.Anchor.Repository.ID
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) repositoryOnboardContext(
	ctx context.Context,
	path string,
	candidate anchor.Candidate,
) (repositoryOnboardContext, []domain.Finding) {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return repositoryOnboardContext{}, []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ONBOARD_BOUNDARY_NOT_PROVEN", err.Error(), path,
		)}
	}
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil || !services.repositoryOnboardGitStateEligible(ctx, info, status, candidate) {
		return repositoryOnboardContext{}, []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ONBOARD_GIT_STATE_UNSAFE",
			"Onboarding requires either a clean current default branch or a clean embedded detached checkout at its exact superproject gitlink.",
			info.WorktreeRoot,
		)}
	}
	observedFile, err := anchor.Observe(info.WorktreeRoot)
	if err != nil || observedFile.File.State != "missing" {
		return repositoryOnboardContext{}, []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ALREADY_MANAGED",
			"Repository onboarding requires the canonical anchor path to be absent.",
			info.WorktreeRoot,
		)}
	}
	topology, err := services.Git.InspectTopology(ctx, info.WorktreeRoot)
	if err != nil {
		return repositoryOnboardContext{}, []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ONBOARD_REMOTE_NOT_PROVEN", err.Error(), info.WorktreeRoot,
		)}
	}
	remoteFindings := gitcontracts.ValidateRemote(topology, "origin", gitcontracts.ExpectedRepository{
		Owner: candidate.Anchor.Provider.Owner, Name: candidate.Anchor.Provider.Name,
	})
	if len(remoteFindings) != 0 {
		return repositoryOnboardContext{}, remoteFindings
	}
	estateRoot, estateFindings := services.lifecycleEstateRoot(ctx)
	if len(estateFindings) != 0 {
		return repositoryOnboardContext{}, estateFindings
	}
	compiled := services.Compiler.CompileDirectory(
		estateRoot, candidate.Anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return repositoryOnboardContext{}, compiled.Findings
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Head      gitprovider.HeadState   `json:"head"`
		Branch    gitprovider.BranchState `json:"branch"`
		Changes   gitprovider.ChangeState `json:"changes"`
		Anchor    anchor.FileObservation  `json:"anchor"`
		Candidate string                  `json:"candidate"`
	}{status.Head, status.Branch, status.Changes, observedFile.File, candidate.Digest})
	if err != nil {
		return repositoryOnboardContext{}, []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ONBOARD_FINGERPRINT_FAILED", err.Error(), info.WorktreeRoot,
		)}
	}
	return repositoryOnboardContext{
		root: info.WorktreeRoot, candidate: candidate, file: observedFile.File,
		observation: operations.Observation{
			RepositoryID: candidate.Anchor.Repository.ID, HeadOID: status.Head.OID,
			WorktreeFingerprint: fingerprint, ManifestDigest: observedFile.File.Digest,
			PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func (services *Services) repositoryOnboardGitStateEligible(
	ctx context.Context,
	info gitprovider.RepositoryInfo,
	status gitprovider.Status,
	candidate anchor.Candidate,
) bool {
	if status.Head.OID == "" || !checkoutStatusIsClean(status) {
		return false
	}
	if status.Head.Mode == "branch" {
		if status.Branch.Name == candidate.Anchor.Git.DefaultBranch {
			return status.Branch.Upstream == "origin/"+candidate.Anchor.Git.DefaultBranch &&
				status.Branch.Ahead == 0 && status.Branch.Behind == 0 && !status.Branch.Diverged
		}
		if status.Branch.Upstream == "" {
			return status.Branch.UpstreamState == "missing"
		}
		return status.Branch.Upstream == "origin/"+status.Branch.Name &&
			status.Branch.Ahead == 0 && status.Branch.Behind == 0 && !status.Branch.Diverged
	}
	if status.Head.Mode != "detached" || info.SuperprojectRoot == "" {
		return false
	}
	parentStatus, err := services.Git.InspectStatus(ctx, info.SuperprojectRoot)
	if err != nil || !checkoutStatusIsClean(parentStatus) {
		return false
	}
	topology, err := services.Git.InspectTopology(ctx, info.SuperprojectRoot)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(info.SuperprojectRoot, info.WorktreeRoot)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	relative = filepath.ToSlash(relative)
	for _, submodule := range topology.Submodules {
		if submodule.Path == relative && submodule.GitlinkStage == 0 &&
			submodule.GitlinkOID == status.Head.OID && submodule.CurrentOID == status.Head.OID &&
			submodule.WorktreeState == "at-gitlink" {
			return true
		}
	}
	return false
}

func (services *Services) lifecycleEstateRoot(ctx context.Context) (string, []domain.Finding) {
	_ = ctx
	requested := strings.TrimSpace(os.Getenv("GDS_ESTATE_ROOT"))
	expectedRepositoryID := ""
	if requested == "" {
		registrationPath, err := estateregistry.DefaultPath(os.Getenv, os.UserHomeDir)
		if err != nil {
			return "", []domain.Finding{repositoryOnboardFinding(
				"GDS_REPOSITORY_ONBOARD_ESTATE_NOT_PROVEN", err.Error(), "",
			)}
		}
		registration, findings := estateregistry.Load(registrationPath, services.Schemas)
		if len(findings) != 0 {
			return "", findings
		}
		requested = registration.Document.Estate.Root
		expectedRepositoryID = registration.Document.Estate.RepositoryID
		anchorEvidence, err := anchor.Observe(requested)
		if err != nil || anchorEvidence.File.State != "regular" ||
			anchorEvidence.File.ContentDigest != registration.Document.Estate.AnchorDigest {
			return "", []domain.Finding{repositoryOnboardFinding(
				"GDS_REPOSITORY_ONBOARD_ESTATE_NOT_PROVEN",
				"Registered estate anchor no longer matches its trusted device locator.", requested,
			)}
		}
	}
	root, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return "", []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ONBOARD_ESTATE_NOT_PROVEN", err.Error(), requested,
		)}
	}
	anchorValue, findings := manifest.NewLoader(services.Schemas).LoadRepository(root)
	if len(findings) != 0 || !hasRole(anchorValue.Repository.Roles, "control-plane") {
		if len(findings) != 0 {
			return "", findings
		}
		return "", []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ONBOARD_ESTATE_NOT_PROVEN",
			"Configured estate root is not the control-plane repository.", root,
		)}
	}
	if expectedRepositoryID != "" && expectedRepositoryID != anchorValue.Repository.ID {
		return "", []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ONBOARD_ESTATE_NOT_PROVEN",
			"Registered estate repository identity differs from the control-plane anchor.", root,
		)}
	}
	return filepath.Clean(root), nil
}

func (services *Services) loadAnchorCandidate(path string) (anchor.Candidate, []domain.Finding) {
	if strings.TrimSpace(path) == "" {
		return anchor.Candidate{}, []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ANCHOR_REQUIRED", "--anchor must identify an exact candidate file.", path,
		)}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return anchor.Candidate{}, []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ANCHOR_NOT_PROVEN", err.Error(), path,
		)}
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return anchor.Candidate{}, []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ANCHOR_NOT_PROVEN",
			"Anchor candidate must be a bounded regular non-symlink file.", absolute,
		)}
	}
	raw, err := os.ReadFile(absolute)
	if err != nil {
		return anchor.Candidate{}, []domain.Finding{repositoryOnboardFinding(
			"GDS_REPOSITORY_ANCHOR_NOT_PROVEN", err.Error(), absolute,
		)}
	}
	return anchor.DecodeCandidate(absolute, raw, services.Schemas)
}

func loadRepositoryOnboardPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, string, anchor.Candidate, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, "", anchor.Candidate{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != "onboard-repository" ||
		plan.PlanDigest != record.PlanDigest || len(plan.Steps) != 1 ||
		len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, "", anchor.Candidate{}, errors.New("stored plan is not a valid onboarding plan")
	}
	root, candidate, err := anchor.StepCandidate(plan.Steps[0], schemas)
	return plan, root, candidate, err
}

func repositoryOnboardPlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_REPOSITORY_ONBOARD_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable repository onboarding plan.",
	})
}

func repositoryOnboardFinding(code string, message string, path string) domain.Finding {
	evidence := map[string]any{}
	if path != "" {
		evidence["path"] = path
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence,
	}
}
