package app

import (
	"context"
	"errors"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	forkworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/fork"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitcontracts"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type ForkDetachOptions struct {
	ProjectionOperationOptions
	AnchorPath string
}

type ForkDetachPlanData struct {
	Plan        operations.Plan  `json:"plan"`
	StatePath   string           `json:"state_path"`
	Target      string           `json:"target"`
	ExpectedURL string           `json:"expected_url"`
	Candidate   anchor.Candidate `json:"candidate"`
}

type forkDetachContext struct {
	anchorChange anchorChangeContext
	expectedURL  string
}

type forkDetachObserver struct {
	services  *Services
	root      string
	candidate anchor.Candidate
}

func (observer forkDetachObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.forkDetachContext(ctx, observer.root, observer.candidate)
	if len(findings) != 0 || current.anchorChange.observation.RepositoryID != repositoryID {
		return operations.Observation{}, errors.New("fork detach precondition is no longer proven")
	}
	return current.anchorChange.observation, nil
}

func (services *Services) PlanForkDetach(
	ctx context.Context,
	path string,
	options ForkDetachOptions,
) domain.Envelope {
	const command = "gds fork detach plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	candidate, findings := services.loadAnchorCandidate(options.AnchorPath)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	current, findings := services.forkDetachContext(ctx, path, candidate)
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
		Operation: "detach-fork",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID:        current.anchorChange.observation.RepositoryID,
			HeadOID:             current.anchorChange.observation.HeadOID,
			WorktreeFingerprint: current.anchorChange.observation.WorktreeFingerprint,
			ManifestDigest:      current.anchorChange.observation.ManifestDigest,
			PolicyDigest:        current.anchorChange.observation.PolicyDigest,
		}},
		Steps: []operations.Step{
			{
				StepID: "remove-fork-upstream-remote", RepositoryID: current.anchorChange.observation.RepositoryID,
				Action: forkworkflow.DetachAction, RequiresApproval: true,
				Compensation: operations.Compensation{Mode: "explicit-plan", Action: "restore-fork-upstream-remote"},
				Parameters:   forkworkflow.DetachStepParameters(current.anchorChange.root, current.expectedURL),
			},
			{
				StepID: "materialize-repository-anchor", RepositoryID: current.anchorChange.observation.RepositoryID,
				Action: anchor.MaterializeAction, RequiresApproval: true,
				Compensation: operations.Compensation{Mode: "manual"},
				Parameters: anchor.Parameters(
					current.anchorChange.root, current.anchorChange.file, current.anchorChange.candidate,
				),
			},
		},
		ApprovalClass: "detach-fork",
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		forkDetachObserver{services: services, root: current.anchorChange.root, candidate: candidate},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	envelope := domain.Success(command, ForkDetachPlanData{
		Plan: plan, StatePath: statePath, Target: current.anchorChange.root,
		ExpectedURL: current.expectedURL, Candidate: candidate,
	})
	envelope.Scope["repository_id"] = current.anchorChange.observation.RepositoryID
	return envelope
}

func (services *Services) ApplyForkDetach(
	ctx context.Context,
	planID string,
	options ForkDetachOptions,
) domain.Envelope {
	const command = "gds fork detach apply"
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
	plan, parameters, candidate, err := loadForkDetachPlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return forkDetachPlanInvalid(command)
	}
	detachHandler, err := forkworkflow.NewDetachHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError(command, err)
	}
	anchorHandler, err := anchor.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		forkDetachObserver{services: services, root: parameters.WorktreeRoot, candidate: candidate},
		map[string]operations.ActionHandler{
			forkworkflow.DetachAction: detachHandler, anchor.MaterializeAction: anchorHandler,
		}, options.DeviceID, options.SessionID,
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

func (services *Services) VerifyForkDetach(
	ctx context.Context,
	operationID string,
	options ForkDetachOptions,
) domain.Envelope {
	const command = "gds fork detach verify"
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
		return forkDetachPlanInvalid(command)
	}
	plan, _, candidate, err := loadForkDetachPlan(ctx, store, operation.PlanID, services.Schemas)
	if err != nil {
		return forkDetachPlanInvalid(command)
	}
	detachHandler, err := forkworkflow.NewDetachHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError(command, err)
	}
	anchorHandler, err := anchor.NewHandler(services.Schemas)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, forkDetachObserver{}, map[string]operations.ActionHandler{
			forkworkflow.DetachAction: detachHandler, anchor.MaterializeAction: anchorHandler,
		}, options.DeviceID, options.SessionID,
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

