package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitcontracts"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubmutationruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	repositoryworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/repository"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type RepositoryDeleteOptions struct {
	ProjectionOperationOptions
	GitHubReadOptions
	MutationRuntimeConfig string
	InventoryRoot         string
	MaxDepth              int
	MaxRepositories       int
	Concurrency           int
	ConfirmRepositoryID   string
	ConfirmProviderID     string
	// PreserveIdentities names, exactly, what the operator accepts losing. A
	// blanket "yes I am sure" flag would restore the behaviour the retirement
	// evidence exists to remove, so preservation is stated per identity or not
	// at all.
	PreserveIdentities []string
}

type RepositoryDeletePlanData struct {
	Plan          operations.Plan                       `json:"plan"`
	StatePath     string                                `json:"state_path"`
	Transition    repositoryworkflow.ProviderTransition `json:"transition"`
	Index         estate.IdentityIndex                  `json:"index"`
	ReadyForApply bool                                  `json:"ready_for_apply"`
	ApplyBlocker  string                                `json:"apply_blocker"`
	Retirement    repositoryworkflow.RetirementEvidence `json:"retirement"`
}

// Repository deletion turns on whether the repository is finished, not on
// whether one checkout is clean.
//
// The preconditions the plan always had -- archived lifecycle, a complete
// relationship index with no reference in either direction, a clean attached
// default-branch checkout, exact provider identity, a signed approval -- observe
// the repository's *placement*. None of them observe its *contents*. A second
// worktree, an unpushed branch, a commit reachable only locally, a remote branch,
// an open or closed-unmerged pull request, an unresolved review conversation or
// an open issue would all have survived every one of them and then been deleted.
//
// `RepositoryRetirementEvidence/v1` closes that. Everything the device and the
// provider hold is enumerated and classified as completed, preserved, blocking
// or unknown; unknown blocks, because a page that could not be read and an empty
// one are the same bytes and opposite meanings. The claim is bound into the
// plan's precondition and rebuilt immediately before the mutation, so an
// approval cannot outlive the state that justified it.

type repositoryDeleteContext struct {
	root        string
	transition  repositoryworkflow.ProviderTransition
	index       estate.IdentityIndex
	observation operations.Observation
}

type repositoryDeleteObserver struct {
	services        *Services
	root            string
	inventoryRoot   string
	maxDepth        int
	maxRepositories int
	concurrency     int
	reader          repositoryworkflow.ProviderReader
	retirement      RetirementReader
	preserve        []string
	transition      repositoryworkflow.ProviderTransition
}

func (observer repositoryDeleteObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.repositoryDeleteContext(ctx, observer.root, RepositoryDeleteOptions{
		InventoryRoot: observer.inventoryRoot, MaxDepth: observer.maxDepth,
		MaxRepositories: observer.maxRepositories, Concurrency: observer.concurrency,
	})
	if len(findings) != 0 || current.transition.RepositoryID != repositoryID {
		return operations.Observation{}, errors.New("repository delete precondition is no longer proven")
	}
	if observer.reader != nil {
		repository, _, notModified, err := observer.reader.GetRepository(
			ctx, observer.transition.CurrentOwner, observer.transition.CurrentName, "",
		)
		if err != nil || notModified || repository.ID != observer.transition.ProviderRepositoryID {
			return operations.Observation{}, errors.New("repository delete provider precondition is no longer proven")
		}
		digest, err := repositoryworkflow.ProviderDigest(repository)
		if err != nil {
			return operations.Observation{}, err
		}
		// The retirement evidence is re-observed here, on every apply, and its
		// claim is folded into the same precondition as the provider state. Any
		// branch, pull request, issue, review conversation, worktree or local ref
		// that appeared or changed since planning yields a different digest, and
		// the engine refuses before the handler is called. That is the difference
		// between evidence and a checkbox: it has to still be true at the moment
		// of the mutation, not at the moment somebody approved it.
		evidence := observer.services.gatherRetirementEvidence(
			ctx, observer.root, observer.transition, repository.DefaultBranch,
			observer.retirement, repositoryworkflow.PreservationDeclaration{
				Identities: observer.preserve,
			},
		)
		if !evidence.Retirable {
			return operations.Observation{}, errors.New(
				"repository retirement evidence is no longer proven",
			)
		}
		combined, err := retirementDigest(digest, evidence)
		if err != nil {
			return operations.Observation{}, err
		}
		current.observation.RemoteEvidenceDigest = combined
	}
	return current.observation, nil
}

