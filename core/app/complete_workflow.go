package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitops"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type CompleteOptions struct {
	StatePath         string
	DeviceID          string
	SessionID         string
	ApprovalReference string
	TaskID            string
	Checkouts         []string
	RefreshMaxAge     time.Duration
}

type CompleteAssessment struct {
	RepositoryID       string             `json:"repository_id,omitempty"`
	WorktreeRoot       string             `json:"worktree_root"`
	Eligible           bool               `json:"eligible"`
	Reason             string             `json:"reason"`
	ApplySupported     bool               `json:"apply_supported"`
	Status             gitprovider.Status `json:"status"`
	DefaultBranchRef   string             `json:"default_branch_ref,omitempty"`
	TaskBranchRef      string             `json:"task_branch_ref,omitempty"`
	ExpectedDefaultOID string             `json:"expected_default_oid,omitempty"`
	ExpectedTaskOID    string             `json:"expected_task_oid,omitempty"`
	RequiredChecks     []HandoffCheck     `json:"required_checks"`
	IntegrationPolicy  string             `json:"integration_policy,omitempty"`
	DependencyOrder    int                `json:"dependency_order"`
}

type CompletePlanData struct {
	Plan        *operations.Plan     `json:"plan,omitempty"`
	StatePath   string               `json:"state_path"`
	TaskID      string               `json:"task_id"`
	Assessments []CompleteAssessment `json:"assessments"`
}

type completeContext struct {
	root         string
	repositoryID string
	anchor       domain.RepositoryAnchor
	observation  operations.Observation
	assessment   CompleteAssessment
}

type completeObserver struct {
	services *Services
	store    *state.Store
	roots    map[string]string
	maxAge   time.Duration
}

func (observer completeObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	root, found := observer.roots[repositoryID]
	if !found {
		return operations.Observation{}, errors.New("completion repository is outside the stored plan")
	}
	current, _ := observer.services.completeContext(ctx, root, observer.store, observer.maxAge)
	if !current.assessment.Eligible || current.repositoryID != repositoryID {
		return operations.Observation{}, errors.New("completion checkout is no longer eligible")
	}
	return current.observation, nil
}