func (services *Services) forkDetachContext(
	ctx context.Context,
	path string,
	candidate anchor.Candidate,
) (forkDetachContext, []domain.Finding) {
	_, currentAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return forkDetachContext{}, findings
	}
	if findings := forkworkflow.ValidateDetach(currentAnchor, candidate.Anchor); len(findings) != 0 {
		return forkDetachContext{}, findings
	}
	change, findings := services.anchorChangeContext(ctx, path, candidate)
	if len(findings) != 0 {
		return forkDetachContext{}, findings
	}
	topology, err := services.Git.InspectTopology(ctx, change.root)
	if err != nil {
		return forkDetachContext{}, []domain.Finding{{
			Code: "GDS_FORK_DETACH_TOPOLOGY_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	remoteFindings := gitcontracts.ValidateRemote(topology, "origin", gitcontracts.ExpectedRepository{
		Owner: currentAnchor.Provider.Owner, Name: currentAnchor.Provider.Name,
	})
	remoteFindings = append(remoteFindings, gitcontracts.ValidateRemote(
		topology, "upstream", gitcontracts.ExpectedRepository{
			Owner: currentAnchor.Fork.Upstream.Owner, Name: currentAnchor.Fork.Upstream.Name,
		},
	)...)
	if len(remoteFindings) != 0 {
		return forkDetachContext{}, remoteFindings
	}
	upstream, found := gitcontracts.FindRemote(topology, "upstream")
	if !found || len(upstream.FetchURLs) != 1 || len(upstream.PushURLs) != 1 ||
		upstream.FetchURLs[0].CredentialsRedacted || upstream.PushURLs[0].CredentialsRedacted ||
		upstream.FetchURLs[0].Value != upstream.PushURLs[0].Value {
		return forkDetachContext{}, []domain.Finding{{
			Code: "GDS_FORK_DETACH_REMOTE_AMBIGUOUS", Severity: domain.SeverityHigh,
			Message: "Fork detach requires one identical credential-free upstream fetch and push URL.",
		}}
	}
	evidence, err := services.GitMutations.ObserveRemoteRemoval(
		ctx, change.root, "upstream", upstream.FetchURLs[0].Value,
	)
	if err != nil || evidence.State != "present" {
		return forkDetachContext{}, []domain.Finding{{
			Code: "GDS_FORK_DETACH_REMOTE_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "Upstream remote removal precondition could not be proven.",
		}}
	}
	return forkDetachContext{anchorChange: change, expectedURL: upstream.FetchURLs[0].Value}, nil
}

func loadForkDetachPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, forkworkflow.DetachParameters, anchor.Candidate, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, forkworkflow.DetachParameters{}, anchor.Candidate{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != "detach-fork" || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) != 2 || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, forkworkflow.DetachParameters{}, anchor.Candidate{},
			errors.New("stored plan is not a valid fork detach")
	}
	parameters, err := forkworkflow.StepDetachParameters(plan.Steps[0])
	if err != nil {
		return operations.Plan{}, forkworkflow.DetachParameters{}, anchor.Candidate{}, err
	}
	_, candidate, err := anchor.StepCandidate(plan.Steps[1], schemas)
	if err != nil {
		return operations.Plan{}, forkworkflow.DetachParameters{}, anchor.Candidate{}, err
	}
	return plan, parameters, candidate, nil
}

func forkDetachPlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_FORK_DETACH_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable fork detach.",
	})
}

func (services *Services) PlanForkArchive(
	ctx context.Context,
	path string,
	options RepositoryTransitionOptions,
) domain.Envelope {
	_, repositoryAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds fork archive plan", classifyFindings(findings), nil, findings...)
	}
	if repositoryAnchor.Fork == nil {
		return domain.NewEnvelope("gds fork archive plan", domain.ExitPolicy, nil, domain.Finding{
			Code: "GDS_FORK_ARCHIVE_ROLE_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Fork archive requires declared fork metadata.",
		})
	}
	envelope := services.PlanRepositoryTransition(ctx, path, "archive", options)
	envelope.Command = "gds fork archive plan"
	return envelope
}
