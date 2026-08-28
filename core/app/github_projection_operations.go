package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubchange"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubmutationruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const githubProjectionOperation = "publish-github-projections"

type GitHubProjectionOperationOptions struct {
	GitHubReadOptions
	ProjectionOperationOptions
	MutationRuntimeConfig string
}

type GitHubProjectionPlanData struct {
	Plan          *operations.Plan          `json:"plan,omitempty"`
	StatePath     string                    `json:"state_path,omitempty"`
	Candidate     projections.Candidate     `json:"candidate"`
	Initial       githubchange.InitialState `json:"initial"`
	Assignment    estate.Assignment         `json:"assignment"`
	RequiredOps   []string                  `json:"required_operations"`
	NoChanges     bool                      `json:"no_changes"`
	ReadyForApply bool                      `json:"ready_for_apply"`
	ApplyBlocker  string                    `json:"apply_blocker,omitempty"`
}

type githubProjectionContext struct {
	root        string
	runtime     githubRuntime
	reader      *githubprovider.Client
	candidate   projections.Candidate
	assignment  estate.Assignment
	capability  estate.MutationCapability
	repository  githubprovider.Repository
	plan        operations.Plan
	initial     githubchange.InitialState
	requiredOps []string
	observation operations.Observation
	ready       bool
	blocker     string
}

type githubProjectionObserver struct {
	services *Services
	root     string
	reader   githubchange.Reader
	plan     operations.Plan
}

func (observer githubProjectionObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.projectionOperationContext(ctx, observer.root, ProjectionSourceOptions{})
	if len(findings) != 0 || current.repositoryID != repositoryID {
		return operations.Observation{}, errors.New("local projection context is no longer proven")
	}
	if err := matchProjectionCandidate(observer.plan, current.candidate); err != nil {
		return operations.Observation{}, err
	}
	digest, err := githubchange.InitialEvidenceDigest(ctx, observer.reader, observer.plan)
	if err != nil {
		return operations.Observation{}, err
	}
	current.observation.RemoteEvidenceDigest = digest
	return current.observation, nil
}

func (services *Services) PlanGitHubProjection(
	ctx context.Context,
	path string,
	options GitHubProjectionOperationOptions,
) domain.Envelope {
	const command = "gds github projection-pr plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, envelope := services.prepareGitHubProjectionContext(ctx, path, options, command)
	if envelope != nil {
		return *envelope
	}
	data := GitHubProjectionPlanData{
		Candidate: current.candidate, Initial: current.initial, Assignment: current.assignment,
		RequiredOps: current.requiredOps, NoChanges: len(current.plan.Steps) == 0,
		ReadyForApply: current.ready, ApplyBlocker: current.blocker,
	}
	if len(current.plan.Steps) == 0 {
		result := domain.Success(command, data)
		result.Scope["repository_id"] = current.observation.RepositoryID
		return result
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		githubProjectionObserver{services: services, root: current.root, reader: current.reader, plan: current.plan},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, current.plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	data.Plan = &current.plan
	data.StatePath = statePath
	result := domain.Success(command, data)
	result.Scope["repository_id"] = current.observation.RepositoryID
	return result
}

func (services *Services) ApplyGitHubProjection(
	ctx context.Context,
	path string,
	planID string,
	options GitHubProjectionOperationOptions,
) domain.Envelope {
	const command = "gds github projection-pr apply"
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
	plan, scope, err := loadGitHubProjectionPlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return githubProjectionPlanInvalid(command)
	}
	current, envelope := services.loadGitHubProjectionContext(ctx, path, options, plan, scope, command)
	if envelope != nil {
		return *envelope
	}
	if !current.ready {
		return domain.NewEnvelope(command, domain.ExitPolicy, map[string]any{
			"plan_id": plan.PlanID, "ready_for_apply": false,
		}, domain.Finding{
			Code: "GDS_GITHUB_PROJECTION_MUTATION_BLOCKED", Severity: domain.SeverityHigh,
			Message: current.blocker,
		})
	}
	mutationConfig, err := githubmutationruntime.Load(
		options.MutationRuntimeConfig, current.runtime.desired, services.Schemas,
	)
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	mutators, err := githubmutationruntime.BuildMutators(
		mutationConfig, current.runtime.config, current.runtime.desired,
		services.GitHubMutationRuntimeBuildOptions,
	)
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	factory, found := mutators[scope.MutationCapabilityID]
	if !found {
		return githubMutationRuntimeError(command, errors.New("mutation capability is unavailable"))
	}
	writer, err := factory.BindRepository(githubprovider.RepositoryMutationScope{
		RepositoryID: scope.ProviderRepositoryID, Owner: scope.Owner, Name: scope.Name,
		Operations: current.requiredOps,
	})
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	handlers := githubProjectionHandlers(plan, current.reader, writer, writer.Scope())
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		githubProjectionObserver{services: services, root: current.root, reader: current.reader, plan: plan},
		handlers, options.DeviceID, options.SessionID,
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
	value := domain.Success(command, result)
	value.Mutation.Attempted = result.MutationAttempted
	value.Mutation.Completed = result.MutationCompleted
	value.OperationID = result.OperationID
	value.Scope["repository_id"] = current.observation.RepositoryID
	return value
}

