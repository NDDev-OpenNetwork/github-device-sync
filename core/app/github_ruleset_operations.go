package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubmutationruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruleset"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// The default-branch ruleset is governed from a tracked file so the desired
// state is reviewable in Git rather than typed at a terminal.
const trackedRulesetRelativePath = ".github/rulesets/branch-main.json"

// Required contexts that live on the branch ruleset but are produced outside
// GDS, declared so a reconcile carries them through instead of deleting them.
//
// GDS owns the `required_status_checks` rule wholesale, so the desired state it
// sends replaces the live list entirely. Without this the first reconcile after
// any generated context changes would silently drop every platform-emitted
// context -- branch protection would quietly lose gates nobody edited, and the
// only evidence would be their absence.
const externalRequiredChecksRelativePath = "requirements/external-required-checks.json"

const githubRulesetOperation = "reconcile-github-ruleset"

type GitHubRulesetPlanData struct {
	Plan      *operations.Plan                      `json:"plan,omitempty"`
	StatePath string                                `json:"state_path,omitempty"`
	Observed  githubprovider.RepositoryRulesetState `json:"observed"`
	Desired   githubprovider.RepositoryRuleset      `json:"desired"`
	InSync    bool                                  `json:"in_sync"`
}

// PlanGitHubRuleset stores one exact, side-effect-free plan that reconciles the
// live default-branch ruleset with the tracked contract.
//
// The whole point of routing this through the control plane rather than a raw
// API call is that the update is compare-and-swap bound: the observed state is
// recorded in the plan, and apply refuses if the live ruleset moved in between.
// A direct `gh api` PUT cannot offer that, and it also cannot preserve fields
// GDS does not own -- the observation carries them through byte-for-byte.
func (services *Services) PlanGitHubRuleset(
	ctx context.Context,
	path string,
	options GitHubGovernanceOperationOptions,
) domain.Envelope {
	const command = "gds github ruleset plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, envelope := services.githubRulesetContext(ctx, path, options, command)
	if envelope != nil {
		return *envelope
	}
	data := GitHubRulesetPlanData{
		Observed: current.visible, Desired: current.desired, InSync: current.inSync,
	}
	if current.inSync {
		result := domain.Success(command, data)
		result.Scope["repository_id"] = current.governance.observation.RepositoryID
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
	// Apply compares the stored expectation against the visible form of what it
	// re-observes, so the plan must record exactly that form.
	expected := rulesetPlanExpected(current)
	plan, err := operations.NewPlan(planID, now, now.Add(projectionPlanLifetime), operations.PlanInput{
		Operation: githubRulesetOperation,
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID:   current.governance.observation.RepositoryID,
			HeadOID:        current.governance.observation.HeadOID,
			ManifestDigest: current.governance.observation.ManifestDigest,
			PolicyDigest:   current.governance.observation.PolicyDigest,
			// The engine re-observes through the same governance observer and
			// compares every precondition field by exact equality, so a field the
			// observer populates and the plan leaves empty can never match. Omitting
			// this one made every apply block as stale before reaching a handler --
			// a plan that always succeeded and an apply that never could.
			RemoteEvidenceDigest: current.governance.observation.RemoteEvidenceDigest,
		}},
		Steps: []operations.Step{{
			StepID:       "reconcile-default-branch-ruleset",
			RepositoryID: current.governance.observation.RepositoryID,
			Action:       githubruleset.Action, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "manual"},
			Parameters: githubruleset.OperationParameters(githubruleset.Parameters{
				Scope: githubruleset.Scope{
					ReadInstallationID:   options.InstallationID,
					MutationCapabilityID: current.mutationCapabilityID,
					ProviderRepositoryID: current.providerRepositoryID,
					Owner:                options.Owner, Name: options.Repository,
				},
				Expected: expected,
				Desired:  current.desired,
			}),
		}},
		ApprovalClass: "github-ruleset-write",
	})
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, current.governance.observer(services), nil,
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	data.Plan, data.StatePath = &plan, statePath
	result := domain.Success(command, data)
	result.Scope["repository_id"] = current.governance.observation.RepositoryID
	return result
}

func rulesetPlanExpected(current githubRulesetOperationContext) *githubprovider.RepositoryRulesetState {
	if !current.exists {
		return nil
	}
	observed := githubruleset.VisibleState(current.observed)
	return &observed
}

