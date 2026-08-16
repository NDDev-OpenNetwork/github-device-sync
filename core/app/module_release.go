package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubmutationruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitops"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type ModuleReleaseOptions struct {
	ProjectionOperationOptions
	Version string
	// RuntimeConfig and MutationRuntimeConfig are the private device-local GitHub
	// read and mutation runtime configurations. RuntimeConfig is also consulted
	// while planning or applying a release with required exact-commit checks.
	RuntimeConfig         string
	MutationRuntimeConfig string
	Assets                []string
	// HarnessEvidenceDirectory and HarnessEvidenceTrustPolicy locate the signed
	// private QA proof for a module owned by an active harness. They are required
	// for those modules and unused for modules with no active bridge mapping.
	HarnessEvidenceDirectory   string
	HarnessEvidenceTrustPolicy string
}

type ModuleReleaseAsset struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type ModuleReleaseAssessment struct {
	RepositoryID string `json:"repository_id"`
	ModuleRoot   string `json:"module_root"`
	Version      string `json:"version"`
	TagStyle     string `json:"tag_style"`
	TagRef       string `json:"tag_ref"`
	CommitOID    string `json:"commit_oid"`
	PinPolicy    string `json:"pin_policy"`
	ReleaseMode  string `json:"release_mode"`
	// The following fields identify the GitHub Release target and are only
	// populated for github-release mode. They are resolved from the module
	// anchor's provider locator and the canonical estate mutation capability.
	Owner                string                    `json:"owner,omitempty"`
	Name                 string                    `json:"name,omitempty"`
	Installation         string                    `json:"installation,omitempty"`
	ProviderRepositoryID int64                     `json:"provider_repository_id,omitempty"`
	MutationCapabilityID string                    `json:"mutation_capability_id,omitempty"`
	RequiredChecks       []githubprovider.CheckRun `json:"required_checks"`
	Assets               []ModuleReleaseAsset      `json:"assets"`
	// HarnessEvidence is the signed private QA proof for the exact released
	// commit. It is absent only for a module with no active harness mapping.
	HarnessEvidence *ModuleReleaseEvidence `json:"harness_evidence,omitempty"`
}

type ModuleReleasePlanData struct {
	Plan       operations.Plan         `json:"plan"`
	StatePath  string                  `json:"state_path"`
	Assessment ModuleReleaseAssessment `json:"assessment"`
}

type moduleReleaseContext struct {
	assessment  ModuleReleaseAssessment
	observation operations.Observation
}

type moduleReleaseObserver struct {
	services *Services
	root     string
	options  ModuleReleaseOptions
}

func (observer moduleReleaseObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.moduleReleaseContext(
		ctx, observer.root, observer.options,
	)
	if len(findings) != 0 || current.assessment.RepositoryID != repositoryID {
		return operations.Observation{}, errors.New("module release precondition is no longer proven")
	}
	return current.observation, nil
}

