package app

import (
	"context"
	"errors"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitcontracts"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubmutationruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitops"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	repositoryworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/repository"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type RepositoryTransitionOptions struct {
	ProjectionOperationOptions
	GitHubReadOptions
	AnchorPath            string
	MutationRuntimeConfig string
}

type RepositoryTransitionPlanData struct {
	Plan          operations.Plan                       `json:"plan"`
	StatePath     string                                `json:"state_path"`
	Target        string                                `json:"target"`
	Candidate     anchor.Candidate                      `json:"candidate"`
	Transition    repositoryworkflow.ProviderTransition `json:"transition"`
	ReadyForApply bool                                  `json:"ready_for_apply"`
	ApplyBlocker  string                                `json:"apply_blocker"`
}

type repositoryProviderRuntime struct {
	runtime    githubRuntime
	readers    map[string]repositoryworkflow.ProviderReader
	capability estate.MutationCapability
	assignment estate.Assignment
	repository githubprovider.Repository
	ready      bool
	blocker    string
}

type repositoryTransitionObserver struct {
	local      anchorChangeObserver
	reader     repositoryworkflow.ProviderReader
	transition repositoryworkflow.ProviderTransition
}

func (observer repositoryTransitionObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	observation, err := observer.local.Observe(ctx, repositoryID)
	if err != nil {
		return operations.Observation{}, err
	}
	repository, _, notModified, err := observer.reader.GetRepository(
		ctx, observer.transition.CurrentOwner, observer.transition.CurrentName, "",
	)
	if err != nil || notModified || repository.ID != observer.transition.ProviderRepositoryID {
		return operations.Observation{}, errors.New("GitHub repository lifecycle precondition is no longer proven")
	}
	digest, err := repositoryworkflow.ProviderDigest(repository)
	if err != nil {
		return operations.Observation{}, err
	}
	observation.RemoteEvidenceDigest = digest
	return observation, nil
}

func (services *Services) PlanRepositoryTransition(
	ctx context.Context,
	path string,
	operation string,
	options RepositoryTransitionOptions,
) domain.Envelope {
	command := "gds repository " + operation + " plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	candidate, findings := services.loadAnchorCandidate(options.AnchorPath)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	_, currentAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	current, findings := services.anchorChangeContext(ctx, path, candidate)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	transition, findings := repositoryworkflow.ValidateTransition(
		operation, currentAnchor, candidate.Anchor,
	)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	providerRuntime, runtimeEnvelope := services.repositoryProviderRuntime(
		ctx, path, currentAnchor, transition, options.GitHubReadOptions, command,
	)
	if runtimeEnvelope != nil {
		return *runtimeEnvelope
	}
	transition.MutationCapabilityID = providerRuntime.capability.Mutation.ID
	providerDigest, err := repositoryworkflow.ProviderDigest(providerRuntime.repository)
	if err != nil {
		return domain.InternalError(command, err)
	}
	transition.ExpectedProviderDigest = providerDigest
	topology, err := services.Git.InspectTopology(ctx, current.root)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitNotProven, nil, repositoryTransitionFinding(
			"GDS_REPOSITORY_TRANSITION_REMOTE_NOT_PROVEN", err.Error(),
		))
	}
	remoteFindings := gitcontracts.ValidateRemote(topology, "origin", gitcontracts.ExpectedRepository{
		Owner: currentAnchor.Provider.Owner, Name: currentAnchor.Provider.Name,
	})
	if len(remoteFindings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(remoteFindings), nil, remoteFindings...)
	}
	currentRemoteURL, err := gitcontracts.ExactRemoteURL(topology, "origin")
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitNotProven, nil, repositoryTransitionFinding(
			"GDS_REPOSITORY_TRANSITION_REMOTE_AMBIGUOUS", err.Error(),
		))
	}
	targetRemoteURL := currentRemoteURL
	if operation == repositoryworkflow.RenameOperation || operation == repositoryworkflow.TransferOperation {
		targetRemoteURL, err = gitprovider.RewriteGitHubRepositoryURL(
			currentRemoteURL, transition.TargetOwner, transition.TargetName,
		)
		if err != nil {
			return domain.NewEnvelope(command, domain.ExitValidation, nil, repositoryTransitionFinding(
				"GDS_REPOSITORY_TRANSITION_REMOTE_TARGET_INVALID", err.Error(),
			))
		}
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
	steps := []operations.Step{{
		StepID: "change-provider-repository", RepositoryID: current.observation.RepositoryID,
		Action: repositoryworkflow.ProviderLifecycleAction, RequiresApproval: true,
		Compensation: operations.Compensation{Mode: "manual"},
		Parameters:   repositoryworkflow.Parameters(transition),
	}}
	if targetRemoteURL != currentRemoteURL {
		steps = append(steps, operations.Step{
			StepID: "update-origin-remote", RepositoryID: current.observation.RepositoryID,
			Action: gitops.UpdateRemoteAction, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "manual"},
			Parameters: gitops.RemoteUpdateParameters(
				current.root, "origin", currentRemoteURL, targetRemoteURL,
			),
		})
	}
	steps = append(steps, operations.Step{
		StepID: "materialize-repository-anchor", RepositoryID: current.observation.RepositoryID,
		Action: anchor.MaterializeAction, RequiresApproval: true,
		Compensation: operations.Compensation{Mode: "manual"},
		Parameters:   anchor.Parameters(current.root, current.file, candidate),
	})
	plan, err := operations.NewPlan(planID, now, now.Add(projectionPlanLifetime), operations.PlanInput{
		Operation: operation + "-repository",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			WorktreeFingerprint:  current.observation.WorktreeFingerprint,
			ManifestDigest:       current.observation.ManifestDigest,
			PolicyDigest:         current.observation.PolicyDigest,
			RemoteEvidenceDigest: transition.ExpectedProviderDigest,
		}},
		Steps:         steps,
		ApprovalClass: operation + "-github-repository",
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		repositoryTransitionObserver{
			local:  anchorChangeObserver{services: services, root: current.root, candidate: candidate},
			reader: providerRuntime.readers[transition.CurrentInstallation], transition: transition,
		},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	resultEnvelope := domain.Success(command, RepositoryTransitionPlanData{
		Plan: plan, StatePath: statePath, Target: current.root, Candidate: candidate,
		Transition: transition, ReadyForApply: providerRuntime.ready,
		ApplyBlocker: providerRuntime.blocker,
	})
	resultEnvelope.Scope["repository_id"] = current.observation.RepositoryID
	return resultEnvelope
}

