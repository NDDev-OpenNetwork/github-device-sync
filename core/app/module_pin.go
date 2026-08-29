package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitops"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	moduleworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/module"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type ModulePinOptions struct {
	ProjectionOperationOptions
	ModulePath     string
	GitmodulesName string
}

type ModulePinAssessment struct {
	ConsumerID     string `json:"consumer_id"`
	ModuleID       string `json:"module_id"`
	ConsumerRoot   string `json:"consumer_root"`
	ModuleRoot     string `json:"module_root"`
	GitmodulesName string `json:"gitmodules_name"`
	GitlinkPath    string `json:"gitlink_path"`
	ExpectedOldOID string `json:"expected_old_oid"`
	TargetOID      string `json:"target_oid"`
	TargetRef      string `json:"target_ref"`
}

type ModulePinPlanData struct {
	Plan       operations.Plan     `json:"plan"`
	StatePath  string              `json:"state_path"`
	Assessment ModulePinAssessment `json:"assessment"`
}

type modulePinContext struct {
	assessment  ModulePinAssessment
	observation operations.Observation
}

type modulePinObserver struct {
	services *Services
	consumer string
	module   string
	name     string
}

func (observer modulePinObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.modulePinContext(
		ctx, observer.consumer, observer.module, observer.name,
	)
	if len(findings) != 0 || current.assessment.ConsumerID != repositoryID {
		return operations.Observation{}, errors.New("module pin precondition is no longer proven")
	}
	return current.observation, nil
}

func (services *Services) PlanModuleUpdatePin(
	ctx context.Context,
	path string,
	options ModulePinOptions,
) domain.Envelope {
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope("gds module update-pin plan", domain.ExitInput, nil, *finding)
	}
	current, findings := services.modulePinContext(ctx, path, options.ModulePath, options.GitmodulesName)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds module update-pin plan", classifyFindings(findings), nil, findings...,
		)
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds module update-pin plan", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError("gds module update-pin plan", err)
	}
	plan, err := operations.NewPlan(planID, now, now.Add(projectionPlanLifetime), operations.PlanInput{
		Operation: "update-module-pin",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			WorktreeFingerprint: current.observation.WorktreeFingerprint,
			ManifestDigest:      current.observation.ManifestDigest, PolicyDigest: current.observation.PolicyDigest,
		}},
		Steps: []operations.Step{{
			StepID: "update-gitlink-pin", RepositoryID: current.assessment.ConsumerID,
			// A gitlink rewrite in the consumer's own working tree is local and
			// fully reversible: it writes no provider, replaces no credential and
			// publishes nothing. It used to require a signed approval, which meant
			// the private Ed25519 key had to be present to advance a pin -- so pins
			// stopped advancing and the estate silently drifted behind its modules.
			// The real gate on this change is the consumer's own pull request and
			// checks. Signed approval stays on the operations that write outside
			// this repository: provider lifecycle, rulesets, releases and anchors.
			Action: gitops.UpdateGitlinkAction, RequiresApproval: false,
			Compensation: operations.Compensation{Mode: "explicit-plan", Action: gitops.UpdateGitlinkAction},
			Parameters: map[string]any{"gitlink_pin": map[string]any{
				"consumer_root":    current.assessment.ConsumerRoot,
				"module_root":      current.assessment.ModuleRoot,
				"module_id":        current.assessment.ModuleID,
				"gitmodules_name":  current.assessment.GitmodulesName,
				"expected_old_oid": current.assessment.ExpectedOldOID,
				"target_oid":       current.assessment.TargetOID,
				"target_ref":       current.assessment.TargetRef,
			}},
		}},
		ApprovalClass: "update-module-gitlink-pin",
	})
	if err != nil {
		return domain.InternalError("gds module update-pin plan", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		modulePinObserver{services: services, consumer: current.assessment.ConsumerRoot, module: current.assessment.ModuleRoot, name: current.assessment.GitmodulesName},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope("gds module update-pin plan", err)
	}
	envelope := domain.Success("gds module update-pin plan", ModulePinPlanData{
		Plan: plan, StatePath: statePath, Assessment: current.assessment,
	})
	envelope.Scope["repository_id"] = current.assessment.ConsumerID
	return envelope
}