func (services *Services) PlanModuleRelease(
	ctx context.Context,
	path string,
	options ModuleReleaseOptions,
) domain.Envelope {
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope("gds module release plan", domain.ExitInput, nil, *finding)
	}
	current, findings := services.moduleReleaseContext(ctx, path, options)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module release plan", classifyFindings(findings), nil, findings...)
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds module release plan", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError("gds module release plan", err)
	}
	stepID, action, approvalClass := "publish-version-tag", gitops.PublishVersionTagAction, "publish-module-version-tag"
	if current.assessment.ReleaseMode == "github-release" {
		stepID, action, approvalClass = "publish-github-release", gitops.PublishGitHubReleaseAction, "publish-module-github-release"
	}
	checkParameters := make([]map[string]any, 0, len(current.assessment.RequiredChecks))
	for _, check := range current.assessment.RequiredChecks {
		checkParameters = append(checkParameters, map[string]any{
			"id": check.ID, "name": check.Name, "head_sha": check.HeadSHA,
			"status": check.Status, "conclusion": check.Conclusion,
			"completed_at": check.CompletedAt.Format("2006-01-02T15:04:05Z07:00"),
			"details_url":  check.DetailsURL, "external_id": check.ExternalID,
			"app_id": check.AppID, "app_slug": check.AppSlug,
			"run_id": check.RunID, "job_id": check.JobID,
		})
	}
	assetParameters := make([]map[string]any, 0, len(current.assessment.Assets))
	for _, asset := range current.assessment.Assets {
		assetParameters = append(assetParameters, map[string]any{
			"path": asset.Path, "name": asset.Name, "size": asset.Size, "sha256": asset.SHA256,
		})
	}
	releaseParameters := map[string]any{
		"module_root": current.assessment.ModuleRoot, "version": current.assessment.Version,
		"tag_style": current.assessment.TagStyle,
		"tag_ref":   current.assessment.TagRef, "commit_oid": current.assessment.CommitOID,
		"required_checks": checkParameters,
		"assets":          assetParameters,
	}
	// Binding the signed proof into the plan makes the approved release
	// inseparable from the QA that justified it: the digest cannot be swapped for
	// another harness's pass, and re-observation must reproduce it exactly.
	if evidence := current.assessment.HarnessEvidence; evidence != nil {
		releaseParameters["harness_evidence"] = map[string]any{
			"harness_id": evidence.HarnessID, "harness_root_sha": evidence.HarnessRootSHA,
			"module_sha": evidence.ModuleSHA, "executable_version": evidence.ExecutableVersion,
			"suite_version": evidence.SuiteVersion, "suite_cases_digest": evidence.SuiteCasesDigest,
			"evidence_digest": evidence.EvidenceDigest,
			"generated_at":    evidence.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"),
			"expires_at":      evidence.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		}
	}
	plan, err := operations.NewPlan(planID, now, now.Add(projectionPlanLifetime), operations.PlanInput{
		Operation: "release-module-version",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID: current.observation.RepositoryID, HeadOID: current.observation.HeadOID,
			WorktreeFingerprint: current.observation.WorktreeFingerprint,
			RemoteDefaultOID:    current.observation.RemoteDefaultOID,
			ManifestDigest:      current.observation.ManifestDigest, PolicyDigest: current.observation.PolicyDigest,
		}},
		Steps: []operations.Step{{
			StepID: stepID, RepositoryID: current.assessment.RepositoryID,
			Action: action, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "manual"},
			Parameters:   map[string]any{"module_release": releaseParameters},
		}},
		ApprovalClass: approvalClass,
	})
	if err != nil {
		return domain.InternalError("gds module release plan", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		moduleReleaseObserver{
			services: services, root: current.assessment.ModuleRoot, options: options,
		},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope("gds module release plan", err)
	}
	envelope := domain.Success("gds module release plan", ModuleReleasePlanData{
		Plan: plan, StatePath: statePath, Assessment: current.assessment,
	})
	envelope.Scope["repository_id"] = current.assessment.RepositoryID
	return envelope
}

func (services *Services) ApplyModuleRelease(
	ctx context.Context,
	planID string,
	options ModuleReleaseOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return syncIdentifierRequired("gds module release apply", "plan", "--apply")
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope("gds module release apply", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds module release apply", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	plan, assessment, err := loadModuleReleasePlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return moduleReleasePlanInvalid("gds module release apply")
	}
	handlers, blocker := services.moduleReleaseApplyHandlers(ctx, assessment, options)
	if blocker != nil {
		return *blocker
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		moduleReleaseObserver{services: services, root: assessment.ModuleRoot,
			options: moduleReleaseObservationOptions(options, assessment)},
		handlers,
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, plan.PlanID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope("gds module release apply", err)
		envelope.Data = result
		envelope.OperationID = result.OperationID
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		return envelope
	}
	envelope := domain.Success("gds module release apply", result)
	envelope.OperationID = result.OperationID
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.Scope["repository_id"] = assessment.RepositoryID
	return envelope
}