func (services *Services) ApplyRepositoryTransition(
	ctx context.Context,
	path string,
	operation string,
	planID string,
	options RepositoryTransitionOptions,
) domain.Envelope {
	command := "gds repository " + operation + " apply"
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
	plan, transition, candidate, err := loadRepositoryTransitionPlan(
		ctx, store, planID, operation, services.Schemas,
	)
	if err != nil {
		return repositoryTransitionPlanInvalid(command)
	}
	_, currentAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	current, findings := services.anchorChangeContext(ctx, path, candidate)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	providerRuntime, runtimeEnvelope := services.repositoryProviderRuntime(
		ctx, path, currentAnchor, transition, options.GitHubReadOptions, command,
	)
	if runtimeEnvelope != nil {
		return *runtimeEnvelope
	}
	if !providerRuntime.ready {
		return domain.NewEnvelope(command, domain.ExitPolicy, map[string]any{
			"plan_id": plan.PlanID, "transition": transition, "ready_for_apply": false,
		}, repositoryTransitionFinding("GDS_REPOSITORY_PROVIDER_MUTATION_BLOCKED", providerRuntime.blocker))
	}
	mutationConfig, err := githubmutationruntime.Load(
		options.MutationRuntimeConfig, providerRuntime.runtime.desired, services.Schemas,
	)
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	mutators, err := githubmutationruntime.BuildMutators(
		mutationConfig, providerRuntime.runtime.config, providerRuntime.runtime.desired,
		services.GitHubMutationRuntimeBuildOptions,
	)
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	factory := mutators[transition.MutationCapabilityID]
	if factory == nil {
		return githubMutationRuntimeError(command, errors.New("repository mutation capability is unavailable"))
	}
	writer, err := factory.BindRepository(githubprovider.RepositoryMutationScope{
		RepositoryID: transition.ProviderRepositoryID,
		Owner:        transition.CurrentOwner, Name: transition.CurrentName,
		Operations: []string{githubprovider.MutationRepositoryLifecycle},
	})
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	providerHandler := &repositoryworkflow.ProviderHandler{Readers: providerRuntime.readers, Writer: writer}
	remoteHandler := &gitops.UpdateRemoteHandler{Git: services.GitMutations}
	anchorHandler, err := anchor.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError(command, err)
	}
	observer := repositoryTransitionObserver{
		local:  anchorChangeObserver{services: services, root: current.root, candidate: candidate},
		reader: providerRuntime.readers[transition.CurrentInstallation], transition: transition,
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, observer,
		map[string]operations.ActionHandler{
			repositoryworkflow.ProviderLifecycleAction: providerHandler,
			gitops.UpdateRemoteAction:                  remoteHandler,
			anchor.MaterializeAction:                   anchorHandler,
		}, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, planID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope(command, err)
		envelope.Data = result
		envelope.OperationID = result.OperationID
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		return envelope
	}
	envelopeValue := domain.Success(command, result)
	envelopeValue.OperationID = result.OperationID
	envelopeValue.Mutation.Attempted = result.MutationAttempted
	envelopeValue.Mutation.Completed = result.MutationCompleted
	envelopeValue.Scope["repository_id"] = transition.RepositoryID
	return envelopeValue
}