func (services *Services) ApplyModuleUpdatePin(
	ctx context.Context,
	planID string,
	options ModulePinOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return syncIdentifierRequired("gds module update-pin apply", "plan", "--apply")
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope("gds module update-pin apply", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds module update-pin apply", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	plan, assessment, err := loadModulePinPlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return modulePinPlanInvalid("gds module update-pin apply")
	}
	// No push-capability gate here either. Apply rewrites one gitlink in the
	// consumer index and touches the module not at all; the module's remote is
	// re-observed by the plan's preconditions, which is a read. Requiring push
	// access to the module in order to record which of its commits is consumed
	// refused every module whose origin is a real remote.
	handler, err := gitops.NewUpdateGitlinkHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError("gds module update-pin apply", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		modulePinObserver{services: services, consumer: assessment.ConsumerRoot, module: assessment.ModuleRoot, name: assessment.GitmodulesName},
		map[string]operations.ActionHandler{gitops.UpdateGitlinkAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, plan.PlanID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope("gds module update-pin apply", err)
		envelope.Data = result
		envelope.OperationID = result.OperationID
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		return envelope
	}
	envelope := domain.Success("gds module update-pin apply", result)
	envelope.OperationID = result.OperationID
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.Scope["repository_id"] = assessment.ConsumerID
	return envelope
}

func (services *Services) VerifyModuleUpdatePin(
	ctx context.Context,
	operationID string,
	options ModulePinOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return syncIdentifierRequired("gds module update-pin verify", "operation", "--verify")
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope("gds module update-pin verify", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds module update-pin verify", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	operation, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return modulePinPlanInvalid("gds module update-pin verify")
	}
	plan, assessment, err := loadModulePinPlan(ctx, store, operation.PlanID, services.Schemas)
	if err != nil {
		return modulePinPlanInvalid("gds module update-pin verify")
	}
	handler, err := gitops.NewUpdateGitlinkHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError("gds module update-pin verify", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, modulePinObserver{},
		map[string]operations.ActionHandler{gitops.UpdateGitlinkAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope("gds module update-pin verify", err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success("gds module update-pin verify", result)
	envelope.OperationID = operationID
	envelope.Scope["repository_id"] = assessment.ConsumerID
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) modulePinContext(
	ctx context.Context,
	consumerPath string,
	modulePath string,
	gitmodulesName string,
) (modulePinContext, []domain.Finding) {
	if strings.TrimSpace(modulePath) == "" || strings.TrimSpace(gitmodulesName) == "" {
		return modulePinContext{}, []domain.Finding{modulePinFinding(
			"GDS_MODULE_PIN_INPUT_REQUIRED", "--module and --name must identify the exact module boundary.",
		)}
	}
	estateRoot, consumer, findings := services.policyInputs(ctx, consumerPath)
	if len(findings) != 0 {
		return modulePinContext{}, findings
	}
	consumerInfo, err := services.Git.RepositoryInfo(ctx, consumerPath)
	if err != nil {
		return modulePinContext{}, []domain.Finding{modulePinFinding("GDS_MODULE_PIN_CONSUMER_NOT_PROVEN", err.Error())}
	}
	consumerTopology, err := services.Git.InspectTopology(ctx, consumerInfo.WorktreeRoot)
	if err != nil {
		return modulePinContext{}, []domain.Finding{modulePinFinding("GDS_MODULE_PIN_GITLINK_NOT_PROVEN", err.Error())}
	}
	var submodule *gitprovider.Submodule
	for index := range consumerTopology.Submodules {
		if consumerTopology.Submodules[index].Name == gitmodulesName {
			submodule = &consumerTopology.Submodules[index]
			break
		}
	}
	consumerStatus, err := services.Git.InspectStatus(ctx, consumerInfo.WorktreeRoot)
	if err != nil || consumerStatus.Head.Mode != "branch" || consumerStatus.Head.OID == "" ||
		consumerStatus.Branch.Name == consumer.Git.DefaultBranch ||
		!pinConsumerStatusIsClean(consumerStatus, submodule) {
		return modulePinContext{}, []domain.Finding{modulePinFinding(
			"GDS_MODULE_PIN_CONSUMER_STATE_UNSAFE", "Module pin updates require a clean attached non-default consumer task branch.",
		)}
	}
	var relationship *domain.Relationship
	for index := range consumer.Relationships {
		candidate := &consumer.Relationships[index]
		if candidate.Type == "git-submodule-consumer" && candidate.GitmodulesName == gitmodulesName {
			relationship = candidate
			break
		}
	}
	if relationship == nil {
		return modulePinContext{}, []domain.Finding{modulePinFinding(
			"GDS_MODULE_PIN_RELATIONSHIP_MISSING", "Consumer has no exact typed git-submodule relationship.",
		)}
	}
	if finding := modulePinManagementFinding(*relationship); finding != nil {
		return modulePinContext{}, []domain.Finding{*finding}
	}
	_, moduleAnchor, moduleFindings := services.policyInputs(ctx, modulePath)
	if len(moduleFindings) != 0 {
		return modulePinContext{}, moduleFindings
	}
	if moduleAnchor.Repository.ID != relationship.Target || !hasRole(moduleAnchor.Repository.Roles, "module") ||
		moduleAnchor.Module == nil {
		return modulePinContext{}, []domain.Finding{modulePinFinding(
			"GDS_MODULE_PIN_IDENTITY_MISMATCH", "Selected module boundary does not match the typed consumer relationship.",
		)}
	}
	if moduleAnchor.Module.PinPolicy != "default-branch-commit" {
		return modulePinContext{}, []domain.Finding{modulePinFinding(
			"GDS_MODULE_PIN_RELEASE_REQUIRED", "This module pin policy requires a verified versioned release before consumer update.",
		)}
	}
	moduleInfo, err := services.Git.RepositoryInfo(ctx, modulePath)
	if err != nil {
		return modulePinContext{}, []domain.Finding{modulePinFinding("GDS_MODULE_PIN_SOURCE_NOT_PROVEN", err.Error())}
	}
	moduleRoot := moduleInfo.WorktreeRoot
	moduleStatus, err := services.Git.InspectStatus(ctx, moduleRoot)
	if err != nil || moduleStatus.Head.Mode != "branch" ||
		moduleStatus.Branch.Name != moduleAnchor.Git.DefaultBranch || !checkoutStatusIsClean(moduleStatus) {
		return modulePinContext{}, []domain.Finding{modulePinFinding(
			"GDS_MODULE_PIN_SOURCE_STATE_UNSAFE", "Module source must be clean on its default branch.",
		)}
	}
	// The module's origin is observed, never written. `LocalPushSupported` used
	// to guard this line, which asks whether the module's remote accepts a push
	// from this device -- a question this operation never needs, since the only
	// mutation is a gitlink rewrite in the consumer. It refuses every remote that
	// is not a local path, so on a real estate it refused every module, and the
	// pin could not advance for that reason alone. `ObserveRemoteBranchOptional`
	// is an `ls-remote`, and proving the target commit is published is exactly
	// what this step is for.
	targetRef := "refs/heads/" + moduleAnchor.Git.DefaultBranch
	targetOID, found, err := services.GitMutations.ObserveRemoteBranchOptional(ctx, moduleRoot, "origin", targetRef)
	if err != nil || !found || targetOID != moduleStatus.Head.OID {
		return modulePinContext{}, []domain.Finding{modulePinFinding(
			"GDS_MODULE_PIN_TARGET_NOT_PUBLISHED", "Module default commit is not exactly published on its configured origin.",
		)}
	}
	// Required checks used to refuse the pin outright, whatever the module was:
	// any declared lane meant "no verified execution evidence" and there was no
	// way to supply any. Every module in this estate declares at least one, so
	// the command could not advance a single pin. The happy-path test did not
	// catch it because its module fixture declared no verification at all, which
	// is the one shape the refusal let through and the one no real module has.
	//
	// The evidence now exists. `gds module verify` runs a module's required lanes
	// against an exact commit in a throwaway worktree, and that is the same
	// question this refusal was asking. So it is asked, at the commit about to be
	// consumed rather than at whatever the worktree holds, and the refusal stands
	// only when a lane actually fails.
	verificationPlan, verificationFindings := moduleworkflow.PlanVerification(
		moduleAnchor, gitmodulesName, moduleRoot, targetOID,
	)
	if len(verificationFindings) != 0 {
		return modulePinContext{}, verificationFindings
	}
	verification, runFindings := services.runModuleLanes(
		ctx, moduleRoot, verificationPlan, defaultModuleCommandTimeout,
	)
	if len(runFindings) != 0 {
		return modulePinContext{}, runFindings
	}
	verificationDigest, err := canonicaljson.Digest(verifiedOutcome(verification))
	if err != nil {
		return modulePinContext{}, []domain.Finding{modulePinFinding(
			"GDS_MODULE_PIN_CHECKS_NOT_PROVEN", err.Error(),
		)}
	}
	if submodule == nil || submodule.GitlinkOID == "" || submodule.GitlinkStage != 0 ||
		submodule.GitlinkOID == targetOID || !pinWorktreeStateIsEligible(*submodule, targetOID) {
		return modulePinContext{}, []domain.Finding{modulePinFinding(
			"GDS_MODULE_PIN_GITLINK_NOT_ELIGIBLE",
			"Consumer requires one changed, stage-zero gitlink whose checkout is absent or already at the target commit.",
		)}
	}
	consumerCompiled := services.Compiler.CompileDirectory(estateRoot, consumer, compiler.DevelopmentBundleVersion)
	if len(consumerCompiled.Findings) != 0 {
		return modulePinContext{}, consumerCompiled.Findings
	}
	consumerManifestDigest, err := fileDigest(filepath.Join(consumerInfo.WorktreeRoot, ".gds", "repository.yaml"))
	if err != nil {
		return modulePinContext{}, []domain.Finding{modulePinFinding("GDS_MODULE_PIN_MANIFEST_NOT_PROVEN", err.Error())}
	}
	moduleManifestDigest, err := fileDigest(filepath.Join(moduleRoot, ".gds", "repository.yaml"))
	if err != nil {
		return modulePinContext{}, []domain.Finding{modulePinFinding("GDS_MODULE_PIN_SOURCE_MANIFEST_NOT_PROVEN", err.Error())}
	}
	fingerprint, err := canonicaljson.Digest(struct {
		ConsumerStatus gitprovider.Status    `json:"consumer_status"`
		Submodule      gitprovider.Submodule `json:"submodule"`
		ModuleID       string                `json:"module_id"`
		ModuleHead     string                `json:"module_head"`
		ModuleManifest string                `json:"module_manifest"`
		TargetRef      string                `json:"target_ref"`
		TargetOID      string                `json:"target_oid"`
		// Verification joins the fingerprint so the plan is bound to the evidence
		// that justified it. A plan approved while a lane passed must not stay
		// applicable after that lane stops passing.
		Verification string `json:"verification"`
	}{consumerStatus, *submodule, moduleAnchor.Repository.ID, moduleStatus.Head.OID, moduleManifestDigest, targetRef, targetOID, verificationDigest})
	if err != nil {
		return modulePinContext{}, []domain.Finding{modulePinFinding("GDS_MODULE_PIN_FINGERPRINT_FAILED", err.Error())}
	}
	return modulePinContext{
		assessment: ModulePinAssessment{
			ConsumerID: consumer.Repository.ID, ModuleID: moduleAnchor.Repository.ID,
			ConsumerRoot: consumerInfo.WorktreeRoot, ModuleRoot: moduleRoot,
			GitmodulesName: gitmodulesName, GitlinkPath: submodule.Path,
			ExpectedOldOID: submodule.GitlinkOID, TargetOID: targetOID, TargetRef: targetRef,
		},
		observation: operations.Observation{
			RepositoryID: consumer.Repository.ID, HeadOID: consumerStatus.Head.OID,
			WorktreeFingerprint: fingerprint, ManifestDigest: consumerManifestDigest,
			PolicyDigest: consumerCompiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

// verifiedOutcome reduces a verification report to what the plan is actually
// claiming: these commands, in these lanes, succeeded at this commit.
//
// The report itself cannot be digested. It carries per-command durations, so
// re-observing at apply -- which re-runs the lanes -- produces a different
// digest every time and the plan reads as stale before its handler is ever
// called. Durations and diagnostics are evidence for a reader, not part of the
// claim; the claim is which commands passed.
func verifiedOutcome(report ModuleVerification) []string {
	outcome := []string{report.GitlinkOID}
	for _, lane := range report.Lanes {
		for _, command := range lane.Commands {
			outcome = append(outcome, lane.Lane+"\x00"+command.Command+"\x00"+command.Status)
		}
	}
	return outcome
}

func loadModulePinPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, ModulePinAssessment, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, ModulePinAssessment{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != "update-module-pin" || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) != 1 || plan.Steps[0].Action != gitops.UpdateGitlinkAction ||
		len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, ModulePinAssessment{}, errors.New("stored plan is not a valid module pin plan")
	}
	raw, ok := plan.Steps[0].Parameters["gitlink_pin"].(map[string]any)
	if !ok {
		return operations.Plan{}, ModulePinAssessment{}, errors.New("module pin parameters are missing")
	}
	assessment := ModulePinAssessment{}
	assessment.ConsumerID = plan.Steps[0].RepositoryID
	assessment.ConsumerRoot, _ = raw["consumer_root"].(string)
	assessment.ModuleRoot, _ = raw["module_root"].(string)
	assessment.ModuleID, _ = raw["module_id"].(string)
	assessment.GitmodulesName, _ = raw["gitmodules_name"].(string)
	assessment.ExpectedOldOID, _ = raw["expected_old_oid"].(string)
	assessment.TargetOID, _ = raw["target_oid"].(string)
	assessment.TargetRef, _ = raw["target_ref"].(string)
	if assessment.ConsumerRoot == "" || assessment.ModuleRoot == "" || assessment.ModuleID == "" ||
		assessment.GitmodulesName == "" || assessment.ExpectedOldOID == "" ||
		assessment.TargetOID == "" || assessment.TargetRef == "" {
		return operations.Plan{}, ModulePinAssessment{}, errors.New("module pin parameters are incomplete")
	}
	return plan, assessment, nil
}

func modulePinPlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_MODULE_PIN_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable module pin update.",
	})
}

func modulePinFinding(code string, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}

func modulePinManagementFinding(relationship domain.Relationship) *domain.Finding {
	if relationship.PinManagement != "consumer-transaction" {
		return nil
	}
	finding := modulePinFinding(
		"GDS_MODULE_PIN_CONSUMER_TRANSACTION_REQUIRED",
		"Generic gitlink mutation is disabled for this consumer; use its repository-owned atomic pin transaction.",
	)
	return &finding
}

// pinConsumerStatusIsClean accepts the one shape a pin update necessarily
// produces: the submodule being repinned is checked out and advanced, so the
// consumer reports exactly one changed entry and that entry is a gitlink.
//
// The shared cleanliness rule counts that as dirty, which is why advancing a
// pin used to require `git submodule deinit` first -- an undocumented step that
// made the two guards read as a contradiction (`GDS_MODULE_PIN_CONSUMER_STATE_
// UNSAFE` wants a clean consumer, `GDS_MODULE_PIN_GITLINK_NOT_ELIGIBLE` wants a
// changed gitlink). Nothing else is relaxed: any staged, untracked, conflicted
// or second changed entry still refuses.
func pinConsumerStatusIsClean(status gitprovider.Status, target *gitprovider.Submodule) bool {
	if checkoutStatusIsClean(status) {
		return true
	}
	if target == nil || target.WorktreeState != "off-gitlink" {
		return false
	}
	return status.Changes.Staged == 0 && status.Changes.Untracked == 0 &&
		status.Changes.Conflicted == 0 && status.Submodules.Conflicted == 0 &&
		status.Changes.Unstaged == 1 && status.Changes.SubmoduleChanges == 1 &&
		status.Submodules.Modified <= 1
}

// pinWorktreeStateIsEligible allows the gitlink to be advanced either from an
// absent checkout or from one that already holds exactly the target commit.
// The second case is strictly more evidence than the first: the consumer's own
// working tree contains the commit about to be pinned.
func pinWorktreeStateIsEligible(submodule gitprovider.Submodule, targetOID string) bool {
	switch submodule.WorktreeState {
	case "uninitialized":
		return true
	case "off-gitlink":
		return submodule.CurrentOID != "" && submodule.CurrentOID == targetOID
	default:
		return false
	}
}