// moduleReleaseApplyHandlers assembles the mode-specific action handler set for
// an apply. The version-tag path keeps the isolated local Git mutation provider
// exactly as before; the github-release path re-derives the current provider
// scope, gates on the canonical estate mutation capability, builds the separate
// mutation runtime, and binds a repository-scoped GitHub Release mutator. It
// returns a blocking envelope instead of handlers when the release is not
// authorized or the runtime cannot be proven.
func (services *Services) moduleReleaseApplyHandlers(
	ctx context.Context,
	assessment ModuleReleaseAssessment,
	options ModuleReleaseOptions,
) (map[string]operations.ActionHandler, *domain.Envelope) {
	const command = "gds module release apply"
	if assessment.ReleaseMode != "github-release" {
		if err := services.GitMutations.LocalPushSupported(ctx, assessment.ModuleRoot); err != nil {
			envelope := domain.NewEnvelope(command, domain.ExitUnsupported, nil, domain.Finding{
				Code: "GDS_MODULE_RELEASE_LIVE_PROVIDER_DISABLED", Severity: domain.SeverityHigh,
				Message: "Module release apply is restricted to the verified isolated local provider.",
			})
			return nil, &envelope
		}
		handler, err := gitops.NewPublishVersionTagHandler(services.GitMutations)
		if err != nil {
			envelope := domain.InternalError(command, err)
			return nil, &envelope
		}
		return map[string]operations.ActionHandler{gitops.PublishVersionTagAction: handler}, nil
	}
	current, findings := services.moduleReleaseContext(
		ctx, assessment.ModuleRoot, moduleReleaseObservationOptions(options, assessment),
	)
	if len(findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return nil, &envelope
	}
	if current.assessment.ReleaseMode != "github-release" ||
		current.assessment.RepositoryID != assessment.RepositoryID ||
		current.assessment.ModuleRoot != assessment.ModuleRoot ||
		current.assessment.Version != assessment.Version ||
		current.assessment.TagStyle != assessment.TagStyle ||
		current.assessment.TagRef != assessment.TagRef ||
		current.assessment.CommitOID != assessment.CommitOID ||
		!equalCheckRuns(current.assessment.RequiredChecks, assessment.RequiredChecks) ||
		!equalModuleReleaseAssets(current.assessment.Assets, assessment.Assets) ||
		!equalModuleReleaseEvidence(current.assessment.HarnessEvidence, assessment.HarnessEvidence) {
		envelope := domain.NewEnvelope(command, domain.ExitStale, nil, domain.Finding{
			Code: "GDS_MODULE_RELEASE_PLAN_SCOPE_CHANGED", Severity: domain.SeverityHigh,
			Message: "The immutable module release plan no longer matches the resolved module scope.",
		})
		return nil, &envelope
	}
	estateRoot, anchor, policyFindings := services.policyInputs(ctx, assessment.ModuleRoot)
	if len(policyFindings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(policyFindings), nil, policyFindings...)
		return nil, &envelope
	}
	desired, estateFindings := estate.Load(estateRoot, services.Schemas)
	if len(estateFindings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(estateFindings), nil, estateFindings...)
		return nil, &envelope
	}
	capability, found := mutationCapabilityForInstallation(desired, current.assessment.Installation)
	if !found || capability.Mutation.ID != current.assessment.MutationCapabilityID {
		envelope := domain.NewEnvelope(command, domain.ExitPolicy, nil, domain.Finding{
			Code: "GDS_MODULE_RELEASE_MUTATION_CAPABILITY_MISSING", Severity: domain.SeverityHigh,
			Message: "No canonical mutation capability is bound to the module installation.",
		})
		return nil, &envelope
	}
	if ready, blocker := moduleReleaseMutationGate(desired, capability, anchor.Repository.Lifecycle); !ready {
		envelope := domain.NewEnvelope(command, domain.ExitPolicy, nil, domain.Finding{
			Code: "GDS_MODULE_RELEASE_MUTATION_BLOCKED", Severity: domain.SeverityHigh, Message: blocker,
		})
		return nil, &envelope
	}
	readConfig, err := githubruntime.Load(options.RuntimeConfig, desired, services.Schemas)
	if err != nil {
		envelope := githubRuntimeError(command, err)
		return nil, &envelope
	}
	mutationConfig, err := githubmutationruntime.Load(options.MutationRuntimeConfig, desired, services.Schemas)
	if err != nil {
		envelope := githubMutationRuntimeError(command, err)
		return nil, &envelope
	}
	mutators, err := githubmutationruntime.BuildMutators(
		mutationConfig, readConfig, desired, services.GitHubMutationRuntimeBuildOptions,
	)
	if err != nil {
		envelope := githubMutationRuntimeError(command, err)
		return nil, &envelope
	}
	factory, found := mutators[current.assessment.MutationCapabilityID]
	if !found {
		envelope := githubMutationRuntimeError(command, errors.New("mutation capability is unavailable"))
		return nil, &envelope
	}
	writer, err := factory.BindRepository(githubprovider.RepositoryMutationScope{
		RepositoryID: current.assessment.ProviderRepositoryID,
		Owner:        current.assessment.Owner, Name: current.assessment.Name,
		Operations: []string{githubprovider.MutationRepositoryRelease},
	})
	if err != nil {
		envelope := githubMutationRuntimeError(command, err)
		return nil, &envelope
	}
	handler, err := gitops.NewPublishGitHubReleaseHandler(writer)
	if err != nil {
		envelope := domain.InternalError(command, err)
		return nil, &envelope
	}
	return map[string]operations.ActionHandler{gitops.PublishGitHubReleaseAction: handler}, nil
}

// moduleReleaseMutationGate authorizes a github-release apply against the
// canonical estate mutation capability. It mirrors the conservative posture of
// the GitHub governance gate: the estate must not disable mutation, the module
// lifecycle must be in the capability scope, and the capability must permit the
// repository-release operation.
func moduleReleaseMutationGate(
	desired estate.Config,
	capability estate.MutationCapability,
	lifecycle string,
) (bool, string) {
	if desired.Root.Rollout.MutationMode == "disabled" {
		return false, "Canonical estate mutation_mode is disabled."
	}
	if !governanceContains(capability.Scope.Lifecycles, lifecycle) {
		return false, "Mutation capability scope does not include the module lifecycle."
	}
	if !governanceContains(capability.Operations, githubprovider.MutationRepositoryRelease) {
		return false, "Mutation capability does not permit repository-release operations."
	}
	return true, ""
}