func (services *Services) VerifyGitHubProjection(
	ctx context.Context,
	path string,
	operationID string,
	options GitHubProjectionOperationOptions,
) domain.Envelope {
	const command = "gds github projection-pr verify"
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
	record, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return operationFailureEnvelope(command, err)
	}
	plan, scope, err := loadGitHubProjectionPlan(ctx, store, record.PlanID, services.Schemas)
	if err != nil {
		return githubProjectionPlanInvalid(command)
	}
	current, envelope := services.loadGitHubProjectionContext(ctx, path, options, plan, scope, command)
	if envelope != nil {
		return *envelope
	}
	bound := githubprovider.RepositoryMutationScope{
		RepositoryID: scope.ProviderRepositoryID, Owner: scope.Owner, Name: scope.Name,
		Operations: current.requiredOps,
	}
	handlers := githubProjectionHandlers(plan, current.reader, nil, bound)
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		githubProjectionObserver{services: services, root: current.root, reader: current.reader, plan: plan},
		handlers, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope(command, err)
		envelope.OperationID = operationID
		return envelope
	}
	value := domain.Success(command, result)
	value.OperationID = operationID
	value.Scope["repository_id"] = current.observation.RepositoryID
	return value
}

func (services *Services) prepareGitHubProjectionContext(
	ctx context.Context,
	path string,
	options GitHubProjectionOperationOptions,
	command string,
) (githubProjectionContext, *domain.Envelope) {
	local, findings := services.projectionOperationContext(ctx, path, ProjectionSourceOptions{})
	if len(findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return githubProjectionContext{}, &envelope
	}
	estateRoot, anchor, findings := services.policyInputs(ctx, local.root)
	if len(findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return githubProjectionContext{}, &envelope
	}
	readOptions := options.GitHubReadOptions
	readOptions.EstateRoot = estateRoot
	readOptions.InstallationID = anchor.Provider.Installation
	runtime, runtimeEnvelope := services.loadGitHubRuntime(ctx, local.root, readOptions, command)
	if runtimeEnvelope != nil {
		return githubProjectionContext{}, runtimeEnvelope
	}
	reader, found := runtime.readers[anchor.Provider.Installation]
	if !found {
		envelope := githubProjectionScopeInvalid(command, "Canonical read installation is unavailable.")
		return githubProjectionContext{}, &envelope
	}
	repository, _, notModified, err := reader.GetRepository(
		ctx, anchor.Provider.Owner, anchor.Provider.Name, "",
	)
	if err != nil || notModified {
		envelope := githubReadError(command, anchor.Provider.Installation, err)
		return githubProjectionContext{}, &envelope
	}
	assignment, capability, envelope := services.githubProjectionPolicyContext(
		runtime, anchor, repository, command,
	)
	if envelope != nil {
		return githubProjectionContext{}, envelope
	}
	base, _, err := reader.GetBranchRef(
		ctx, repository.Owner, repository.Name, repository.DefaultBranch,
	)
	if err != nil {
		envelope := githubReadError(command, anchor.Provider.Installation, err)
		return githubProjectionContext{}, &envelope
	}
	headBranch, err := projectionBranchName(local.candidate.OutputDigest, base.SHA)
	if err != nil {
		envelope := githubProjectionPreparationError(command, err)
		return githubProjectionContext{}, &envelope
	}
	desired := make([]githubchange.DesiredFile, len(local.candidate.Files))
	for index, file := range local.candidate.Files {
		desired[index] = githubchange.DesiredFile{
			Path: file.Path, Message: "chore: update GDS projection " + file.Path,
			Content: append([]byte(nil), file.Content...),
		}
	}
	prepared, err := githubchange.Prepare(ctx, reader, githubchange.PrepareInput{
		RepositoryID: local.repositoryID, ReadInstallationID: anchor.Provider.Installation,
		MutationCapabilityID: capability.Mutation.ID,
		ProviderRepositoryID: repository.ID, Owner: repository.Owner, Name: repository.Name,
		BaseBranch: repository.DefaultBranch, HeadBranch: headBranch,
		Title: "chore: update GDS projections", Body: projectionPullRequestBody(local.candidate),
		Files: desired,
	})
	if err != nil {
		envelope := githubProjectionPreparationError(command, err)
		return githubProjectionContext{}, &envelope
	}
	if prepared.Initial.Base.SHA != base.SHA {
		envelope := githubProjectionPreparationError(
			command, errors.New("GitHub default branch changed during projection planning"),
		)
		return githubProjectionContext{}, &envelope
	}
	context := githubProjectionContext{
		root: local.root, runtime: runtime, reader: reader, candidate: local.candidate,
		assignment: assignment, capability: capability, repository: repository,
		initial: prepared.Initial, observation: local.observation,
	}
	context.observation.RemoteEvidenceDigest = prepared.EvidenceDigest
	if prepared.NoChanges {
		context.ready = false
		context.blocker = "Generated projections already match the provider default branch."
		return context, nil
	}
	context.plan, err = newGitHubProjectionPlan(services, options, local.observation, prepared)
	if err != nil {
		envelope := domain.InternalError(command, err)
		return githubProjectionContext{}, &envelope
	}
	context.requiredOps = githubProjectionRequiredOperations(context.plan)
	context.ready, context.blocker = githubProjectionMutationGate(
		runtime.desired, assignment, capability, anchor.Repository.Lifecycle, context.requiredOps,
	)
	return context, nil
}

