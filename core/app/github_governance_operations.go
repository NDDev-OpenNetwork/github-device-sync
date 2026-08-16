package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubmutationruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/governance"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const githubGovernanceOperation = "reconcile-github-governance"

type GitHubGovernanceOperationOptions struct {
	GitHubGovernanceOptions
	ProjectionOperationOptions
	MutationRuntimeConfig string
}

type GitHubGovernancePlanData struct {
	Plan          *operations.Plan                  `json:"plan,omitempty"`
	StatePath     string                            `json:"state_path,omitempty"`
	Snapshot      githubprovider.GovernanceSnapshot `json:"snapshot"`
	Comparison    governance.Result                 `json:"comparison"`
	Remediation   governance.Remediation            `json:"remediation"`
	Assignment    estate.Assignment                 `json:"assignment"`
	ReadyForApply bool                              `json:"ready_for_apply"`
	ApplyBlocker  string                            `json:"apply_blocker,omitempty"`
}

type githubGovernanceOperationContext struct {
	root        string
	estateRoot  string
	runtime     githubRuntime
	reader      *githubprovider.Client
	snapshot    githubprovider.GovernanceSnapshot
	comparison  governance.Result
	remediation governance.Remediation
	assignment  estate.Assignment
	capability  estate.MutationCapability
	observation operations.Observation
	ready       bool
	blocker     string
}

type githubGovernanceObserver struct {
	services     *Services
	root         string
	estateRoot   string
	repositoryID string
	providerID   int64
	owner        string
	name         string
	installation string
	reader       governance.GovernanceReader
}

func (observer githubGovernanceObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	if repositoryID != observer.repositoryID {
		return operations.Observation{}, errors.New("GitHub governance repository identity changed")
	}
	anchor, findings := manifest.NewLoader(observer.services.Schemas).LoadRepository(observer.root)
	if len(findings) != 0 || observer.estateRoot == "" || anchor.Repository.ID != observer.repositoryID ||
		anchor.Provider.RepositoryID != observer.providerID ||
		!strings.EqualFold(anchor.Provider.Owner, observer.owner) ||
		!strings.EqualFold(anchor.Provider.Name, observer.name) {
		return operations.Observation{}, errors.New("local GitHub governance identity is no longer proven")
	}
	compiled := observer.services.Compiler.CompileDirectory(
		observer.estateRoot, anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return operations.Observation{}, errors.New("current GitHub governance policy does not compile")
	}
	headOID, err := observer.services.Git.HeadOID(ctx, observer.root)
	if err != nil {
		return operations.Observation{}, err
	}
	manifestDigest, err := fileDigest(filepath.Join(observer.root, ".gds", "repository.yaml"))
	if err != nil {
		return operations.Observation{}, err
	}
	snapshot, err := observer.reader.GetRepositoryGovernance(ctx, observer.owner, observer.name)
	if err != nil {
		return operations.Observation{}, err
	}
	if snapshot.InstallationID != observer.installation || snapshot.Repository.ID != observer.providerID {
		return operations.Observation{}, errors.New("GitHub governance provider identity changed")
	}
	digest, err := governance.EvidenceDigest(snapshot)
	if err != nil {
		return operations.Observation{}, err
	}
	return operations.Observation{
		RepositoryID: observer.repositoryID, HeadOID: headOID,
		RemoteEvidenceDigest: digest, ManifestDigest: manifestDigest,
		PolicyDigest: compiled.Document.CompiledPolicy.Digest,
	}, nil
}

func (services *Services) PlanGitHubGovernance(
	ctx context.Context,
	path string,
	options GitHubGovernanceOperationOptions,
) domain.Envelope {
	const command = "gds github governance plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, envelope := services.githubGovernanceContext(ctx, path, options, command)
	if envelope != nil {
		return *envelope
	}
	data := GitHubGovernancePlanData{
		Snapshot: current.snapshot, Comparison: current.comparison,
		Remediation: current.remediation, Assignment: current.assignment,
		ReadyForApply: current.ready, ApplyBlocker: current.blocker,
	}
	if len(current.remediation.Steps) == 0 {
		result := domain.Success(command, data)
		result.Scope["repository_id"] = current.observation.RepositoryID
		return result
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
		Operation: githubGovernanceOperation,
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			RemoteEvidenceDigest: current.observation.RemoteEvidenceDigest,
			ManifestDigest:       current.observation.ManifestDigest,
			PolicyDigest:         current.observation.PolicyDigest,
		}},
		Steps: current.remediation.Steps, ApprovalClass: "github-governance-write",
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, current.observer(services), nil,
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	data.Plan = &plan
	data.StatePath = statePath
	result := domain.Success(command, data)
	result.Scope["repository_id"] = current.observation.RepositoryID
	return result
}