func (services *Services) PlanRepositoryDelete(
	ctx context.Context,
	path string,
	options RepositoryDeleteOptions,
) domain.Envelope {
	const command = "gds repository delete plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, findings := services.repositoryDeleteContext(ctx, path, options)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	if options.ConfirmRepositoryID != current.transition.RepositoryID ||
		options.ConfirmProviderID != strconv.FormatInt(current.transition.ProviderRepositoryID, 10) {
		return domain.NewEnvelope(command, domain.ExitApproval, nil, repositoryTransitionFinding(
			"GDS_REPOSITORY_DELETE_CONFIRMATION_REQUIRED",
			"Deletion planning requires the exact GDS repository ID and provider repository ID.",
		))
	}
	_, currentAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	providerRuntime, runtimeEnvelope := services.repositoryProviderRuntime(
		ctx, path, currentAnchor, current.transition, options.GitHubReadOptions, command,
	)
	if runtimeEnvelope != nil {
		return *runtimeEnvelope
	}
	current.transition.MutationCapabilityID = providerRuntime.capability.Mutation.ID
	providerDigest, err := repositoryworkflow.ProviderDigest(providerRuntime.repository)
	if err != nil {
		return domain.InternalError(command, err)
	}
	current.transition.ExpectedProviderDigest = providerDigest
	// The question the plan has to answer is not "is this checkout clean" but
	// "does any work exist here that exists nowhere else". Everything the device
	// and the provider hold is enumerated and classified now, and the claim is
	// bound into the precondition alongside the provider state.
	evidence := services.gatherRetirementEvidence(
		ctx, current.root, current.transition, providerRuntime.repository.DefaultBranch,
		retirementReaderFor(providerRuntime.readers[current.transition.CurrentInstallation]),
		repositoryworkflow.PreservationDeclaration{Identities: options.PreserveIdentities},
	)
	if !evidence.Retirable {
		envelope := domain.NewEnvelope(command, domain.ExitPolicy, RepositoryDeletePlanData{
			Transition: current.transition, Index: current.index,
			ReadyForApply: false, ApplyBlocker: "retirement evidence does not prove this repository is finished",
			Retirement: evidence,
		}, retirementFindings(evidence)...)
		envelope.Scope["repository_id"] = current.transition.RepositoryID
		return envelope
	}
	combinedDigest, err := retirementDigest(providerDigest, evidence)
	if err != nil {
		return domain.InternalError(command, err)
	}
	current.observation.RemoteEvidenceDigest = combinedDigest
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
		Operation: "delete-repository",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			WorktreeFingerprint:  current.observation.WorktreeFingerprint,
			ManifestDigest:       current.observation.ManifestDigest,
			PolicyDigest:         current.observation.PolicyDigest,
			RemoteEvidenceDigest: current.observation.RemoteEvidenceDigest,
		}},
		Steps: []operations.Step{{
			StepID: "delete-provider-repository", RepositoryID: current.observation.RepositoryID,
			Action: repositoryworkflow.ProviderLifecycleAction, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "manual"},
			Parameters:   repositoryworkflow.Parameters(current.transition),
		}},
		ApprovalClass: "delete-github-repository",
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, repositoryDeleteObserver{
			services: services, root: current.root, inventoryRoot: current.transition.AnalysisRoot,
			maxDepth: options.MaxDepth, maxRepositories: options.MaxRepositories,
			concurrency: options.Concurrency,
			reader:      providerRuntime.readers[current.transition.CurrentInstallation],
			retirement:  retirementReaderFor(providerRuntime.readers[current.transition.CurrentInstallation]),
			preserve:    options.PreserveIdentities,
			transition:  current.transition,
		}, nil, options.DeviceID, options.SessionID)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	envelope := domain.Success(command, RepositoryDeletePlanData{
		Plan: plan, StatePath: statePath, Transition: current.transition, Index: current.index,
		ReadyForApply: providerRuntime.ready, ApplyBlocker: providerRuntime.blocker,
		Retirement: evidence,
	})
	envelope.Scope["repository_id"] = current.transition.RepositoryID
	return envelope
}