// ApplyGitHubRuleset applies one approved ruleset plan through the bound
// mutation capability.
func (services *Services) ApplyGitHubRuleset(
	ctx context.Context,
	path string,
	planID string,
	options GitHubGovernanceOperationOptions,
) domain.Envelope {
	const command = "gds github ruleset apply"
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
	plan, parameters, err := loadGitHubRulesetPlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return githubRulesetPlanInvalid(command)
	}
	current, envelope := services.githubRulesetContext(ctx, path, options, command)
	if envelope != nil {
		return *envelope
	}
	if parameters.Scope.ProviderRepositoryID != current.providerRepositoryID ||
		!strings.EqualFold(parameters.Scope.Owner, options.Owner) ||
		!strings.EqualFold(parameters.Scope.Name, options.Repository) {
		return domain.NewEnvelope(command, domain.ExitStale, nil, domain.Finding{
			Code: "GDS_GITHUB_RULESET_PLAN_SCOPE_CHANGED", Severity: domain.SeverityHigh,
			Message: "The immutable ruleset plan no longer matches the resolved repository scope.",
		})
	}
	mutationConfig, err := githubmutationruntime.Load(
		options.MutationRuntimeConfig, current.governance.runtime.desired, services.Schemas,
	)
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	mutators, err := githubmutationruntime.BuildMutators(
		mutationConfig, current.governance.runtime.config, current.governance.runtime.desired,
		services.GitHubMutationRuntimeBuildOptions,
	)
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	factory, found := mutators[parameters.Scope.MutationCapabilityID]
	if !found {
		return githubMutationRuntimeError(command, errors.New("mutation capability is unavailable"))
	}
	writer, err := factory.BindRepository(githubprovider.RepositoryMutationScope{
		RepositoryID: parameters.Scope.ProviderRepositoryID,
		Owner:        parameters.Scope.Owner, Name: parameters.Scope.Name,
		Operations: []string{githubprovider.MutationRepositoryRuleset},
	})
	if err != nil {
		return githubMutationRuntimeError(command, err)
	}
	handler := &githubruleset.Handler{Reader: writer, Writer: writer, Scope: writer.Scope()}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, current.governance.observer(services),
		map[string]operations.ActionHandler{githubruleset.Action: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, plan.PlanID, options.ApprovalReference)
	if err != nil {
		failure := operationFailureEnvelope(command, err)
		failure.Data = result
		failure.OperationID = result.OperationID
		failure.Mutation.Attempted = result.MutationAttempted
		failure.Mutation.Completed = result.MutationCompleted
		return failure
	}
	success := domain.Success(command, result)
	success.OperationID = result.OperationID
	success.Mutation.Attempted = result.MutationAttempted
	success.Mutation.Completed = result.MutationCompleted
	success.Scope["repository_id"] = current.governance.observation.RepositoryID
	return success
}