func (services *Services) VerifyModuleRelease(
	ctx context.Context,
	operationID string,
	options ModuleReleaseOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return syncIdentifierRequired("gds module release verify", "operation", "--verify")
	}
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope("gds module release verify", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds module release verify", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	operation, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return moduleReleasePlanInvalid("gds module release verify")
	}
	plan, assessment, err := loadModuleReleasePlan(ctx, store, operation.PlanID, services.Schemas)
	if err != nil {
		return moduleReleasePlanInvalid("gds module release verify")
	}
	var handlers map[string]operations.ActionHandler
	if assessment.ReleaseMode == "github-release" {
		// Verification re-reads the live release through the read runtime. It is an
		// observation, so it must not require mutation authority, and it must not
		// be satisfied by recorded evidence alone.
		observer, blocker := services.moduleReleaseObserverBinding(ctx, assessment, options)
		if blocker != nil {
			return *blocker
		}
		handler, handlerErr := gitops.NewVerifyGitHubReleaseHandler(observer)
		if handlerErr != nil {
			return domain.InternalError("gds module release verify", handlerErr)
		}
		handlers = map[string]operations.ActionHandler{gitops.PublishGitHubReleaseAction: handler}
	} else {
		handler, handlerErr := gitops.NewPublishVersionTagHandler(services.GitMutations)
		if handlerErr != nil {
			return domain.InternalError("gds module release verify", handlerErr)
		}
		handlers = map[string]operations.ActionHandler{gitops.PublishVersionTagAction: handler}
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, moduleReleaseObserver{},
		handlers,
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope("gds module release verify", err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success("gds module release verify", result)
	envelope.OperationID = operationID
	envelope.Scope["repository_id"] = assessment.RepositoryID
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

// moduleReleaseObservationOptions rebuilds the observation inputs from the
// immutable plan rather than from the caller's flags, so re-observation during
// apply and verify is bound to what was approved. Only the locations of the
// signed evidence and its trust policy come from the caller, because a path is
// device-local; the evidence content itself is still checked against the plan.
func moduleReleaseObservationOptions(
	options ModuleReleaseOptions,
	assessment ModuleReleaseAssessment,
) ModuleReleaseOptions {
	observation := options
	observation.Version = assessment.Version
	observation.Assets = moduleReleaseAssetPaths(assessment.Assets)
	return observation
}

func (services *Services) moduleReleaseContext(
	ctx context.Context,
	path string,
	options ModuleReleaseOptions,
) (moduleReleaseContext, []domain.Finding) {
	version, runtimeConfig, assetPaths := options.Version, options.RuntimeConfig, options.Assets
	estateRoot, moduleAnchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return moduleReleaseContext{}, findings
	}
	if !hasRole(moduleAnchor.Repository.Roles, "module") || moduleAnchor.Module == nil {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_ROLE_REQUIRED", "Repository is not a declared independently versioned module.",
		)}
	}
	tagStyle := moduleAnchor.Release.TagStyle
	if tagStyle == "" {
		tagStyle = "v-semver"
	}
	tagRef, err := gitprovider.VersionTagRefWithStyle(version, tagStyle)
	if err != nil {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding("GDS_MODULE_RELEASE_VERSION_INVALID", err.Error())}
	}
	releaseMode := moduleAnchor.Release.Mode
	switch releaseMode {
	case "package-version":
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_REGISTRY_PROVIDER_REQUIRED", "Package-version release requires a verified registry provider.",
		)}
	case "none":
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_NOT_REQUIRED", "Repository release mode does not publish a module release.",
		)}
	case "version-tag":
	case "github-release":
	default:
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_POLICY_INVALID", "Repository release mode is not valid for a module release.",
		)}
	}
	// A required GitHub Release publication is the expected state for
	// github-release mode; only non-github-release modes must treat it as an
	// unmet provider dependency.
	if releaseMode != "github-release" && moduleAnchor.Module.Publication.GitHubRelease == "required" {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_GITHUB_PROVIDER_REQUIRED", "Policy requires a GitHub Release provider that is not enabled.",
		)}
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding("GDS_MODULE_RELEASE_BOUNDARY_NOT_PROVEN", err.Error())}
	}
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil || status.Head.Mode != "branch" || status.Branch.Name != moduleAnchor.Git.DefaultBranch ||
		status.Head.OID == "" || !checkoutStatusIsClean(status) {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_GIT_STATE_UNSAFE", "Module release requires a clean attached default branch.",
		)}
	}
	// The isolated local-provider restriction only applies to version-tag mode,
	// which publishes the immutable tag by pushing to the git remote. In
	// github-release mode the live provider is the GitHub Release API, which is
	// authorized separately at apply time through the estate mutation capability.
	if releaseMode != "github-release" {
		if err := services.GitMutations.LocalPushSupported(ctx, info.WorktreeRoot); err != nil {
			return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
				"GDS_MODULE_RELEASE_LIVE_PROVIDER_DISABLED", "Module release target is restricted to the isolated local provider.",
			)}
		}
	}
	defaultRef := "refs/heads/" + moduleAnchor.Git.DefaultBranch
	remoteDefault, found, err := services.GitMutations.ObserveRemoteBranchOptional(
		ctx, info.WorktreeRoot, "origin", defaultRef,
	)
	if err != nil || !found || remoteDefault != status.Head.OID {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_COMMIT_NOT_PUBLISHED", "Module release commit is not exactly published on origin default.",
		)}
	}
	var tag gitprovider.TagEvidence
	if releaseMode == "github-release" {
		var tagFindings []domain.Finding
		tag, tagFindings = services.observeGitHubReleaseTargetAbsent(
			ctx, estateRoot, moduleAnchor, status.Head.OID, tagRef, runtimeConfig,
		)
		if len(tagFindings) != 0 {
			return moduleReleaseContext{}, tagFindings
		}
	} else {
		tag, err = services.GitMutations.ObserveVersionTag(ctx, info.WorktreeRoot, tagRef, status.Head.OID)
		if err != nil || tag.LocalOID != strings.Repeat("0", len(status.Head.OID)) ||
			tag.RemoteOID != strings.Repeat("0", len(status.Head.OID)) {
			return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
				"GDS_MODULE_RELEASE_TAG_CONFLICT", "Requested immutable version tag already exists or cannot be proved absent.",
			)}
		}
	}
	requiredChecks, checkFindings := services.moduleReleaseChecks(
		ctx, estateRoot, moduleAnchor, status.Head.OID, runtimeConfig,
	)
	if len(checkFindings) != 0 {
		return moduleReleaseContext{}, checkFindings
	}
	harnessEvidence, evidenceFindings := services.moduleReleaseHarnessEvidence(
		estateRoot, moduleAnchor, status.Head.OID, options,
	)
	if len(evidenceFindings) != 0 {
		return moduleReleaseContext{}, evidenceFindings
	}
	assets, assetErr := inspectModuleReleaseAssets(assetPaths)
	if assetErr != nil {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_ASSET_INVALID", assetErr.Error(),
		)}
	}
	if releaseMode == "github-release" && len(assets) == 0 {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_ASSET_REQUIRED", "GitHub release mode requires at least one exact asset.",
		)}
	}
	if releaseMode != "github-release" && len(assets) != 0 {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_ASSET_UNSUPPORTED", "Release assets are supported only by github-release mode.",
		)}
	}
	compiled := services.Compiler.CompileDirectory(estateRoot, moduleAnchor, compiler.DevelopmentBundleVersion)
	if len(compiled.Findings) != 0 {
		return moduleReleaseContext{}, compiled.Findings
	}
	manifestDigest, err := fileDigest(filepath.Join(info.WorktreeRoot, ".gds", "repository.yaml"))
	if err != nil {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding("GDS_MODULE_RELEASE_MANIFEST_NOT_PROVEN", err.Error())}
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Status          gitprovider.Status        `json:"status"`
		RemoteDefault   string                    `json:"remote_default"`
		Tag             gitprovider.TagEvidence   `json:"tag"`
		Version         string                    `json:"version"`
		TagStyle        string                    `json:"tag_style"`
		RequiredChecks  []githubprovider.CheckRun `json:"required_checks"`
		Assets          []ModuleReleaseAsset      `json:"assets"`
		HarnessEvidence *ModuleReleaseEvidence    `json:"harness_evidence"`
	}{status, remoteDefault, tag, version, tagStyle, requiredChecks, assets, harnessEvidence})
	if err != nil {
		return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding("GDS_MODULE_RELEASE_FINGERPRINT_FAILED", err.Error())}
	}
	assessment := ModuleReleaseAssessment{
		RepositoryID: moduleAnchor.Repository.ID, ModuleRoot: info.WorktreeRoot,
		Version: version, TagStyle: tagStyle, TagRef: tagRef, CommitOID: status.Head.OID,
		PinPolicy: moduleAnchor.Module.PinPolicy, ReleaseMode: releaseMode,
		RequiredChecks: requiredChecks, Assets: assets, HarnessEvidence: harnessEvidence,
	}
	if releaseMode == "github-release" {
		if moduleAnchor.Provider.Type != "github" || moduleAnchor.Provider.Owner == "" ||
			moduleAnchor.Provider.Name == "" || moduleAnchor.Provider.Installation == "" ||
			moduleAnchor.Provider.RepositoryID <= 0 {
			return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
				"GDS_MODULE_RELEASE_PROVIDER_SCOPE_INVALID", "GitHub release requires an exact GitHub provider owner, name, installation, and repository id.",
			)}
		}
		desired, estateFindings := estate.Load(estateRoot, services.Schemas)
		if len(estateFindings) != 0 {
			return moduleReleaseContext{}, estateFindings
		}
		capability, found := mutationCapabilityForInstallation(desired, moduleAnchor.Provider.Installation)
		if !found {
			return moduleReleaseContext{}, []domain.Finding{moduleReleaseFinding(
				"GDS_MODULE_RELEASE_MUTATION_CAPABILITY_MISSING", "No canonical mutation capability is bound to the module installation.",
			)}
		}
		assessment.Owner = moduleAnchor.Provider.Owner
		assessment.Name = moduleAnchor.Provider.Name
		assessment.Installation = moduleAnchor.Provider.Installation
		assessment.ProviderRepositoryID = moduleAnchor.Provider.RepositoryID
		assessment.MutationCapabilityID = capability.Mutation.ID
	}
	return moduleReleaseContext{
		assessment: assessment,
		observation: operations.Observation{
			RepositoryID: moduleAnchor.Repository.ID, HeadOID: status.Head.OID,
			WorktreeFingerprint: fingerprint, RemoteDefaultOID: remoteDefault,
			ManifestDigest: manifestDigest, PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func (services *Services) observeGitHubReleaseTargetAbsent(
	ctx context.Context,
	estateRoot string,
	anchor domain.RepositoryAnchor,
	commitOID string,
	tagRef string,
	runtimeConfig string,
) (gitprovider.TagEvidence, []domain.Finding) {
	desired, findings := estate.Load(estateRoot, services.Schemas)
	if len(findings) != 0 {
		return gitprovider.TagEvidence{}, findings
	}
	config, err := githubruntime.Load(runtimeConfig, desired, services.Schemas)
	if err != nil {
		return gitprovider.TagEvidence{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_TARGET_NOT_PROVEN", err.Error(),
		)}
	}
	readers, err := githubruntime.BuildReaders(config, desired, services.GitHubRuntimeBuildOptions)
	if err != nil {
		return gitprovider.TagEvidence{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_TARGET_NOT_PROVEN", err.Error(),
		)}
	}
	reader, found := readers[anchor.Provider.Installation]
	if !found {
		return gitprovider.TagEvidence{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_TARGET_NOT_PROVEN", "The repository installation has no configured GitHub reader.",
		)}
	}
	repository, _, _, err := reader.GetRepository(ctx, anchor.Provider.Owner, anchor.Provider.Name, "")
	if err != nil || repository.ID != anchor.Provider.RepositoryID {
		message := "GitHub reader could not prove access to the exact release repository."
		if err != nil {
			message = err.Error()
		}
		return gitprovider.TagEvidence{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_TARGET_NOT_PROVEN", message,
		)}
	}
	tagName := strings.TrimPrefix(tagRef, "refs/tags/")
	_, _, tagFound, err := reader.GetVersionTagRefOptional(
		ctx, anchor.Provider.Owner, anchor.Provider.Name, tagName,
	)
	if err != nil {
		return gitprovider.TagEvidence{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_TARGET_NOT_PROVEN", err.Error(),
		)}
	}
	_, _, releaseFound, err := reader.GetReleaseByTagOptional(
		ctx, anchor.Provider.Owner, anchor.Provider.Name, tagName,
	)
	if err != nil {
		return gitprovider.TagEvidence{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_TARGET_NOT_PROVEN", err.Error(),
		)}
	}
	if tagFound || releaseFound {
		return gitprovider.TagEvidence{}, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_TAG_CONFLICT", "Requested immutable GitHub release tag or release already exists.",
		)}
	}
	zero := strings.Repeat("0", len(commitOID))
	return gitprovider.TagEvidence{
		TagRef: tagRef, CommitOID: strings.ToLower(commitOID), LocalOID: zero, RemoteOID: zero,
	}, nil
}

func loadModuleReleasePlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, ModuleReleaseAssessment, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, ModuleReleaseAssessment{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	releaseMode, modeOK := moduleReleaseModeForAction(func() string {
		if len(plan.Steps) == 1 {
			return plan.Steps[0].Action
		}
		return ""
	}())
	if err != nil || plan.Operation != "release-module-version" || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) != 1 || !modeOK || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, ModuleReleaseAssessment{}, errors.New("stored plan is not a valid module release plan")
	}
	raw, ok := plan.Steps[0].Parameters["module_release"].(map[string]any)
	if !ok {
		return operations.Plan{}, ModuleReleaseAssessment{}, errors.New("module release parameters are missing")
	}
	assessment := ModuleReleaseAssessment{RepositoryID: plan.Steps[0].RepositoryID, ReleaseMode: releaseMode}
	assessment.ModuleRoot, _ = raw["module_root"].(string)
	assessment.Version, _ = raw["version"].(string)
	assessment.TagStyle, _ = raw["tag_style"].(string)
	if assessment.TagStyle == "" {
		assessment.TagStyle = "v-semver"
	}
	assessment.TagRef, _ = raw["tag_ref"].(string)
	assessment.CommitOID, _ = raw["commit_oid"].(string)
	checks, checksOK := decodeModuleReleaseChecks(raw["required_checks"])
	assessment.RequiredChecks = checks
	assets, assetsOK := decodeModuleReleaseAssets(raw["assets"])
	assessment.Assets = assets
	evidence, evidenceOK := decodeModuleReleaseEvidence(raw["harness_evidence"])
	assessment.HarnessEvidence = evidence
	if assessment.ModuleRoot == "" || assessment.Version == "" || assessment.TagRef == "" || assessment.CommitOID == "" {
		return operations.Plan{}, ModuleReleaseAssessment{}, errors.New("module release parameters are incomplete")
	}
	if !checksOK || !assetsOK || !evidenceOK {
		return operations.Plan{}, ModuleReleaseAssessment{}, errors.New("module release check evidence is invalid")
	}
	return plan, assessment, nil
}