func (services *Services) ApplyRepositoryDelete(
	ctx context.Context,
	path string,
	planID string,
	options RepositoryDeleteOptions,
) domain.Envelope {
	const command = "gds repository delete apply"
	if strings.TrimSpace(planID) == "" {
		return syncIdentifierRequired(command, "plan", "--apply")
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	return services.applyRepositoryDelete(ctx, path, planID, options)
}

// applyRepositoryDelete performs the mutation the plan authorized.
//
// It is reachable because the retirement evidence exists. The observer rebuilds
// that evidence before the handler is called, so a branch, pull request, issue,
// review conversation, worktree or local ref that appeared since planning makes
// the precondition mismatch and the operation stops with nothing attempted.
func (services *Services) applyRepositoryDelete(
	ctx context.Context,
	path string,
	planID string,
	options RepositoryDeleteOptions,
) domain.Envelope {
	const command = "gds repository delete apply"
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	plan, transition, err := loadRepositoryDeletePlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return repositoryTransitionPlanInvalid(command)
	}
	effective := options
	effective.InventoryRoot = transition.AnalysisRoot
	current, findings := services.repositoryDeleteContext(ctx, path, effective)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	_, currentAnchor, findings := services.policyInputs(ctx, path)
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
		}, repositoryTransitionFinding("GDS_REPOSITORY_DELETE_MUTATION_BLOCKED", providerRuntime.blocker))
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
		return githubMutationRuntimeError(command, errors.New("repository delete capability is unavailable"))
	}
	writer, err := factory.BindRepository(githubprovider.RepositoryMutationScope{
		RepositoryID: transition.ProviderRepositoryID,
		Owner:        transition.CurrentOwner, Name: transition.CurrentName,
		Operations: []string{githubprovider.MutationRepositoryDelete},
	})
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	handler := &repositoryworkflow.ProviderHandler{Readers: providerRuntime.readers, Writer: writer}
	observer := repositoryDeleteObserver{
		services: services, root: current.root, inventoryRoot: transition.AnalysisRoot,
		maxDepth: options.MaxDepth, maxRepositories: options.MaxRepositories,
		concurrency: options.Concurrency,
		reader:      providerRuntime.readers[transition.CurrentInstallation], transition: transition,
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, observer,
		map[string]operations.ActionHandler{repositoryworkflow.ProviderLifecycleAction: handler},
		options.DeviceID, options.SessionID,
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

func (services *Services) VerifyRepositoryDelete(
	ctx context.Context,
	path string,
	operationID string,
	options RepositoryDeleteOptions,
) domain.Envelope {
	const command = "gds repository delete verify"
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
	plan, transition, err := loadRepositoryDeletePlan(ctx, store, record.PlanID, services.Schemas)
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
	handler := &repositoryworkflow.ProviderHandler{
		Readers: readers,
		Scope: githubprovider.RepositoryMutationScope{
			RepositoryID: transition.ProviderRepositoryID,
			Owner:        transition.CurrentOwner, Name: transition.CurrentName,
			Operations: []string{githubprovider.MutationRepositoryDelete},
		},
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, repositoryDeleteObserver{},
		map[string]operations.ActionHandler{repositoryworkflow.ProviderLifecycleAction: handler},
		options.DeviceID, options.SessionID,
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
	envelopeValue.Scope["repository_id"] = transition.RepositoryID
	envelopeValue.Scope["repositories"] = plan.Scope.Repositories
	return envelopeValue
}

func (services *Services) repositoryDeleteContext(
	ctx context.Context,
	path string,
	options RepositoryDeleteOptions,
) (repositoryDeleteContext, []domain.Finding) {
	estateRoot, currentAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return repositoryDeleteContext{}, findings
	}
	transition, findings := repositoryworkflow.ValidateDelete(currentAnchor)
	if len(findings) != 0 {
		return repositoryDeleteContext{}, findings
	}
	physicalInventoryRoot, finding := physicalInventoryRoot(options.InventoryRoot)
	if finding != nil {
		return repositoryDeleteContext{}, []domain.Finding{*finding}
	}
	transition.AnalysisRoot = physicalInventoryRoot
	index, indexFindings := services.completeRelationshipIndex(ctx, DiscoveryOptions{
		Root: physicalInventoryRoot, MaxDepth: options.MaxDepth,
		MaxRepositories: options.MaxRepositories, Concurrency: options.Concurrency,
	})
	if len(indexFindings) != 0 {
		return repositoryDeleteContext{}, indexFindings
	}
	found := false
	for _, item := range index.Repositories {
		if item.ID == currentAnchor.Repository.ID {
			found = true
			break
		}
	}
	if !found {
		return repositoryDeleteContext{}, []domain.Finding{repositoryTransitionFinding(
			"GDS_REPOSITORY_DELETE_INVENTORY_INCOMPLETE",
			"The selected repository is absent from the complete relationship index.",
		)}
	}
	for _, relationship := range index.Relationships {
		if relationship.Source == currentAnchor.Repository.ID || relationship.Target == currentAnchor.Repository.ID {
			return repositoryDeleteContext{}, []domain.Finding{{
				Code: "GDS_REPOSITORY_DELETE_RELATIONSHIP_BLOCKED", Severity: domain.SeverityHigh,
				Message: "Repository deletion is blocked while a typed relationship references the repository.",
				Evidence: map[string]any{
					"source": relationship.Source, "type": relationship.Type, "target": relationship.Target,
				},
			}}
		}
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return repositoryDeleteContext{}, []domain.Finding{repositoryTransitionFinding(
			"GDS_REPOSITORY_DELETE_BOUNDARY_NOT_PROVEN", err.Error(),
		)}
	}
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil || status.Head.Mode != "branch" || status.Head.OID == "" ||
		status.Branch.Name != currentAnchor.Git.DefaultBranch || !checkoutStatusIsClean(status) {
		return repositoryDeleteContext{}, []domain.Finding{repositoryTransitionFinding(
			"GDS_REPOSITORY_DELETE_GIT_STATE_UNSAFE",
			"Deletion planning requires a clean attached default branch.",
		)}
	}
	topology, err := services.Git.InspectTopology(ctx, info.WorktreeRoot)
	if err != nil {
		return repositoryDeleteContext{}, []domain.Finding{repositoryTransitionFinding(
			"GDS_REPOSITORY_DELETE_REMOTE_NOT_PROVEN", err.Error(),
		)}
	}
	remoteFindings := gitcontracts.ValidateRemote(topology, "origin", gitcontracts.ExpectedRepository{
		Owner: currentAnchor.Provider.Owner, Name: currentAnchor.Provider.Name,
	})
	if len(remoteFindings) != 0 {
		return repositoryDeleteContext{}, remoteFindings
	}
	compiled := services.Compiler.CompileDirectory(estateRoot, currentAnchor, compiler.DevelopmentBundleVersion)
	if len(compiled.Findings) != 0 {
		return repositoryDeleteContext{}, compiled.Findings
	}
	observedAnchor, err := anchor.Observe(info.WorktreeRoot)
	if err != nil || observedAnchor.File.State != "regular" {
		return repositoryDeleteContext{}, []domain.Finding{repositoryTransitionFinding(
			"GDS_REPOSITORY_DELETE_ANCHOR_NOT_PROVEN", "Current repository anchor is unavailable.",
		)}
	}
	// A retirement question is repository-wide, so the checkout the command runs
	// in is not the whole subject. Every other worktree sharing this Git store
	// is inspected and any that exists at all blocks: it is a second working
	// tree over the same history, it may hold the only copy of unfinished work,
	// and a repository being retired should have none. Blocking is the answer
	// GDS can defend; "probably fine" is not.
	if findings := secondaryWorktreeFindings(ctx, services, info.WorktreeRoot, status); len(findings) != 0 {
		return repositoryDeleteContext{}, findings
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Head      gitprovider.HeadState   `json:"head"`
		Branch    gitprovider.BranchState `json:"branch"`
		Changes   gitprovider.ChangeState `json:"changes"`
		Worktrees []gitprovider.Worktree  `json:"worktrees"`
		Anchor    anchor.FileObservation  `json:"anchor"`
		Index     estate.IdentityIndex    `json:"index"`
	}{status.Head, status.Branch, status.Changes, status.Worktrees, observedAnchor.File, index})
	if err != nil {
		return repositoryDeleteContext{}, []domain.Finding{repositoryTransitionFinding(
			"GDS_REPOSITORY_DELETE_FINGERPRINT_FAILED", err.Error(),
		)}
	}
	return repositoryDeleteContext{
		root: info.WorktreeRoot, transition: transition, index: index,
		observation: operations.Observation{
			RepositoryID: currentAnchor.Repository.ID, HeadOID: status.Head.OID,
			WorktreeFingerprint: fingerprint, ManifestDigest: observedAnchor.File.ContentDigest,
			PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func loadRepositoryDeletePlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, repositoryworkflow.ProviderTransition, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, repositoryworkflow.ProviderTransition{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != "delete-repository" || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) != 1 || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, repositoryworkflow.ProviderTransition{},
			errors.New("stored plan is not a valid repository deletion")
	}
	transition, err := repositoryworkflow.StepTransition(plan.Steps[0])
	if err != nil || transition.Operation != repositoryworkflow.DeleteOperation ||
		transition.TargetLifecycle != "tombstoned" {
		return operations.Plan{}, repositoryworkflow.ProviderTransition{},
			errors.New("repository deletion parameters are invalid")
	}
	return plan, transition, nil
}

func physicalInventoryRoot(path string) (string, *domain.Finding) {
	if strings.TrimSpace(path) == "" {
		finding := repositoryTransitionFinding(
			"GDS_REPOSITORY_DELETE_INVENTORY_ROOT_REQUIRED",
			"--inventory-root must identify the complete local repository analysis scope.",
		)
		return "", &finding
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		absolute, err = filepath.EvalSymlinks(absolute)
	}
	info, statErr := os.Lstat(absolute)
	if err != nil || statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		finding := repositoryTransitionFinding(
			"GDS_REPOSITORY_DELETE_INVENTORY_ROOT_INVALID",
			"Deletion inventory root must be a real existing directory.",
		)
		return "", &finding
	}
	return filepath.Clean(absolute), nil
}