func (services *Services) loadGitHubProjectionContext(
	ctx context.Context,
	path string,
	options GitHubProjectionOperationOptions,
	plan operations.Plan,
	scope githubchange.Scope,
	command string,
) (githubProjectionContext, *domain.Envelope) {
	local, findings := services.projectionOperationContext(ctx, path, ProjectionSourceOptions{})
	if len(findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return githubProjectionContext{}, &envelope
	}
	if local.repositoryID != plan.Steps[0].RepositoryID || matchProjectionCandidate(plan, local.candidate) != nil {
		envelope := githubProjectionScopeInvalid(command, "Local generated projections differ from the immutable plan.")
		return githubProjectionContext{}, &envelope
	}
	estateRoot, anchor, findings := services.policyInputs(ctx, local.root)
	if len(findings) != 0 || anchor.Provider.Installation != scope.ReadInstallationID ||
		anchor.Provider.RepositoryID != scope.ProviderRepositoryID ||
		!strings.EqualFold(anchor.Provider.Owner, scope.Owner) ||
		!strings.EqualFold(anchor.Provider.Name, scope.Name) {
		envelope := githubProjectionScopeInvalid(command, "Local repository identity differs from the immutable plan.")
		return githubProjectionContext{}, &envelope
	}
	readOptions := options.GitHubReadOptions
	readOptions.EstateRoot = estateRoot
	readOptions.InstallationID = scope.ReadInstallationID
	runtime, runtimeEnvelope := services.loadGitHubRuntime(ctx, local.root, readOptions, command)
	if runtimeEnvelope != nil {
		return githubProjectionContext{}, runtimeEnvelope
	}
	reader, found := runtime.readers[scope.ReadInstallationID]
	if !found {
		envelope := githubProjectionScopeInvalid(command, "Planned read installation is unavailable.")
		return githubProjectionContext{}, &envelope
	}
	repository, _, notModified, err := reader.GetRepository(ctx, scope.Owner, scope.Name, "")
	if err != nil || notModified || repository.ID != scope.ProviderRepositoryID ||
		repository.DefaultBranch != scope.BaseBranch {
		envelope := githubProjectionScopeInvalid(command, "Provider repository identity differs from the immutable plan.")
		return githubProjectionContext{}, &envelope
	}
	assignment, capability, envelope := services.githubProjectionPolicyContext(
		runtime, anchor, repository, command,
	)
	if envelope != nil || capability.Mutation.ID != scope.MutationCapabilityID {
		if envelope != nil {
			return githubProjectionContext{}, envelope
		}
		invalid := githubProjectionScopeInvalid(command, "Mutation capability differs from the immutable plan.")
		return githubProjectionContext{}, &invalid
	}
	digest, err := githubchange.InitialEvidenceDigest(ctx, reader, plan)
	if err != nil {
		envelope := githubProjectionPreparationError(command, err)
		return githubProjectionContext{}, &envelope
	}
	required := githubProjectionRequiredOperations(plan)
	ready, blocker := githubProjectionMutationGate(
		runtime.desired, assignment, capability, anchor.Repository.Lifecycle, required,
	)
	local.observation.RemoteEvidenceDigest = digest
	return githubProjectionContext{
		root: local.root, runtime: runtime, reader: reader, candidate: local.candidate,
		assignment: assignment, capability: capability, repository: repository,
		plan: plan, requiredOps: required, observation: local.observation,
		ready: ready, blocker: blocker,
	}, nil
}