func inspectModuleReleaseAssets(paths []string) ([]ModuleReleaseAsset, error) {
	if len(paths) > 16 {
		return nil, errors.New("release asset count exceeds 16")
	}
	result := make([]ModuleReleaseAsset, 0, len(paths))
	names := make(map[string]struct{}, len(paths))
	var total int64
	for _, candidate := range paths {
		absolute, err := filepath.Abs(candidate)
		if err != nil || filepath.Clean(absolute) != absolute {
			return nil, errors.New("release asset path is invalid")
		}
		info, err := os.Lstat(absolute)
		stat, ownerOK := infoSysStat(info)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			!ownerOK || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o022 != 0 ||
			info.Size() < 1 || info.Size() > 64<<20 {
			return nil, fmt.Errorf("release asset %q is not a safe bounded regular file", absolute)
		}
		name := filepath.Base(absolute)
		if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") {
			return nil, errors.New("release asset basename is invalid")
		}
		if _, duplicate := names[name]; duplicate {
			return nil, fmt.Errorf("release asset basename %q is duplicated", name)
		}
		names[name] = struct{}{}
		file, err := os.Open(absolute)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		written, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || written != info.Size() {
			return nil, errors.Join(copyErr, closeErr, errors.New("release asset changed while hashing"))
		}
		total += info.Size()
		if total > 128<<20 {
			return nil, errors.New("release asset total exceeds 128 MiB")
		}
		result = append(result, ModuleReleaseAsset{Path: absolute, Name: name, Size: info.Size(), SHA256: fmt.Sprintf("sha256:%x", hash.Sum(nil))})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func infoSysStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func equalModuleReleaseAssets(left, right []ModuleReleaseAsset) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func moduleReleaseAssetPaths(assets []ModuleReleaseAsset) []string {
	paths := make([]string, 0, len(assets))
	for _, asset := range assets {
		paths = append(paths, asset.Path)
	}
	return paths
}

func decodeModuleReleaseAssets(raw any) ([]ModuleReleaseAsset, bool) {
	values, ok := raw.([]any)
	if !ok || len(values) > 16 {
		return nil, false
	}
	result := make([]ModuleReleaseAsset, 0, len(values))
	for _, value := range values {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		asset := ModuleReleaseAsset{}
		asset.Path, _ = entry["path"].(string)
		asset.Name, _ = entry["name"].(string)
		asset.SHA256, _ = entry["sha256"].(string)
		size, sizeOK := entry["size"].(float64)
		asset.Size = int64(size)
		if !sizeOK || float64(asset.Size) != size || asset.Path == "" || asset.Name == "" ||
			!strings.HasPrefix(asset.SHA256, "sha256:") || len(asset.SHA256) != 71 ||
			asset.Size < 1 || asset.Size > 64<<20 {
			return nil, false
		}
		result = append(result, asset)
	}
	return result, true
}

func (services *Services) moduleReleaseChecks(
	ctx context.Context,
	estateRoot string,
	anchor domain.RepositoryAnchor,
	commitOID string,
	runtimeConfig string,
) ([]githubprovider.CheckRun, []domain.Finding) {
	required := requiredReleaseCheckContexts(anchor)
	if len(required) == 0 {
		return []githubprovider.CheckRun{}, nil
	}
	if anchor.Provider.Type != "github" || anchor.Provider.Owner == "" ||
		anchor.Provider.Name == "" || anchor.Provider.Installation == "" {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_CHECK_PROVIDER_INVALID",
			"Required release checks need an exact GitHub provider scope.",
		)}
	}
	desired, findings := estate.Load(estateRoot, services.Schemas)
	if len(findings) != 0 {
		return nil, findings
	}
	config, err := githubruntime.Load(runtimeConfig, desired, services.Schemas)
	if err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_CHECK_RUNTIME_NOT_PROVEN", err.Error(),
		)}
	}
	readers, err := githubruntime.BuildReaders(config, desired, services.GitHubRuntimeBuildOptions)
	if err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_CHECK_RUNTIME_NOT_PROVEN", err.Error(),
		)}
	}
	reader, found := readers[anchor.Provider.Installation]
	if !found {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_CHECK_RUNTIME_NOT_PROVEN",
			"The repository installation has no configured GitHub reader.",
		)}
	}
	runs, _, err := reader.ListCheckRuns(
		ctx, anchor.Provider.Owner, anchor.Provider.Name, commitOID,
	)
	if err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_CHECKS_NOT_PROVEN", err.Error(),
		)}
	}
	result, err := selectRequiredReleaseChecks(required, runs, anchor, commitOID)
	if err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_CHECKS_NOT_PROVEN", err.Error(),
		)}
	}
	return result, nil
}