// secondaryWorktreeFingerprint is why `status.Worktrees` is bound into the plan
// as well as checked: a worktree created after planning changes what the
// repository holds, and a precondition that ignored it would let the operation
// proceed against a subject that had grown a second working tree.

// secondaryWorktreeFindings reports every worktree over this Git store other
// than the one the command runs in.
//
// `gitprovider.Status` has always returned `Worktrees`, the observer has always
// read them, and nothing looked. The delete preconditions asked whether *this
// checkout* is clean; an archived, relationship-free repository with a clean
// default branch could still hold the only copy of unfinished work one
// directory away.
//
// Each is reported with what could be observed about it, so the finding names a
// path an operator can act on rather than a count.
func secondaryWorktreeFindings(
	ctx context.Context,
	services *Services,
	current string,
	status gitprovider.Status,
) []domain.Finding {
	findings := []domain.Finding{}
	for _, worktree := range status.Worktrees {
		if worktree.Path == current {
			continue
		}
		evidence := map[string]any{
			"path": worktree.Path, "head": worktree.Head, "branch": worktree.Branch,
			"detached": worktree.Detached, "locked": worktree.Locked,
			"prunable": worktree.Prunable, "bare": worktree.Bare,
		}
		// Observed where it can be. A worktree whose status cannot be read is
		// reported as unreadable rather than assumed empty; either way it
		// blocks, and the difference is what the operator is told.
		if observed, err := services.Git.InspectStatus(ctx, worktree.Path); err == nil {
			evidence["clean"] = checkoutStatusIsClean(observed)
			evidence["staged"] = observed.Changes.Staged
			evidence["unstaged"] = observed.Changes.Unstaged
			evidence["untracked"] = observed.Changes.Untracked
		} else {
			evidence["status"] = "unreadable"
		}
		findings = append(findings, domain.Finding{
			Code: "GDS_REPOSITORY_DELETE_SECONDARY_WORKTREE", Severity: domain.SeverityHigh,
			Message: "A second worktree over this repository blocks retirement until it is " +
				"removed deliberately.",
			Evidence: evidence,
		})
	}
	return findings
}