func (services *Services) repositoryProviderRuntime(
	ctx context.Context,
	path string,
	anchorValue domain.RepositoryAnchor,
	transition repositoryworkflow.ProviderTransition,
	options GitHubReadOptions,
	command string,
) (repositoryProviderRuntime, *domain.Envelope) {
	options.InstallationID = ""
	runtime, runtimeEnvelope := services.loadGitHubRuntime(ctx, path, options, command)
	if runtimeEnvelope != nil {
		return repositoryProviderRuntime{}, runtimeEnvelope
	}
	readers := make(map[string]repositoryworkflow.ProviderReader, len(runtime.readers))
	for id, reader := range runtime.readers {
		readers[id] = reader
	}
	reader := readers[transition.CurrentInstallation]
	if reader == nil || readers[transition.TargetInstallation] == nil {
		envelope := domain.NewEnvelope(command, domain.ExitPolicy, nil, repositoryTransitionFinding(
			"GDS_REPOSITORY_PROVIDER_INSTALLATION_MISSING",
			"Current and target repository installations must both have read-App bindings.",
		))
		return repositoryProviderRuntime{}, &envelope
	}
	repository, _, notModified, err := reader.GetRepository(
		ctx, transition.CurrentOwner, transition.CurrentName, "",
	)
	if err != nil || notModified {
		envelope := githubReadError(command, transition.CurrentInstallation, err)
		return repositoryProviderRuntime{}, &envelope
	}
	if repository.ID != transition.ProviderRepositoryID ||
		anchorValue.Provider.RepositoryID != repository.ID ||
		!strings.EqualFold(repository.Owner, transition.CurrentOwner) ||
		!strings.EqualFold(repository.Name, transition.CurrentName) {
		envelope := domain.NewEnvelope(command, domain.ExitSecurity, nil, repositoryTransitionFinding(
			"GDS_REPOSITORY_PROVIDER_IDENTITY_MISMATCH",
			"Current GitHub repository evidence differs from the immutable local anchor.",
		))
		return repositoryProviderRuntime{}, &envelope
	}
	compiled, findings := estate.Compile(runtime.desired, []estate.ObservedRepository{{
		ProviderID: repository.ID, Owner: repository.Owner, Name: repository.Name,
		Fork: repository.Fork, Archived: repository.Archived,
		Visibility: repository.Visibility, DefaultBranch: repository.DefaultBranch,
	}})
	if len(findings) != 0 || len(compiled.Repositories) != 1 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return repositoryProviderRuntime{}, &envelope
	}
	capability, found := mutationCapabilityForInstallation(runtime.desired, transition.CurrentInstallation)
	if !found {
		envelope := domain.NewEnvelope(command, domain.ExitPolicy, nil, repositoryTransitionFinding(
			"GDS_REPOSITORY_MUTATION_CAPABILITY_MISSING",
			"No mutation capability is bound to the current repository installation.",
		))
		return repositoryProviderRuntime{}, &envelope
	}
	ready, blocker := repositoryProviderMutationGate(
		runtime.desired, compiled.Repositories[0], capability,
		transition.CurrentLifecycle, transition.Operation,
	)
	return repositoryProviderRuntime{
		runtime: runtime, readers: readers, capability: capability,
		assignment: compiled.Repositories[0], repository: repository,
		ready: ready, blocker: blocker,
	}, nil
}

func repositoryProviderMutationGate(
	desired estate.Config,
	assignment estate.Assignment,
	capability estate.MutationCapability,
	lifecycle string,
	operation string,
) (bool, string) {
	if operation == repositoryworkflow.TransferOperation {
		return false, repositoryworkflow.TransferApplyBlocker
	}
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
	required := githubprovider.MutationRepositoryLifecycle
	if operation == repositoryworkflow.DeleteOperation {
		required = githubprovider.MutationRepositoryDelete
	}
	if !governanceContains(capability.Operations, required) {
		return false, "Mutation capability does not permit the repository lifecycle operation."
	}
	return true, ""
}