func (services *Services) PlanComplete(
	ctx context.Context,
	path string,
	options CompleteOptions,
) domain.Envelope {
	if finding := validateOperationActor(options.DeviceID, options.SessionID); finding != nil {
		return domain.NewEnvelope("gds complete plan", domain.ExitInput, nil, *finding)
	}
	if !identity.Valid("task", options.TaskID) {
		return domain.NewEnvelope("gds complete plan", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_COMPLETE_TASK_ID_INVALID", Severity: domain.SeverityHigh,
			Message: "Completion planning requires a canonical task id.",
		})
	}
	maxAge, finding := validateRefreshMaxAge(options.RefreshMaxAge)
	if finding != nil {
		return domain.NewEnvelope("gds complete plan", domain.ExitInput, nil, *finding)
	}
	selected := append([]string(nil), options.Checkouts...)
	if len(selected) == 0 {
		selected = []string{path}
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds complete plan", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	roots, rootFindings := services.selectedCheckoutRoots(ctx, selected)
	if len(rootFindings) != 0 {
		return domain.NewEnvelope("gds complete plan", classifyFindings(rootFindings),
			CompletePlanData{StatePath: statePath, TaskID: options.TaskID, Assessments: []CompleteAssessment{}},
			rootFindings...,
		)
	}
	contexts := make([]completeContext, 0, len(roots))
	findings := []domain.Finding{}
	byID := map[string]completeContext{}
	for _, root := range roots {
		current, currentFindings := services.completeContext(ctx, root, store, maxAge)
		findings = append(findings, currentFindings...)
		if current.repositoryID != "" {
			if _, duplicate := byID[current.repositoryID]; duplicate {
				return domain.NewEnvelope("gds complete plan", domain.ExitConflict, nil, domain.Finding{
					Code: "GDS_COMPLETE_IDENTITY_CONFLICT", Severity: domain.SeverityHigh,
					Message: "Selected checkouts contain the same stable repository identity more than once.",
				})
			}
			byID[current.repositoryID] = current
		}
		contexts = append(contexts, current)
	}
	for _, current := range contexts {
		if !current.assessment.Eligible {
			return domain.NewEnvelope("gds complete plan", domain.ExitNotProven,
				CompletePlanData{
					StatePath: statePath, TaskID: options.TaskID,
					Assessments: completeAssessments(contexts),
				}, findings...,
			)
		}
	}
	ordered, graphFindings := services.orderCompletionGraph(ctx, contexts, byID)
	findings = append(findings, graphFindings...)
	if len(graphFindings) != 0 {
		return domain.NewEnvelope("gds complete plan", domain.ExitValidation,
			CompletePlanData{
				StatePath: statePath, TaskID: options.TaskID,
				Assessments: completeAssessments(contexts),
			}, findings...,
		)
	}
	preconditions := make([]operations.Precondition, 0, len(ordered))
	steps := make([]operations.Step, 0, len(ordered))
	rootsByID := make(map[string]string, len(ordered))
	for order, current := range ordered {
		current.assessment.DependencyOrder = order
		for index := range contexts {
			if contexts[index].repositoryID == current.repositoryID {
				contexts[index].assessment.DependencyOrder = order
			}
		}
		rootsByID[current.repositoryID] = current.root
		observation := current.observation
		preconditions = append(preconditions, operations.Precondition{
			RepositoryID: observation.RepositoryID, HeadOID: observation.HeadOID,
			WorktreeFingerprint: observation.WorktreeFingerprint,
			UpstreamOID:         observation.UpstreamOID, RemoteDefaultOID: observation.RemoteDefaultOID,
			RemoteEvidenceDigest: observation.RemoteEvidenceDigest,
			ManifestDigest:       observation.ManifestDigest, PolicyDigest: observation.PolicyDigest,
		})
		checks := make([]map[string]any, 0, len(current.assessment.RequiredChecks))
		for _, check := range current.assessment.RequiredChecks {
			checks = append(checks, map[string]any{
				"name": check.Name, "status": check.Status, "commands": check.Commands,
			})
		}
		steps = append(steps, operations.Step{
			StepID:       fmt.Sprintf("complete-repository-%03d", order+1),
			RepositoryID: current.repositoryID, Action: gitops.CompleteTaskBranchAction,
			RequiresApproval: true, Compensation: operations.Compensation{Mode: "manual"},
			Parameters: map[string]any{"completion": map[string]any{
				"worktree_root":           current.root,
				"default_branch_ref":      current.assessment.DefaultBranchRef,
				"task_branch_ref":         current.assessment.TaskBranchRef,
				"expected_default_oid":    current.assessment.ExpectedDefaultOID,
				"expected_task_oid":       current.assessment.ExpectedTaskOID,
				"integration_policy":      current.assessment.IntegrationPolicy,
				"required_checks":         checks,
				"remote_evidence_digest":  observation.RemoteEvidenceDigest,
				"refresh_max_age_seconds": int64(maxAge / time.Second),
				"dependency_order":        order,
			}},
		})
	}
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError("gds complete plan", err)
	}
	plan, err := operations.NewPlan(planID, now, now.Add(syncPlanLifetime), operations.PlanInput{
		Operation: "complete-work", Actor: operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		TaskID: options.TaskID, Preconditions: preconditions, Steps: steps,
		ApprovalClass: "integrate-publish-clean-completed-work",
	})
	if err != nil {
		return domain.InternalError("gds complete plan", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		completeObserver{services: services, store: store, roots: rootsByID, maxAge: maxAge},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope("gds complete plan", err)
	}
	envelope := domain.Success("gds complete plan", CompletePlanData{
		Plan: &plan, StatePath: statePath, TaskID: options.TaskID,
		Assessments: completeAssessments(contexts),
	}, findings...)
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) ApplyComplete(
	ctx context.Context,
	planID string,
	options CompleteOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return syncIdentifierRequired("gds complete apply", "plan", "--apply")
	}
	if finding := validateOperationActor(options.DeviceID, options.SessionID); finding != nil {
		return domain.NewEnvelope("gds complete apply", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds complete apply", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	plan, roots, maxAge, err := loadCompletePlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return completePlanInvalid("gds complete apply")
	}
	for _, root := range roots {
		if err := services.GitMutations.LocalPushSupported(ctx, root); err != nil {
			class := domain.ExitValidation
			code := "GDS_COMPLETE_LOCAL_PROVIDER_NOT_PROVEN"
			if errors.Is(err, gitprovider.ErrNetworkMutationDisabled) {
				class = domain.ExitUnsupported
				code = "GDS_COMPLETE_LIVE_INTEGRATION_DISABLED"
			}
			return domain.NewEnvelope("gds complete apply", class, nil, domain.Finding{
				Code: code, Severity: domain.SeverityHigh,
				Message: "Completion apply is blocked before integration because the remote is outside the verified local provider boundary.",
			})
		}
	}
	handler, err := gitops.NewCompleteTaskBranchHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError("gds complete apply", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		completeObserver{services: services, store: store, roots: roots, maxAge: maxAge},
		map[string]operations.ActionHandler{gitops.CompleteTaskBranchAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, plan.PlanID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope("gds complete apply", err)
		envelope.Data = result
		envelope.OperationID = result.OperationID
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		return envelope
	}
	envelope := domain.Success("gds complete apply", result)
	envelope.OperationID = result.OperationID
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) VerifyComplete(
	ctx context.Context,
	operationID string,
	options CompleteOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return syncIdentifierRequired("gds complete verify", "operation", "--verify")
	}
	if finding := validateOperationActor(options.DeviceID, options.SessionID); finding != nil {
		return domain.NewEnvelope("gds complete verify", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds complete verify", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	operation, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return domain.NewEnvelope("gds complete verify", domain.ExitNotProven, nil, domain.Finding{
			Code: "GDS_COMPLETE_OPERATION_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "The selected completion operation is unavailable.",
		})
	}
	plan, roots, _, err := loadCompletePlan(ctx, store, operation.PlanID, services.Schemas)
	if err != nil {
		return completePlanInvalid("gds complete verify")
	}
	handler, err := gitops.NewCompleteTaskBranchHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError("gds complete verify", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		completeObserver{services: services, store: store, roots: roots, maxAge: maximumRefreshMaxAge},
		map[string]operations.ActionHandler{gitops.CompleteTaskBranchAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope("gds complete verify", err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success("gds complete verify", result)
	envelope.OperationID = operationID
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) completeContext(
	ctx context.Context,
	path string,
	store *state.Store,
	maxAge time.Duration,
) (completeContext, []domain.Finding) {
	assessment := CompleteAssessment{
		WorktreeRoot: path, Reason: "not-proven", RequiredChecks: []HandoffCheck{},
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return completeContext{assessment: assessment}, []domain.Finding{completeFinding(path, "git-boundary-not-proven")}
	}
	assessment.WorktreeRoot = info.WorktreeRoot
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil {
		return completeContext{root: info.WorktreeRoot, assessment: assessment}, []domain.Finding{completeFinding(info.WorktreeRoot, "git-state-not-proven")}
	}
	assessment.Status = status
	estateRoot, anchor, policyFindings := services.policyInputs(ctx, info.WorktreeRoot)
	if len(policyFindings) != 0 {
		assessment.Reason = "policy-not-proven"
		return completeContext{root: info.WorktreeRoot, assessment: assessment}, policyFindings
	}
	assessment.RepositoryID = anchor.Repository.ID
	assessment.IntegrationPolicy = anchor.Git.Integration
	assessment.RequiredChecks = requiredHandoffChecks(anchor.Verification)
	current := completeContext{
		root: info.WorktreeRoot, repositoryID: anchor.Repository.ID,
		anchor: anchor, assessment: assessment,
	}
	if status.Head.Mode != "branch" || status.Head.OID == "" || status.Branch.Name == "" {
		current.assessment.Reason = "attached-task-branch-required"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	if status.Branch.Name == anchor.Git.DefaultBranch {
		current.assessment.Reason = "task-branch-required"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	if !checkoutStatusIsClean(status) {
		current.assessment.Reason = "clean-checkout-required"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	if status.Branch.UpstreamState != "present" ||
		status.Branch.Upstream != "origin/"+status.Branch.Name {
		current.assessment.Reason = "published-task-upstream-required"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	if len(current.assessment.RequiredChecks) != 0 {
		current.assessment.Reason = "required-checks-not-proven"
		findings := []domain.Finding{}
		for _, check := range current.assessment.RequiredChecks {
			findings = append(findings, domain.Finding{
				Code: "GDS_COMPLETE_CHECK_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  "Completion cannot integrate while a required check lacks execution evidence.",
				Evidence: map[string]any{"check": check.Name, "commands": check.Commands},
			})
		}
		return current, findings
	}
	if anchor.Git.Integration != "direct" {
		current.assessment.Reason = "pull-request-provider-unavailable"
		return current, []domain.Finding{{
			Code: "GDS_COMPLETE_PR_INTEGRATION_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Repository policy requires a pull-request provider that is not enabled.",
		}}
	}
	defaultRef := "refs/heads/" + anchor.Git.DefaultBranch
	taskRef := "refs/heads/" + status.Branch.Name
	defaultTracking := "refs/remotes/origin/" + anchor.Git.DefaultBranch
	taskTracking := "refs/remotes/origin/" + status.Branch.Name
	if gitprovider.ValidateFastForwardRefs(defaultRef, defaultTracking) != nil ||
		gitprovider.ValidateFastForwardRefs(taskRef, taskTracking) != nil {
		current.assessment.Reason = "branch-ref-unsupported"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	remoteEvidence, reason := services.durableOriginEvidence(
		ctx, store, anchor.Repository.ID, info.WorktreeRoot, status.Head.OID, maxAge,
	)
	if reason != "" {
		current.assessment.Reason = reason
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, reason)}
	}
	remoteDefault, foundDefault, err := services.GitMutations.ObserveRemoteBranchOptional(
		ctx, info.WorktreeRoot, "origin", defaultRef,
	)
	if err != nil || !foundDefault {
		current.assessment.Reason = "remote-default-not-proven"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	remoteTask, foundTask, err := services.GitMutations.ObserveRemoteBranchOptional(
		ctx, info.WorktreeRoot, "origin", taskRef,
	)
	if err != nil || !foundTask || remoteTask != status.Head.OID {
		current.assessment.Reason = "task-commit-not-published"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	tracking := map[string]string{}
	for _, ref := range remoteEvidence.Refs {
		tracking[ref.Reference] = ref.OID
	}
	if tracking[defaultTracking] != remoteDefault || tracking[taskTracking] != remoteTask {
		current.assessment.Reason = "remote-ref-drift"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	localDefault, foundLocalDefault, err := services.GitMutations.ObserveLocalBranch(
		ctx, info.WorktreeRoot, defaultRef,
	)
	if err != nil || !foundLocalDefault || localDefault != remoteDefault {
		current.assessment.Reason = "local-default-not-current"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	ancestor, err := services.GitMutations.IsAncestor(
		ctx, info.WorktreeRoot, remoteDefault, remoteTask,
	)
	if err != nil || !ancestor || remoteDefault == remoteTask {
		current.assessment.Reason = "task-not-fast-forward"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	for _, worktree := range status.Worktrees {
		if worktree.Path == info.WorktreeRoot {
			continue
		}
		if worktree.Branch == anchor.Git.DefaultBranch || worktree.Branch == status.Branch.Name {
			current.assessment.Reason = "active-worktree-blocks-cleanup"
			return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
		}
	}
	applySupported := true
	if err := services.GitMutations.LocalPushSupported(ctx, info.WorktreeRoot); err != nil {
		applySupported = false
		if !errors.Is(err, gitprovider.ErrNetworkMutationDisabled) {
			current.assessment.Reason = "integration-provider-not-proven"
			return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
		}
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Head       gitprovider.HeadState      `json:"head"`
		Branch     gitprovider.BranchState    `json:"branch"`
		Changes    gitprovider.ChangeState    `json:"changes"`
		Submodules gitprovider.SubmoduleState `json:"submodules"`
		DefaultOID string                     `json:"default_oid"`
		TaskOID    string                     `json:"task_oid"`
	}{status.Head, status.Branch, status.Changes, status.Submodules, remoteDefault, remoteTask})
	if err != nil {
		current.assessment.Reason = "worktree-fingerprint-not-proven"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	manifestDigest, err := fileDigest(filepath.Join(info.WorktreeRoot, ".gds", "repository.yaml"))
	if err != nil {
		current.assessment.Reason = "manifest-not-proven"
		return current, []domain.Finding{completeFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	compiled := services.Compiler.CompileDirectory(
		estateRoot, anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		current.assessment.Reason = "policy-not-proven"
		return current, compiled.Findings
	}
	current.assessment.Eligible = true
	current.assessment.ApplySupported = applySupported
	current.assessment.Reason = "integrate-publish-clean"
	current.assessment.DefaultBranchRef = defaultRef
	current.assessment.TaskBranchRef = taskRef
	current.assessment.ExpectedDefaultOID = remoteDefault
	current.assessment.ExpectedTaskOID = remoteTask
	current.observation = operations.Observation{
		RepositoryID: anchor.Repository.ID, HeadOID: status.Head.OID,
		WorktreeFingerprint: fingerprint, UpstreamOID: remoteTask,
		RemoteDefaultOID: remoteDefault, RemoteEvidenceDigest: remoteEvidence.EvidenceDigest,
		ManifestDigest: manifestDigest, PolicyDigest: compiled.Document.CompiledPolicy.Digest,
	}
	findings := []domain.Finding{}
	if !applySupported {
		findings = append(findings, domain.Finding{
			Code: "GDS_COMPLETE_LIVE_INTEGRATION_DISABLED", Severity: domain.SeverityInfo,
			Message: "The completion plan is inspectable, but no verified live integration provider is enabled.",
		})
	}
	return current, findings
}

func (services *Services) orderCompletionGraph(
	ctx context.Context,
	contexts []completeContext,
	byID map[string]completeContext,
) ([]completeContext, []domain.Finding) {
	edges := map[string]map[string]struct{}{}
	indegree := map[string]int{}
	findings := []domain.Finding{}
	for repositoryID := range byID {
		edges[repositoryID] = map[string]struct{}{}
		indegree[repositoryID] = 0
	}
	for _, consumer := range contexts {
		for _, relationship := range consumer.anchor.Relationships {
			dependency, selected := byID[relationship.Target]
			if !selected || (relationship.Type != "git-submodule-consumer" &&
				relationship.Type != "package-consumer") {
				continue
			}
			if relationship.Type == "package-consumer" {
				findings = append(findings, domain.Finding{
					Code: "GDS_COMPLETE_PACKAGE_FINALIZATION_NOT_PROVEN", Severity: domain.SeverityHigh,
					Message:  "Selected package dependency finalization requires a verified release contract.",
					Evidence: map[string]any{"consumer": consumer.repositoryID, "dependency": dependency.repositoryID},
				})
				continue
			}
			topology, err := services.Git.InspectTopology(ctx, consumer.root)
			if err != nil {
				findings = append(findings, domain.Finding{
					Code: "GDS_COMPLETE_GITLINK_NOT_PROVEN", Severity: domain.SeverityHigh,
					Message:  "Consumer gitlink topology could not be inspected.",
					Evidence: map[string]any{"consumer": consumer.repositoryID},
				})
				continue
			}
			matched := false
			for _, submodule := range topology.Submodules {
				if submodule.Name == relationship.GitmodulesName &&
					submodule.GitlinkOID == dependency.assessment.ExpectedTaskOID &&
					(submodule.WorktreeState == "at-gitlink" || submodule.WorktreeState == "uninitialized") {
					matched = true
					break
				}
			}
			if !matched {
				findings = append(findings, domain.Finding{
					Code: "GDS_COMPLETE_GITLINK_FINAL_PIN_REQUIRED", Severity: domain.SeverityHigh,
					Message:  "Consumer task branch does not pin the selected module's final task commit.",
					Evidence: map[string]any{"consumer": consumer.repositoryID, "module": dependency.repositoryID},
				})
				continue
			}
			if _, exists := edges[dependency.repositoryID][consumer.repositoryID]; !exists {
				edges[dependency.repositoryID][consumer.repositoryID] = struct{}{}
				indegree[consumer.repositoryID]++
			}
		}
	}
	if len(findings) != 0 {
		return nil, findings
	}
	ready := []string{}
	for repositoryID, degree := range indegree {
		if degree == 0 {
			ready = append(ready, repositoryID)
		}
	}
	sort.Strings(ready)
	ordered := make([]completeContext, 0, len(contexts))
	for len(ready) != 0 {
		repositoryID := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[repositoryID])
		consumers := make([]string, 0, len(edges[repositoryID]))
		for consumer := range edges[repositoryID] {
			consumers = append(consumers, consumer)
		}
		sort.Strings(consumers)
		for _, consumer := range consumers {
			indegree[consumer]--
			if indegree[consumer] == 0 {
				ready = append(ready, consumer)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(contexts) {
		return nil, []domain.Finding{{
			Code: "GDS_COMPLETE_DEPENDENCY_CYCLE", Severity: domain.SeverityHigh,
			Message: "Selected repository dependencies contain a completion cycle.",
		}}
	}
	return ordered, nil
}

func completeAssessments(contexts []completeContext) []CompleteAssessment {
	assessments := make([]CompleteAssessment, 0, len(contexts))
	for _, current := range contexts {
		assessments = append(assessments, current.assessment)
	}
	sort.Slice(assessments, func(left, right int) bool {
		return assessments[left].RepositoryID < assessments[right].RepositoryID
	})
	return assessments
}

func checkoutStatusIsClean(status gitprovider.Status) bool {
	return status.Changes.Staged == 0 && status.Changes.Unstaged == 0 &&
		status.Changes.Untracked == 0 && status.Changes.Conflicted == 0 &&
		status.Changes.SubmoduleChanges == 0 && status.Submodules.Modified == 0 &&
		status.Submodules.Conflicted == 0
}

func completeFinding(path string, reason string) domain.Finding {
	return domain.Finding{
		Code: "GDS_COMPLETE_NOT_PROVEN", Severity: domain.SeverityHigh,
		Message:  "The selected checkout is not eligible for complete-work integration.",
		Evidence: map[string]any{"path": path, "reason": reason},
	}
}

func loadCompletePlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, map[string]string, time.Duration, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, nil, 0, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != "complete-work" || plan.PlanDigest != record.PlanDigest ||
		len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, nil, 0, errors.New("stored plan is not a valid completion plan")
	}
	roots := make(map[string]string, len(plan.Steps))
	var maxAge time.Duration
	lastOrder := -1
	for _, step := range plan.Steps {
		if step.Action != gitops.CompleteTaskBranchAction {
			return operations.Plan{}, nil, 0, errors.New("completion plan has another action")
		}
		raw, ok := step.Parameters["completion"].(map[string]any)
		if !ok {
			return operations.Plan{}, nil, 0, errors.New("completion parameters are missing")
		}
		root, _ := raw["worktree_root"].(string)
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return operations.Plan{}, nil, 0, errors.New("completion root is invalid")
		}
		seconds, secondsOK := raw["refresh_max_age_seconds"].(float64)
		order, orderOK := raw["dependency_order"].(float64)
		if !secondsOK || seconds < 1 || seconds > 3600 || seconds != float64(int64(seconds)) ||
			!orderOK || int(order) != lastOrder+1 {
			return operations.Plan{}, nil, 0, errors.New("completion order or refresh age is invalid")
		}
		lastOrder = int(order)
		stepMaxAge := time.Duration(int64(seconds)) * time.Second
		if maxAge != 0 && maxAge != stepMaxAge {
			return operations.Plan{}, nil, 0, errors.New("completion refresh ages differ")
		}
		maxAge = stepMaxAge
		roots[step.RepositoryID] = root
	}
	return plan, roots, maxAge, nil
}

func completePlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_COMPLETE_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable complete-work plan.",
	})
}