func (services *Services) ApplyGitHubGovernance(
	ctx context.Context,
	path string,
	planID string,
	options GitHubGovernanceOperationOptions,
) domain.Envelope {
	const command = "gds github governance apply"
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
	plan, parameters, err := loadGitHubGovernancePlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return githubGovernancePlanInvalid(command)
	}
	current, envelope := services.githubGovernanceContext(ctx, path, options, command)
	if envelope != nil {
		return *envelope
	}
	if err := matchGovernancePlan(parameters, current); err != nil {
		return domain.NewEnvelope(command, domain.ExitStale, nil, domain.Finding{
			Code: "GDS_GITHUB_GOVERNANCE_PLAN_SCOPE_CHANGED", Severity: domain.SeverityHigh,
			Message: "The immutable governance plan no longer matches the resolved repository scope.",
		})
	}
	if !current.ready {
		return domain.NewEnvelope(command, domain.ExitPolicy, map[string]any{
			"plan_id": plan.PlanID, "ready_for_apply": false,
		}, domain.Finding{
			Code: "GDS_GITHUB_GOVERNANCE_MUTATION_BLOCKED", Severity: domain.SeverityHigh,
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
	factory, found := mutators[parameters.MutationCapabilityID]
	if !found {
		return githubMutationRuntimeError(command, errors.New("mutation capability is unavailable"))
	}
	writer, err := factory.BindRepository(githubprovider.RepositoryMutationScope{
		RepositoryID: parameters.ProviderRepositoryID, Owner: parameters.Owner, Name: parameters.Name,
		Operations: []string{githubprovider.MutationRepositorySettings},
	})
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	handlers := governanceHandlers(plan, current.reader, writer, writer.Scope())
	engine := operations.NewDefaultEngine(
		store, services.Schemas, current.observer(services), handlers,
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
	envelopeValue := domain.Success(command, result)
	envelopeValue.Mutation.Attempted = result.MutationAttempted
	envelopeValue.Mutation.Completed = result.MutationCompleted
	envelopeValue.OperationID = result.OperationID
	envelopeValue.Scope["repository_id"] = current.observation.RepositoryID
	return envelopeValue
}

func (services *Services) VerifyGitHubGovernance(
	ctx context.Context,
	path string,
	operationID string,
	options GitHubGovernanceOperationOptions,
) domain.Envelope {
	const command = "gds github governance verify"
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
	operationRecord, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return operationFailureEnvelope(command, err)
	}
	plan, parameters, err := loadGitHubGovernancePlan(
		ctx, store, operationRecord.PlanID, services.Schemas,
	)
	if err != nil {
		return githubGovernancePlanInvalid(command)
	}
	current, envelope := services.githubGovernanceContext(ctx, path, options, command)
	if envelope != nil {
		return *envelope
	}
	if err := matchGovernancePlan(parameters, current); err != nil {
		return githubGovernancePlanInvalid(command)
	}
	scope := githubprovider.RepositoryMutationScope{
		RepositoryID: parameters.ProviderRepositoryID, Owner: parameters.Owner,
		Name: parameters.Name, Operations: []string{githubprovider.MutationRepositorySettings},
	}
	handlers := governanceHandlers(plan, current.reader, nil, scope)
	engine := operations.NewDefaultEngine(
		store, services.Schemas, current.observer(services), handlers,
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
	envelopeValue.Scope["repository_id"] = current.observation.RepositoryID
	return envelopeValue
}

func (services *Services) githubGovernanceContext(
	ctx context.Context,
	path string,
	options GitHubGovernanceOperationOptions,
	command string,
) (githubGovernanceOperationContext, *domain.Envelope) {
	if strings.TrimSpace(options.InstallationID) == "" || strings.TrimSpace(options.Owner) == "" ||
		strings.TrimSpace(options.Repository) == "" {
		envelope := domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_GITHUB_GOVERNANCE_SCOPE_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--installation, --owner, and --repository must identify one exact repository.",
		})
		return githubGovernanceOperationContext{}, &envelope
	}
	runtime, envelope := services.loadGitHubRuntime(ctx, path, options.GitHubReadOptions, command)
	if envelope != nil {
		return githubGovernanceOperationContext{}, envelope
	}
	// Canonicalize once at the operation-context boundary so the short and
	// prefixed installation forms behave identically in plan, apply, and
	// verify, exactly as they already do on the read-only path. Everything
	// downstream — scope comparison and evidence — uses the canonical id.
	options.InstallationID = normalizeInstallationID(options.InstallationID, runtime.readers)
	reader, found := runtime.readers[options.InstallationID]
	if !found {
		envelope := domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_GITHUB_INSTALLATION_UNKNOWN", Severity: domain.SeverityHigh,
			Message: "The requested installation is not declared by the current estate.",
		})
		return githubGovernanceOperationContext{}, &envelope
	}
	estateRoot, anchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return githubGovernanceOperationContext{}, &envelope
	}
	if anchor.Provider.Installation != options.InstallationID ||
		!strings.EqualFold(anchor.Provider.Owner, options.Owner) ||
		!strings.EqualFold(anchor.Provider.Name, options.Repository) {
		envelope := domain.NewEnvelope(command, domain.ExitSecurity, nil, domain.Finding{
			Code: "GDS_GITHUB_GOVERNANCE_LOCAL_IDENTITY_MISMATCH", Severity: domain.SeverityCritical,
			Message: "Local repository identity does not match the requested GitHub scope.",
		})
		return githubGovernanceOperationContext{}, &envelope
	}
	repositoryInfo, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		envelope := envelopeForError(command, path, err)
		return githubGovernanceOperationContext{}, &envelope
	}
	localRoot := repositoryInfo.WorktreeRoot
	compiled := services.Compiler.CompileDirectory(estateRoot, anchor, compiler.DevelopmentBundleVersion)
	if len(compiled.Findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(compiled.Findings), nil, compiled.Findings...)
		return githubGovernanceOperationContext{}, &envelope
	}
	snapshot, err := reader.GetRepositoryGovernance(ctx, options.Owner, options.Repository)
	if err != nil {
		envelope := githubReadError(command, options.InstallationID, err)
		return githubGovernanceOperationContext{}, &envelope
	}
	if snapshot.Repository.ID != anchor.Provider.RepositoryID ||
		snapshot.InstallationID != options.InstallationID {
		envelope := domain.NewEnvelope(command, domain.ExitSecurity, nil, domain.Finding{
			Code: "GDS_GITHUB_GOVERNANCE_IDENTITY_MISMATCH", Severity: domain.SeverityCritical,
			Message: "Observed GitHub repository identity differs from the canonical local anchor.",
		})
		return githubGovernanceOperationContext{}, &envelope
	}
	comparison := governance.Compare(compiled.Document, snapshot)
	compiledInventory, inventoryFindings := estate.Compile(runtime.desired, []estate.ObservedRepository{{
		ProviderID: snapshot.Repository.ID, Owner: snapshot.Repository.Owner,
		Name: snapshot.Repository.Name, Fork: snapshot.Repository.Fork,
		Archived: snapshot.Repository.Archived, Visibility: snapshot.Repository.Visibility,
		DefaultBranch: snapshot.Repository.DefaultBranch,
	}})
	if len(inventoryFindings) != 0 || len(compiledInventory.Repositories) != 1 {
		envelope := domain.NewEnvelope(command, classifyFindings(inventoryFindings), nil, inventoryFindings...)
		return githubGovernanceOperationContext{}, &envelope
	}
	assignment := compiledInventory.Repositories[0]
	capability, found := mutationCapabilityForInstallation(runtime.desired, options.InstallationID)
	if !found {
		envelope := domain.NewEnvelope(command, domain.ExitPolicy, nil, domain.Finding{
			Code: "GDS_GITHUB_MUTATION_CAPABILITY_MISSING", Severity: domain.SeverityHigh,
			Message: "No canonical mutation capability is bound to the read installation.",
		})
		return githubGovernanceOperationContext{}, &envelope
	}
	remediation, err := governance.BuildRemediation(governance.RemediationScope{
		RepositoryID: anchor.Repository.ID, ReadInstallationID: options.InstallationID,
		MutationCapabilityID: capability.Mutation.ID,
		ProviderRepositoryID: anchor.Provider.RepositoryID,
		Owner:                anchor.Provider.Owner, Name: anchor.Provider.Name,
	}, snapshot, comparison)
	if err != nil {
		envelope := domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_GITHUB_GOVERNANCE_REMEDIATION_INVALID", Severity: domain.SeverityHigh,
			Message: err.Error(),
		})
		return githubGovernanceOperationContext{}, &envelope
	}
	headOID, err := services.Git.HeadOID(ctx, localRoot)
	if err != nil {
		envelope := envelopeForError(command, localRoot, err)
		return githubGovernanceOperationContext{}, &envelope
	}
	manifestDigest, err := fileDigest(filepath.Join(localRoot, ".gds", "repository.yaml"))
	if err != nil {
		envelope := envelopeForError(command, localRoot, err)
		return githubGovernanceOperationContext{}, &envelope
	}
	ready, blocker := githubGovernanceMutationGate(runtime.desired, assignment, capability, anchor.Repository.Lifecycle)
	return githubGovernanceOperationContext{
		root: localRoot, estateRoot: estateRoot, runtime: runtime, reader: reader, snapshot: snapshot,
		comparison: comparison, remediation: remediation, assignment: assignment,
		capability: capability, ready: ready, blocker: blocker,
		observation: operations.Observation{
			RepositoryID: anchor.Repository.ID, HeadOID: headOID,
			RemoteEvidenceDigest: remediation.InitialEvidenceDigest,
			ManifestDigest:       manifestDigest, PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func (current githubGovernanceOperationContext) observer(services *Services) githubGovernanceObserver {
	return githubGovernanceObserver{
		services: services, root: current.root, estateRoot: current.estateRoot,
		repositoryID: current.observation.RepositoryID,
		providerID:   current.snapshot.Repository.ID, owner: current.snapshot.Repository.Owner,
		name: current.snapshot.Repository.Name, installation: current.snapshot.InstallationID,
		reader: current.reader,
	}
}

func githubGovernanceMutationGate(
	desired estate.Config,
	assignment estate.Assignment,
	capability estate.MutationCapability,
	lifecycle string,
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
	if !governanceContains(capability.Operations, githubprovider.MutationRepositorySettings) {
		return false, "Mutation capability does not permit repository-settings operations."
	}
	return true, ""
}

func governanceContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func mutationCapabilityForInstallation(config estate.Config, installation string) (estate.MutationCapability, bool) {
	for _, capability := range config.Mutations {
		if capability.Mutation.Installation == installation {
			return capability, true
		}
	}
	return estate.MutationCapability{}, false
}

func governanceHandlers(
	plan operations.Plan,
	reader governance.GovernanceReader,
	writer governance.GovernanceWriter,
	scope githubprovider.RepositoryMutationScope,
) map[string]operations.ActionHandler {
	handlers := map[string]operations.ActionHandler{}
	for _, step := range plan.Steps {
		handlers[step.Action] = &governance.Handler{
			Reader: reader, Writer: writer, Scope: scope, Action: step.Action,
		}
	}
	return handlers
}

func loadGitHubGovernancePlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, governance.OperationParameters, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, governance.OperationParameters{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != githubGovernanceOperation || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) == 0 || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, governance.OperationParameters{}, errors.New("stored plan is not a valid governance operation")
	}
	var first governance.OperationParameters
	for index, step := range plan.Steps {
		parameters, parseErr := governance.StepParameters(step)
		if parseErr != nil {
			return operations.Plan{}, governance.OperationParameters{}, parseErr
		}
		if index == 0 {
			first = parameters
			continue
		}
		if parameters.ReadInstallationID != first.ReadInstallationID ||
			parameters.MutationCapabilityID != first.MutationCapabilityID ||
			parameters.ProviderRepositoryID != first.ProviderRepositoryID ||
			!strings.EqualFold(parameters.Owner, first.Owner) ||
			!strings.EqualFold(parameters.Name, first.Name) {
			return operations.Plan{}, governance.OperationParameters{}, errors.New("governance plan steps have different provider scopes")
		}
	}
	return plan, first, nil
}

