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
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const estateRegistrationOperation = "register-device-estate"

type EstateRegistrationOptions struct {
	ProjectionOperationOptions
	EstateRoot       string
	RegistrationPath string
}

type EstateRegistrationPlanData struct {
	Plan             operations.Plan          `json:"plan"`
	StatePath        string                   `json:"state_path"`
	RegistrationPath string                   `json:"registration_path"`
	Candidate        estateregistry.Candidate `json:"candidate"`
}

type estateRegistrationContext struct {
	path        string
	candidate   estateregistry.Candidate
	file        estateregistry.FileObservation
	observation operations.Observation
}

type estateRegistrationObserver struct {
	services  *Services
	candidate estateregistry.Candidate
	path      string
}

func (observer estateRegistrationObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.estateRegistrationContext(
		ctx,
		observer.candidate.Document.Estate.Root,
		observer.path,
		observer.candidate.Document.DeviceID,
	)
	if len(findings) != 0 || current.candidate.Digest != observer.candidate.Digest ||
		current.observation.RepositoryID != repositoryID {
		return operations.Observation{}, errors.New("estate registration precondition is no longer proven")
	}
	return current.observation, nil
}

func (services *Services) PlanEstateRegistration(
	ctx context.Context,
	path string,
	options EstateRegistrationOptions,
) domain.Envelope {
	const command = "gds workspace register-estate plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	requestedRoot := strings.TrimSpace(options.EstateRoot)
	if requestedRoot == "" {
		requestedRoot = path
	}
	registrationPath, err := estateregistry.ResolvePath(
		strings.TrimSpace(options.RegistrationPath), os.Getenv, os.UserHomeDir,
	)
	if err != nil {
		return estateRegistrationError(command, options.RegistrationPath, err)
	}
	current, findings := services.estateRegistrationContext(
		ctx, requestedRoot, registrationPath, options.DeviceID,
	)
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
	parameters, err := estateregistry.Parameters(
		current.path,
		current.file,
		current.candidate,
	)
	if err != nil {
		return estateRegistrationError(command, current.path, err)
	}
	plan, err := operations.NewPlan(planID, now, now.Add(projectionPlanLifetime), operations.PlanInput{
		Operation: estateRegistrationOperation,
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID:        current.observation.RepositoryID,
			HeadOID:             current.observation.HeadOID,
			WorktreeFingerprint: current.observation.WorktreeFingerprint,
			ManifestDigest:      current.observation.ManifestDigest,
			PolicyDigest:        current.observation.PolicyDigest,
		}},
		Steps: []operations.Step{{
			StepID:           "materialize-estate-registration",
			RepositoryID:     current.observation.RepositoryID,
			Action:           estateregistry.MaterializeAction,
			RequiresApproval: true,
			Compensation: operations.Compensation{
				Mode: "explicit-plan", Action: "restore-estate-registration",
			},
			Parameters: parameters,
		}},
		ApprovalClass: "device-estate-registration",
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store,
		services.Schemas,
		estateRegistrationObserver{services: services, candidate: current.candidate, path: current.path},
		nil,
		options.DeviceID,
		options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	envelope := domain.Success(command, EstateRegistrationPlanData{
		Plan: plan, StatePath: statePath, RegistrationPath: current.path,
		Candidate: current.candidate,
	})
	envelope.Scope["repository_id"] = current.observation.RepositoryID
	return envelope
}

func (services *Services) ApplyEstateRegistration(
	ctx context.Context,
	planID string,
	options EstateRegistrationOptions,
) domain.Envelope {
	const command = "gds workspace register-estate apply"
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
	plan, registrationPath, candidate, err := loadEstateRegistrationPlan(
		ctx, store, planID, services.Schemas,
	)
	if err != nil {
		return estateRegistrationPlanInvalid(command)
	}
	handler, err := estateregistry.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store,
		services.Schemas,
		estateRegistrationObserver{services: services, candidate: candidate, path: registrationPath},
		map[string]operations.ActionHandler{estateregistry.MaterializeAction: handler},
		options.DeviceID,
		options.SessionID,
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
	envelope.Scope["repository_id"] = candidate.Document.Estate.RepositoryID
	return envelope
}