func (services *Services) VerifyRepositoryTransition(
	ctx context.Context,
	path string,
	operation string,
	operationID string,
	options RepositoryTransitionOptions,
) domain.Envelope {
	command := "gds repository " + operation + " verify"
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
		return repositoryTransitionPlanInvalid(command)
	}
	plan, transition, candidate, err := loadRepositoryTransitionPlan(
		ctx, store, record.PlanID, operation, services.Schemas,
	)
	if err != nil {
		return repositoryTransitionPlanInvalid(command)
	}
	readOptions := options.GitHubReadOptions
	readOptions.InstallationID = ""
	runtime, runtimeEnvelope := services.loadGitHubRuntime(ctx, path, readOptions, command)
	if runtimeEnvelope != nil {
		return *runtimeEnvelope
	}
	readers := make(map[string]repositoryworkflow.ProviderReader, len(runtime.readers))
	for id, reader := range runtime.readers {
		readers[id] = reader
	}
	providerHandler := &repositoryworkflow.ProviderHandler{
		Readers: readers,
		Scope: githubprovider.RepositoryMutationScope{
			RepositoryID: transition.ProviderRepositoryID,
			Owner:        transition.CurrentOwner, Name: transition.CurrentName,
			Operations: []string{githubprovider.MutationRepositoryLifecycle},
		},
	}
	remoteHandler := &gitops.UpdateRemoteHandler{Git: services.GitMutations}
	anchorHandler, err := anchor.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, anchorChangeObserver{},
		map[string]operations.ActionHandler{
			repositoryworkflow.ProviderLifecycleAction: providerHandler,
			gitops.UpdateRemoteAction:                  remoteHandler,
			anchor.MaterializeAction:                   anchorHandler,
		}, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope(command, err)
		envelope.OperationID = operationID
		return envelope
	}
	envelopeValue := domain.Success(command, result)
	envelopeValue.OperationID = operationID
	envelopeValue.Scope["repository_id"] = candidate.Anchor.Repository.ID
	envelopeValue.Scope["repositories"] = plan.Scope.Repositories
	return envelopeValue
}

func loadRepositoryTransitionPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	operation string,
	schemas *validation.Set,
) (operations.Plan, repositoryworkflow.ProviderTransition, anchor.Candidate, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, repositoryworkflow.ProviderTransition{}, anchor.Candidate{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	expectedSteps := 2
	if operation == repositoryworkflow.RenameOperation || operation == repositoryworkflow.TransferOperation {
		expectedSteps = 3
	}
	if err != nil || plan.Operation != operation+"-repository" || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) != expectedSteps || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, repositoryworkflow.ProviderTransition{}, anchor.Candidate{},
			errors.New("stored plan is not a valid repository provider transition")
	}
	transition, err := repositoryworkflow.StepTransition(plan.Steps[0])
	if err != nil || transition.Operation != operation {
		return operations.Plan{}, repositoryworkflow.ProviderTransition{}, anchor.Candidate{},
			errors.New("repository provider transition parameters are invalid")
	}
	anchorStep := plan.Steps[len(plan.Steps)-1]
	root, candidate, err := anchor.StepCandidate(anchorStep, schemas)
	if err != nil || candidate.Anchor.Repository.ID != transition.RepositoryID ||
		candidate.Anchor.Provider.RepositoryID != transition.ProviderRepositoryID ||
		candidate.Anchor.Provider.Installation != transition.TargetInstallation ||
		candidate.Anchor.Provider.Owner != transition.TargetOwner ||
		candidate.Anchor.Provider.Name != transition.TargetName ||
		candidate.Anchor.Repository.Lifecycle != transition.TargetLifecycle {
		return operations.Plan{}, repositoryworkflow.ProviderTransition{}, anchor.Candidate{},
			errors.New("repository transition candidate differs from provider parameters")
	}
	if expectedSteps == 3 {
		remoteRoot, remote, expectedURL, targetURL, remoteErr := gitops.RemoteUpdateStep(plan.Steps[1])
		expectedRepository, expectedErr := gitprovider.ParseGitHubRepository(expectedURL)
		targetRepository, targetErr := gitprovider.ParseGitHubRepository(targetURL)
		if remoteErr != nil || expectedErr != nil || targetErr != nil || remoteRoot != root || remote != "origin" ||
			!strings.EqualFold(expectedRepository.Owner, transition.CurrentOwner) ||
			!strings.EqualFold(expectedRepository.Name, transition.CurrentName) ||
			!strings.EqualFold(targetRepository.Owner, transition.TargetOwner) ||
			!strings.EqualFold(targetRepository.Name, transition.TargetName) {
			return operations.Plan{}, repositoryworkflow.ProviderTransition{}, anchor.Candidate{},
				errors.New("repository remote update differs from provider transition")
		}
	}
	return plan, transition, candidate, nil
}

func repositoryTransitionPlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, repositoryTransitionFinding(
		"GDS_REPOSITORY_TRANSITION_PLAN_INVALID",
		"The selected plan is not a valid immutable repository provider transition.",
	))
}

func repositoryTransitionFinding(code string, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