// VerifyGitHubRuleset re-reads the live ruleset and compares it with the
// recorded evidence, so a later external edit is reported rather than assumed
// away.
func (services *Services) VerifyGitHubRuleset(
	ctx context.Context,
	path string,
	operationID string,
	options GitHubGovernanceOperationOptions,
) domain.Envelope {
	const command = "gds github ruleset verify"
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
	current, envelope := services.githubRulesetContext(ctx, path, options, command)
	if envelope != nil {
		return *envelope
	}
	handler := &githubruleset.Handler{
		Reader: current.privileged, Scope: current.privileged.Scope(),
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, current.governance.observer(services),
		map[string]operations.ActionHandler{githubruleset.Action: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		failure := operationFailureEnvelope(command, err)
		failure.OperationID = operationID
		return failure
	}
	success := domain.Success(command, result)
	success.OperationID = operationID
	success.Scope["repository_id"] = current.governance.observation.RepositoryID
	return success
}

type githubRulesetOperationContext struct {
	governance           githubGovernanceOperationContext
	privileged           *githubprovider.RepositoryMutator
	observed             githubprovider.RepositoryRulesetState
	visible              githubprovider.RepositoryRulesetState
	desired              githubprovider.RepositoryRuleset
	exists               bool
	inSync               bool
	providerRepositoryID int64
	mutationCapabilityID string
}

// githubRulesetContext resolves the repository scope, the tracked desired
// contract, and the current live state.
//
// Observation needs the privileged reader rather than the ordinary read client:
// bypass actors are only visible to a caller that can administer the ruleset,
// and an update that cannot see them would silently drop them. That is why the
// mutation capability is bound even for a read-only plan.
func (services *Services) githubRulesetContext(
	ctx context.Context,
	path string,
	options GitHubGovernanceOperationOptions,
	command string,
) (githubRulesetOperationContext, *domain.Envelope) {
	governanceContext, envelope := services.githubGovernanceContext(ctx, path, options, command)
	if envelope != nil {
		return githubRulesetOperationContext{}, envelope
	}
	desired, err := loadTrackedRuleset(governanceContext.root)
	if err != nil {
		failure := domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_GITHUB_RULESET_CONTRACT_INVALID", Severity: domain.SeverityHigh,
			Message: err.Error(),
		})
		return githubRulesetOperationContext{}, &failure
	}
	capability, found := mutationCapabilityForInstallation(
		governanceContext.runtime.desired, options.InstallationID,
	)
	if !found {
		failure := domain.NewEnvelope(command, domain.ExitPolicy, nil, domain.Finding{
			Code: "GDS_GITHUB_RULESET_MUTATION_CAPABILITY_MISSING", Severity: domain.SeverityHigh,
			Message: "No canonical mutation capability is bound to the repository installation.",
		})
		return githubRulesetOperationContext{}, &failure
	}
	mutationConfig, err := githubmutationruntime.Load(
		options.MutationRuntimeConfig, governanceContext.runtime.desired, services.Schemas,
	)
	if err != nil {
		failure := githubMutationRuntimeError(command, err)
		return githubRulesetOperationContext{}, &failure
	}
	mutators, err := githubmutationruntime.BuildMutators(
		mutationConfig, governanceContext.runtime.config, governanceContext.runtime.desired,
		services.GitHubMutationRuntimeBuildOptions,
	)
	if err != nil {
		failure := githubMutationRuntimeError(command, err)
		return githubRulesetOperationContext{}, &failure
	}
	factory, found := mutators[capability.Mutation.ID]
	if !found {
		failure := githubMutationRuntimeError(command, errors.New("mutation capability is unavailable"))
		return githubRulesetOperationContext{}, &failure
	}
	providerRepositoryID := governanceContext.snapshot.Repository.ID
	privileged, err := factory.BindRepository(githubprovider.RepositoryMutationScope{
		RepositoryID: providerRepositoryID, Owner: options.Owner, Name: options.Repository,
		Operations: []string{githubprovider.MutationRepositoryRuleset},
	})
	if err != nil {
		failure := githubMutationRuntimeError(command, err)
		return githubRulesetOperationContext{}, &failure
	}
	summaries, _, err := privileged.ListRepositoryRulesets(ctx)
	if err != nil {
		failure := githubReadError(command, options.InstallationID, err)
		return githubRulesetOperationContext{}, &failure
	}
	var rulesetID int64
	for _, summary := range summaries {
		if summary.Name == desired.Name {
			rulesetID = summary.ID
		}
	}
	if rulesetID == 0 {
		return githubRulesetOperationContext{
			governance: governanceContext, privileged: privileged,
			desired: desired, exists: false, inSync: false,
			providerRepositoryID: providerRepositoryID,
			mutationCapabilityID: capability.Mutation.ID,
		}, nil
	}
	observed, _, err := privileged.GetRepositoryRuleset(ctx, rulesetID)
	if err != nil {
		failure := githubReadError(command, options.InstallationID, err)
		return githubRulesetOperationContext{}, &failure
	}
	desired.ID = observed.ID
	visible := githubruleset.VisibleState(observed)
	return githubRulesetOperationContext{
		governance: governanceContext, privileged: privileged,
		observed: observed, visible: visible, desired: desired,
		exists:               true,
		inSync:               rulesetOwnedStateMatches(observed, desired),
		providerRepositoryID: providerRepositoryID,
		mutationCapabilityID: capability.Mutation.ID,
	}, nil
}