func (services *Services) VerifyEstateRegistration(
	ctx context.Context,
	operationID string,
	options EstateRegistrationOptions,
) domain.Envelope {
	const command = "gds workspace register-estate verify"
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
		return estateRegistrationPlanInvalid(command)
	}
	plan, _, candidate, err := loadEstateRegistrationPlan(
		ctx, store, operation.PlanID, services.Schemas,
	)
	if err != nil {
		return estateRegistrationPlanInvalid(command)
	}
	handler, err := estateregistry.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store,
		services.Schemas,
		estateRegistrationObserver{},
		map[string]operations.ActionHandler{estateregistry.MaterializeAction: handler},
		options.DeviceID,
		options.SessionID,
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
	envelope.Scope["repository_id"] = candidate.Document.Estate.RepositoryID
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) estateRegistrationContext(
	ctx context.Context,
	root string,
	registrationPath string,
	deviceID string,
) (estateRegistrationContext, []domain.Finding) {
	info, err := services.Git.RepositoryInfo(ctx, root)
	if err != nil {
		return estateRegistrationContext{}, []domain.Finding{estateRegistrationFinding(
			"GDS_ESTATE_REGISTRATION_ROOT_NOT_PROVEN", err.Error(), root,
		)}
	}
	anchorValue, findings := manifest.NewLoader(services.Schemas).LoadRepository(info.WorktreeRoot)
	if len(findings) != 0 {
		return estateRegistrationContext{}, findings
	}
	if !hasRole(anchorValue.Repository.Roles, "control-plane") {
		return estateRegistrationContext{}, []domain.Finding{estateRegistrationFinding(
			"GDS_ESTATE_REGISTRATION_ROLE_INVALID",
			"Estate registration requires a verified control-plane repository.", info.WorktreeRoot,
		)}
	}
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil || status.Head.OID == "" || status.Head.Mode != "branch" {
		return estateRegistrationContext{}, []domain.Finding{estateRegistrationFinding(
			"GDS_ESTATE_REGISTRATION_GIT_STATE_NOT_PROVEN",
			"Control-plane registration requires an attached repository HEAD.", info.WorktreeRoot,
		)}
	}
	anchorEvidence, err := anchor.Observe(info.WorktreeRoot)
	if err != nil || anchorEvidence.File.State != "regular" {
		return estateRegistrationContext{}, []domain.Finding{estateRegistrationFinding(
			"GDS_ESTATE_REGISTRATION_ANCHOR_NOT_PROVEN",
			"Control-plane repository anchor is unavailable.", info.WorktreeRoot,
		)}
	}
	candidate, findings := estateregistry.NewCandidate(
		deviceID,
		anchorValue.Repository.ID,
		info.WorktreeRoot,
		anchorEvidence.File.ContentDigest,
		services.Schemas,
	)
	if len(findings) != 0 {
		return estateRegistrationContext{}, findings
	}
	fileEvidence, err := estateregistry.Observe(registrationPath)
	if err != nil {
		return estateRegistrationContext{}, []domain.Finding{estateRegistrationFinding(
			"GDS_ESTATE_REGISTRATION_TARGET_NOT_PROVEN", err.Error(), registrationPath,
		)}
	}
	compiled := services.Compiler.CompileDirectory(
		info.WorktreeRoot, anchorValue, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return estateRegistrationContext{}, compiled.Findings
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Head      gitprovider.HeadState          `json:"head"`
		Anchor    anchor.FileObservation         `json:"anchor"`
		Registry  estateregistry.FileObservation `json:"registration"`
		Candidate string                         `json:"candidate_digest"`
	}{status.Head, anchorEvidence.File, fileEvidence.File, candidate.Digest})
	if err != nil {
		return estateRegistrationContext{}, []domain.Finding{estateRegistrationFinding(
			"GDS_ESTATE_REGISTRATION_FINGERPRINT_FAILED", err.Error(), registrationPath,
		)}
	}
	return estateRegistrationContext{
		path: registrationPath, candidate: candidate,
		file: fileEvidence.File,
		observation: operations.Observation{
			RepositoryID:        anchorValue.Repository.ID,
			HeadOID:             status.Head.OID,
			WorktreeFingerprint: fingerprint,
			ManifestDigest:      anchorEvidence.File.ContentDigest,
			PolicyDigest:        compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func loadEstateRegistrationPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, string, estateregistry.Candidate, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, "", estateregistry.Candidate{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != estateRegistrationOperation ||
		plan.PlanDigest != record.PlanDigest || len(plan.Steps) != 1 ||
		len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, "", estateregistry.Candidate{},
			errors.New("stored plan is not a valid estate registration plan")
	}
	path, candidate, err := estateregistry.StepCandidate(plan.Steps[0], schemas)
	return plan, path, candidate, err
}

func estateRegistrationPlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_ESTATE_REGISTRATION_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable estate registration plan.",
	})
}

func estateRegistrationError(command string, path string, err error) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitInput, nil, estateRegistrationFinding(
		"GDS_ESTATE_REGISTRATION_INVALID", err.Error(), path,
	))
}

func estateRegistrationFinding(code string, message string, path string) domain.Finding {
	evidence := map[string]any{}
	if strings.TrimSpace(path) != "" {
		evidence["path"] = filepath.Clean(path)
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: evidence,
	}
}