// requiredReleaseCheckContexts resolves which provider check-run contexts must be
// green before a release.
//
// Two namespaces can supply them and they are not interchangeable.
// release.required_checks names provider contexts directly and is authoritative:
// it can express any context name, including ones outside the verification lane
// enum such as "ci-gate" or "CodeQL / CodeQL (go)".
//
// verification.required names local command lanes ("test", "lint"). It is read
// only as a compatibility fallback, because the active module repositories
// deliberately publish a check run named after their required lane so this gate
// resolves. That convention works only while every required lane name is also a
// real context; a repository whose check is named anything else must declare
// release.required_checks instead, which is why the explicit list wins.
func requiredReleaseCheckContexts(anchor domain.RepositoryAnchor) []string {
	if len(anchor.Release.RequiredChecks) != 0 {
		return anchor.Release.RequiredChecks
	}
	return anchor.Verification.Required
}

func selectRequiredReleaseChecks(
	requiredNames []string,
	runs []githubprovider.CheckRun,
	anchor domain.RepositoryAnchor,
	commitOID string,
) ([]githubprovider.CheckRun, error) {
	byName := make(map[string][]githubprovider.CheckRun, len(runs))
	for _, run := range runs {
		byName[run.Name] = append(byName[run.Name], run)
	}
	result := make([]githubprovider.CheckRun, 0, len(requiredNames))
	for _, required := range requiredNames {
		matches := byName[required]
		if len(matches) != 1 || !trustedSuccessfulActionsCheck(matches[0], anchor, commitOID) {
			return nil, fmt.Errorf("required check %q lacks one trusted exact-commit success", required)
		}
		result = append(result, matches[0])
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result, nil
}

func trustedSuccessfulActionsCheck(
	run githubprovider.CheckRun,
	anchor domain.RepositoryAnchor,
	commitOID string,
) bool {
	if run.HeadSHA != commitOID || run.Status != "completed" || run.Conclusion != "success" ||
		run.AppID != 15368 || run.AppSlug != "github-actions" || run.CompletedAt.IsZero() ||
		run.RunID < 1 || run.JobID < 1 {
		return false
	}
	parsed, err := url.Parse(run.DetailsURL)
	prefix := "/" + anchor.Provider.Owner + "/" + anchor.Provider.Name + "/actions/runs/"
	return err == nil && parsed.Scheme == "https" && strings.EqualFold(parsed.Host, "github.com") &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
		strings.HasPrefix(parsed.Path, prefix)
}

func equalCheckRuns(left []githubprovider.CheckRun, right []githubprovider.CheckRun) bool {
	leftDigest, leftErr := canonicaljson.Digest(left)
	rightDigest, rightErr := canonicaljson.Digest(right)
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}

func decodeModuleReleaseChecks(raw any) ([]githubprovider.CheckRun, bool) {
	values, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, false
	}
	checks := []githubprovider.CheckRun{}
	if err := json.Unmarshal(encoded, &checks); err != nil {
		return nil, false
	}
	return checks, true
}

func moduleReleasePlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_MODULE_RELEASE_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable module release.",
	})
}

// moduleReleaseModeForAction maps a stored plan step action back to its release
// mode so verification and apply can select the exact handler the plan implies
// without weakening plan validation to accept an arbitrary action.
func moduleReleaseModeForAction(action string) (string, bool) {
	switch action {
	case gitops.PublishVersionTagAction:
		return "version-tag", true
	case gitops.PublishGitHubReleaseAction:
		return "github-release", true
	default:
		return "", false
	}
}

func moduleReleaseFinding(code string, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