func (services *Services) githubProjectionPolicyContext(
	runtime githubRuntime,
	anchor domain.RepositoryAnchor,
	repository githubprovider.Repository,
	command string,
) (estate.Assignment, estate.MutationCapability, *domain.Envelope) {
	if repository.ID != anchor.Provider.RepositoryID ||
		!strings.EqualFold(repository.Owner, anchor.Provider.Owner) ||
		!strings.EqualFold(repository.Name, anchor.Provider.Name) ||
		repository.DefaultBranch != anchor.Git.DefaultBranch || repository.Disabled ||
		repository.Visibility != anchor.Classification.VisibilityContract ||
		repository.Archived != (anchor.Repository.Lifecycle == "archived") {
		envelope := githubProjectionScopeInvalid(command, "Provider repository differs from the canonical anchor.")
		return estate.Assignment{}, estate.MutationCapability{}, &envelope
	}
	compiled, findings := estate.Compile(runtime.desired, []estate.ObservedRepository{{
		ProviderID: repository.ID, Owner: repository.Owner, Name: repository.Name,
		Fork: repository.Fork, Archived: repository.Archived,
		Visibility: repository.Visibility, DefaultBranch: repository.DefaultBranch,
	}})
	if len(findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return estate.Assignment{}, estate.MutationCapability{}, &envelope
	}
	if len(compiled.Repositories) != 1 {
		envelope := githubProjectionScopeInvalid(command, "Canonical estate classification is ambiguous.")
		return estate.Assignment{}, estate.MutationCapability{}, &envelope
	}
	capability, found := mutationCapabilityForInstallation(runtime.desired, anchor.Provider.Installation)
	if !found {
		envelope := githubProjectionScopeInvalid(command, "Canonical mutation capability is unavailable.")
		return estate.Assignment{}, estate.MutationCapability{}, &envelope
	}
	return compiled.Repositories[0], capability, nil
}

func newGitHubProjectionPlan(
	services *Services,
	options GitHubProjectionOperationOptions,
	local operations.Observation,
	prepared githubchange.Prepared,
) (operations.Plan, error) {
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return operations.Plan{}, err
	}
	return operations.NewPlan(planID, now, now.Add(projectionPlanLifetime), operations.PlanInput{
		Operation: githubProjectionOperation,
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: local.RepositoryID, HeadOID: local.HeadOID,
			WorktreeFingerprint:  local.WorktreeFingerprint,
			RemoteEvidenceDigest: prepared.EvidenceDigest,
			ManifestDigest:       local.ManifestDigest, PolicyDigest: local.PolicyDigest,
		}},
		Steps: prepared.Build.Steps, ApprovalClass: "github-projection-write",
	})
}

func loadGitHubProjectionPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, githubchange.Scope, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, githubchange.Scope{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != githubProjectionOperation || plan.PlanDigest != record.PlanDigest ||
		len(plan.Validate(schemas)) != 0 || githubchange.ValidatePlan(plan) != nil {
		return operations.Plan{}, githubchange.Scope{}, errors.New("stored plan is not a valid GitHub projection operation")
	}
	parameters, err := githubchange.StepParameters(plan.Steps[0])
	if err != nil {
		return operations.Plan{}, githubchange.Scope{}, err
	}
	return plan, parameters.Scope, nil
}