// rulesetOwnedStateMatches compares only what GDS governs. External fields are
// deliberately excluded: they are preserved through an update rather than
// asserted, so treating them as drift would make every plan permanently dirty.
func rulesetOwnedStateMatches(
	observed githubprovider.RepositoryRulesetState,
	desired githubprovider.RepositoryRuleset,
) bool {
	if observed.Enforcement != "active" {
		return false
	}
	byType := map[string]githubprovider.RulesetRule{}
	for _, rule := range observed.Rules {
		byType[rule.Type] = rule
	}
	for _, wanted := range desired.Rules {
		actual, present := byType[wanted.Type]
		if !present {
			return false
		}
		if wanted.Type == "pull_request" {
			if actual.RequiredApprovingReviewCount != wanted.RequiredApprovingReviewCount ||
				actual.DismissStaleReviewsOnPush != wanted.DismissStaleReviewsOnPush ||
				actual.RequireCodeOwnerReview != wanted.RequireCodeOwnerReview ||
				actual.RequiredReviewThreadResolution != wanted.RequiredReviewThreadResolution ||
				actual.RequireLastPushApproval != wanted.RequireLastPushApproval ||
				actual.RequireExtraApprovalForUnattributedChanges != wanted.RequireExtraApprovalForUnattributedChanges ||
				!sameStringSet(actual.AllowedMergeMethods, wanted.AllowedMergeMethods) {
				return false
			}
			continue
		}
		if wanted.Type != "required_status_checks" {
			continue
		}
		observedContexts := map[string]struct{}{}
		for _, check := range actual.RequiredStatusChecks {
			observedContexts[check.Context] = struct{}{}
		}
		for _, check := range wanted.RequiredStatusChecks {
			if _, ok := observedContexts[check.Context]; !ok {
				return false
			}
		}
	}
	return true
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func loadTrackedRuleset(localRoot string) (githubprovider.RepositoryRuleset, error) {
	path := filepath.Join(localRoot, filepath.FromSlash(trackedRulesetRelativePath))
	info, err := os.Lstat(path)
	if err != nil {
		return githubprovider.RepositoryRuleset{}, fmt.Errorf(
			"tracked ruleset %s is unavailable", trackedRulesetRelativePath,
		)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return githubprovider.RepositoryRuleset{}, fmt.Errorf(
			"tracked ruleset %s is not a bounded regular file", trackedRulesetRelativePath,
		)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return githubprovider.RepositoryRuleset{}, err
	}
	// The tracked contract is written in the provider wire shape, so it must be
	// decoded by the provider rather than unmarshalled directly -- a direct
	// unmarshal succeeds and yields empty rules.
	document, err := githubprovider.DecodeRulesetDocument(raw)
	if err != nil {
		return githubprovider.RepositoryRuleset{}, fmt.Errorf(
			"decode tracked ruleset %s: %w", trackedRulesetRelativePath, err,
		)
	}
	if document.Name == "" || len(document.Rules) == 0 {
		return githubprovider.RepositoryRuleset{}, fmt.Errorf(
			"tracked ruleset %s must declare a name and at least one rule",
			trackedRulesetRelativePath,
		)
	}
	if err := carryExternalRequiredChecks(localRoot, &document); err != nil {
		return githubprovider.RepositoryRuleset{}, err
	}
	return document, nil
}

// carryExternalRequiredChecks appends the declared externally owned contexts to
// the desired required-status-check rule.
//
// The tracked ruleset holds only what GDS generates, because the generator
// compares against it and would otherwise report every platform context as
// drift. But the desired state replaces the live list wholesale, so a context
// absent from it is a context deleted. Merging here keeps both true: the
// generator still owns its file, and a reconcile stops being able to remove a
// gate it does not govern.
//
// A missing declaration file means nothing external is claimed, which is a
// legitimate state and not an error.
func carryExternalRequiredChecks(localRoot string, document *githubprovider.RepositoryRuleset) error {
	path := filepath.Join(localRoot, filepath.FromSlash(externalRequiredChecksRelativePath))
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var declaration struct {
		Contexts []struct {
			Context string `json:"context"`
			Owner   string `json:"owner"`
		} `json:"contexts"`
	}
	if err := json.Unmarshal(raw, &declaration); err != nil {
		return fmt.Errorf("decode %s: %w", externalRequiredChecksRelativePath, err)
	}
	for index := range document.Rules {
		if document.Rules[index].Type != "required_status_checks" {
			continue
		}
		present := map[string]bool{}
		for _, check := range document.Rules[index].RequiredStatusChecks {
			present[check.Context] = true
		}
		for _, entry := range declaration.Contexts {
			if entry.Context == "" || entry.Owner == "" {
				return fmt.Errorf(
					"%s declares a context without both a name and an owner",
					externalRequiredChecksRelativePath,
				)
			}
			if present[entry.Context] {
				// Claiming a generated context as external would let the
				// declaration silently pin something the generator governs.
				return fmt.Errorf(
					"%s claims %q, which the tracked ruleset already generates",
					externalRequiredChecksRelativePath, entry.Context,
				)
			}
			document.Rules[index].RequiredStatusChecks = append(
				document.Rules[index].RequiredStatusChecks,
				githubprovider.RequiredStatusCheck{Context: entry.Context},
			)
			present[entry.Context] = true
		}
	}
	return nil
}

func loadGitHubRulesetPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, githubruleset.Parameters, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, githubruleset.Parameters{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != githubRulesetOperation || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) != 1 || plan.Steps[0].Action != githubruleset.Action ||
		len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, githubruleset.Parameters{}, errors.New(
			"stored plan is not a valid GitHub ruleset plan",
		)
	}
	parameters, err := githubruleset.StepParameters(plan.Steps[0])
	if err != nil {
		return operations.Plan{}, githubruleset.Parameters{}, err
	}
	return plan, parameters, nil
}

func githubRulesetPlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
		Code: "GDS_GITHUB_RULESET_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The referenced plan is not a valid stored GitHub ruleset plan.",
	})
}