func matchGovernancePlan(parameters governance.OperationParameters, current githubGovernanceOperationContext) error {
	if parameters.ReadInstallationID != current.snapshot.InstallationID ||
		parameters.MutationCapabilityID != current.capability.Mutation.ID ||
		parameters.ProviderRepositoryID != current.snapshot.Repository.ID ||
		!strings.EqualFold(parameters.Owner, current.snapshot.Repository.Owner) ||
		!strings.EqualFold(parameters.Name, current.snapshot.Repository.Name) {
		return errors.New("governance plan scope changed")
	}
	return nil
}

func githubGovernancePlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_GITHUB_GOVERNANCE_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable GitHub governance operation.",
	})
}

func githubMutationRuntimeError(command string, err error) domain.Envelope {
	class := domain.ExitNotProven
	code := "GDS_GITHUB_MUTATION_RUNTIME_NOT_PROVEN"
	message := "Separate GitHub mutation runtime configuration is unavailable."
	var runtimeError *githubmutationruntime.Error
	if errors.As(err, &runtimeError) {
		switch runtimeError.Kind {
		case githubmutationruntime.ErrorSecurity:
			class, code, message = domain.ExitSecurity, "GDS_GITHUB_MUTATION_RUNTIME_SECURITY_VIOLATION", "GitHub mutation runtime violates the private-file security contract."
		case githubmutationruntime.ErrorInvalid:
			class, code, message = domain.ExitInput, "GDS_GITHUB_MUTATION_RUNTIME_INVALID", "GitHub mutation runtime does not satisfy its schema contract."
		case githubmutationruntime.ErrorEstateMismatch:
			class, code, message = domain.ExitPolicy, "GDS_GITHUB_MUTATION_RUNTIME_ESTATE_MISMATCH", "GitHub mutation runtime does not exactly match canonical estate intent."
		}
	}
	return domain.NewEnvelope(command, class, nil, domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: map[string]any{"error_type": fmt.Sprintf("%T", err)},
	})
}