func matchProjectionCandidate(plan operations.Plan, candidate projections.Candidate) error {
	if err := githubchange.ValidatePlan(plan); err != nil {
		return err
	}
	candidateFiles := make(map[string]projections.File, len(candidate.Files))
	for _, file := range candidate.Files {
		candidateFiles[file.Path] = file
	}
	for _, step := range plan.Steps[1 : len(plan.Steps)-1] {
		parameters, err := githubchange.StepParameters(step)
		if err != nil || parameters.Content == nil {
			return errors.New("GitHub projection content plan is invalid")
		}
		content, err := githubchange.DecodeContent(*parameters.Content)
		candidateFile, found := candidateFiles[parameters.Content.Path]
		if err != nil || !found || candidateFile.Digest != parameters.Content.ContentDigest ||
			!bytes.Equal(candidateFile.Content, content) {
			return errors.New("GitHub projection plan differs from the generated candidate")
		}
	}
	last, _ := githubchange.StepParameters(plan.Steps[len(plan.Steps)-1])
	if last.PullRequest.Title != "chore: update GDS projections" ||
		last.PullRequest.Body != projectionPullRequestBody(candidate) {
		return errors.New("GitHub projection pull-request metadata differs from the generated candidate")
	}
	return nil
}

func githubProjectionRequiredOperations(plan operations.Plan) []string {
	values := map[string]struct{}{
		githubprovider.MutationBranch: {}, githubprovider.MutationPullRequest: {},
	}
	for _, step := range plan.Steps {
		if step.Action != githubchange.ContentAction {
			continue
		}
		parameters, err := githubchange.StepParameters(step)
		if err != nil || parameters.Content == nil {
			continue
		}
		operation := githubprovider.MutationContent
		if strings.HasPrefix(parameters.Content.Path, ".github/workflows/") {
			operation = githubprovider.MutationWorkflowCaller
		}
		values[operation] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func githubProjectionMutationGate(
	desired estate.Config,
	assignment estate.Assignment,
	capability estate.MutationCapability,
	lifecycle string,
	required []string,
) (bool, string) {
	if desired.Root.Rollout.MutationMode == "disabled" {
		return false, "Canonical estate mutation_mode is disabled."
	}
	if assignment.ManagementMode != "managed" {
		return false, "Repository assignment is not managed by the canonical estate."
	}
	if !governanceContains(capability.Scope.ManagementModes, assignment.ManagementMode) ||
		!governanceContains(capability.Scope.Lifecycles, lifecycle) {
		return false, "Mutation capability scope does not include the repository state."
	}
	for _, operation := range required {
		if !governanceContains(capability.Operations, operation) {
			return false, fmt.Sprintf("Mutation capability does not permit %s operations.", operation)
		}
	}
	return true, ""
}

func githubProjectionHandlers(
	plan operations.Plan,
	reader githubchange.Reader,
	writer githubchange.Writer,
	scope githubprovider.RepositoryMutationScope,
) map[string]operations.ActionHandler {
	handlers := map[string]operations.ActionHandler{}
	for _, step := range plan.Steps {
		handlers[step.Action] = &githubchange.Handler{
			Reader: reader, Writer: writer, Scope: scope, Action: step.Action,
		}
	}
	return handlers
}

func projectionBranchName(outputDigest string, baseSHA string) (string, error) {
	output := strings.TrimPrefix(outputDigest, "sha256:")
	if len(output) != 64 || (len(baseSHA) != 40 && len(baseSHA) != 64) {
		return "", errors.New("projection or base digest is invalid")
	}
	return "gds/projection-" + output[:16] + "-" + baseSHA[:12], nil
}

func projectionPullRequestBody(candidate projections.Candidate) string {
	return fmt.Sprintf(
		"Generated by GDS from canonical repository sources.\n\n"+
			"Projection input digest: `%s`\n"+
			"Projection output digest: `%s`\n"+
			"Generated files: %d\n\n"+
			"Do not edit generated projections directly.\n",
		candidate.InputDigest, candidate.OutputDigest, len(candidate.Files),
	)
}

func githubProjectionPreparationError(command string, err error) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitStale, nil, domain.Finding{
		Code: "GDS_GITHUB_PROJECTION_STATE_CHANGED", Severity: domain.SeverityHigh,
		Message: err.Error(),
	})
}

func githubProjectionScopeInvalid(command string, message string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitSecurity, nil, domain.Finding{
		Code: "GDS_GITHUB_PROJECTION_SCOPE_INVALID", Severity: domain.SeverityCritical,
		Message: message,
	})
}

func githubProjectionPlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_GITHUB_PROJECTION_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable GitHub projection operation.",
	})
}
