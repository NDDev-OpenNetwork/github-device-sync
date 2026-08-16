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

const (
	syncPlanLifetime     = 15 * time.Minute
	defaultRefreshMaxAge = 15 * time.Minute
	maximumRefreshMaxAge = time.Hour
)

type SyncCheckoutOptions struct {
	StatePath         string
	DeviceID          string
	SessionID         string
	ApprovalReference string
	Checkouts         []string
	RefreshMaxAge     time.Duration
}

type SyncCheckoutAssessment struct {
	RepositoryID  string             `json:"repository_id,omitempty"`
	WorktreeRoot  string             `json:"worktree_root"`
	Eligible      bool               `json:"eligible"`
	Reason        string             `json:"reason"`
	Status        gitprovider.Status `json:"status"`
	RefreshDigest string             `json:"refresh_digest,omitempty"`
}

type SyncCheckoutPlanData struct {
	Plan        *operations.Plan         `json:"plan,omitempty"`
	StatePath   string                   `json:"state_path"`
	Assessments []SyncCheckoutAssessment `json:"assessments"`
}

type syncCheckoutContext struct {
	root         string
	repositoryID string
	branchRef    string
	upstreamRef  string
	targetOID    string
	observation  operations.Observation
	assessment   SyncCheckoutAssessment
}

type syncObserver struct {
	services *Services
	store    *state.Store
	roots    map[string]string
	maxAge   time.Duration
}

func (observer syncObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	root, found := observer.roots[repositoryID]
	if !found {
		return operations.Observation{}, errors.New("sync repository is outside the stored plan")
	}
	current, findings := observer.services.syncCheckoutContext(
		ctx, root, observer.store, observer.maxAge,
	)
	if len(findings) != 0 || !current.assessment.Eligible || current.repositoryID != repositoryID {
		return operations.Observation{}, errors.New("sync checkout is no longer eligible")
	}
	return current.observation, nil
}

func (services *Services) PlanSyncCheckouts(
	ctx context.Context,
	path string,
	options SyncCheckoutOptions,
) domain.Envelope {
	if finding := validateOperationActor(options.DeviceID, options.SessionID); finding != nil {
		return domain.NewEnvelope("gds sync checkouts plan", domain.ExitInput, nil, *finding)
	}
	maxAge, finding := validateRefreshMaxAge(options.RefreshMaxAge)
	if finding != nil {
		return domain.NewEnvelope("gds sync checkouts plan", domain.ExitInput, nil, *finding)
	}
	selected := append([]string(nil), options.Checkouts...)
	if len(selected) == 0 {
		selected = []string{path}
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds sync checkouts plan", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	roots, rootFindings := services.selectedCheckoutRoots(ctx, selected)
	if len(rootFindings) != 0 {
		return domain.NewEnvelope(
			"gds sync checkouts plan", classifyFindings(rootFindings),
			SyncCheckoutPlanData{StatePath: statePath, Assessments: []SyncCheckoutAssessment{}},
			rootFindings...,
		)
	}
	contexts := []syncCheckoutContext{}
	assessments := make([]SyncCheckoutAssessment, 0, len(roots))
	findings := []domain.Finding{}
	identities := map[string]string{}
	for _, root := range roots {
		current, currentFindings := services.syncCheckoutContext(ctx, root, store, maxAge)
		assessments = append(assessments, current.assessment)
		findings = append(findings, currentFindings...)
		if !current.assessment.Eligible {
			continue
		}
		if previous, found := identities[current.repositoryID]; found {
			return domain.NewEnvelope(
				"gds sync checkouts plan", domain.ExitConflict,
				SyncCheckoutPlanData{StatePath: statePath, Assessments: assessments},
				domain.Finding{
					Code: "GDS_SYNC_IDENTITY_CONFLICT", Severity: domain.SeverityHigh,
					Message: "Two selected worktrees resolve to the same repository identity; no plan was stored.",
					Evidence: map[string]any{
						"repository_id": current.repositoryID, "first": previous, "second": root,
					},
				},
			)
		}
		identities[current.repositoryID] = root
		contexts = append(contexts, current)
	}
	if len(contexts) == 0 {
		findings = append(findings, domain.Finding{
			Code: "GDS_SYNC_NO_ELIGIBLE_CHECKOUT", Severity: domain.SeverityInfo,
			Message: "No selected checkout is eligible for a fast-forward plan; all local states were preserved.",
		})
		return domain.NewEnvelope(
			"gds sync checkouts plan", domain.ExitNotProven,
			SyncCheckoutPlanData{StatePath: statePath, Assessments: assessments}, findings...,
		)
	}
	sort.Slice(contexts, func(left, right int) bool {
		return contexts[left].repositoryID < contexts[right].repositoryID
	})
	preconditions := make([]operations.Precondition, 0, len(contexts))
	steps := make([]operations.Step, 0, len(contexts))
	for index, current := range contexts {
		observation := current.observation
		preconditions = append(preconditions, operations.Precondition{
			RepositoryID: observation.RepositoryID, HeadOID: observation.HeadOID,
			WorktreeFingerprint: observation.WorktreeFingerprint,
			UpstreamOID:         observation.UpstreamOID, RemoteDefaultOID: observation.RemoteDefaultOID,
			RemoteEvidenceDigest: observation.RemoteEvidenceDigest,
			ManifestDigest:       observation.ManifestDigest, PolicyDigest: observation.PolicyDigest,
		})
		steps = append(steps, operations.Step{
			StepID:       fmt.Sprintf("sync-checkout-%03d", index+1),
			RepositoryID: current.repositoryID, Action: gitops.FastForwardCheckoutAction,
			RequiresApproval: true, Compensation: operations.Compensation{Mode: "manual"},
			Parameters: map[string]any{"checkout_sync": map[string]any{
				"worktree_root": current.root, "branch_ref": current.branchRef,
				"upstream_ref":      current.upstreamRef,
				"expected_head_oid": observation.HeadOID, "target_oid": current.targetOID,
				"remote_evidence_digest":  observation.RemoteEvidenceDigest,
				"refresh_max_age_seconds": int64(maxAge / time.Second),
			}},
		})
	}
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError("gds sync checkouts plan", err)
	}
	plan, err := operations.NewPlan(planID, now, now.Add(syncPlanLifetime), operations.PlanInput{
		Operation:     "sync-checkouts",
		Actor:         operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: preconditions, Steps: steps,
		ApprovalClass: "local-checkout-fast-forward",
	})
	if err != nil {
		return domain.InternalError("gds sync checkouts plan", err)
	}
	observer := syncObserver{
		services: services, store: store, roots: identities, maxAge: maxAge,
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, observer, nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope("gds sync checkouts plan", err)
	}
	envelope := domain.Success(
		"gds sync checkouts plan",
		SyncCheckoutPlanData{Plan: &plan, StatePath: statePath, Assessments: assessments},
		findings...,
	)
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) ApplySyncCheckouts(
	ctx context.Context,
	planID string,
	options SyncCheckoutOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return syncIdentifierRequired("gds sync checkouts apply", "plan", "--apply")
	}
	if finding := validateOperationActor(options.DeviceID, options.SessionID); finding != nil {
		return domain.NewEnvelope("gds sync checkouts apply", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds sync checkouts apply", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	plan, roots, maxAge, err := loadSyncPlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return domain.NewEnvelope("gds sync checkouts apply", domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_SYNC_PLAN_INVALID", Severity: domain.SeverityHigh,
			Message: "The selected plan is not a valid immutable checkout synchronization plan.",
		})
	}
	handler, err := gitops.NewFastForwardCheckoutHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError("gds sync checkouts apply", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		syncObserver{services: services, store: store, roots: roots, maxAge: maxAge},
		map[string]operations.ActionHandler{gitops.FastForwardCheckoutAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, plan.PlanID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope("gds sync checkouts apply", err)
		envelope.Data = result
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		envelope.OperationID = result.OperationID
		return envelope
	}
	envelope := domain.Success("gds sync checkouts apply", result)
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.OperationID = result.OperationID
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) VerifySyncCheckouts(
	ctx context.Context,
	operationID string,
	options SyncCheckoutOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return syncIdentifierRequired("gds sync checkouts verify", "operation", "--verify")
	}
	if finding := validateOperationActor(options.DeviceID, options.SessionID); finding != nil {
		return domain.NewEnvelope("gds sync checkouts verify", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds sync checkouts verify", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	operation, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return domain.NewEnvelope("gds sync checkouts verify", domain.ExitNotProven, nil, domain.Finding{
			Code: "GDS_SYNC_OPERATION_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "The selected synchronization operation is unavailable.",
		})
	}
	plan, roots, _, err := loadSyncPlan(ctx, store, operation.PlanID, services.Schemas)
	if err != nil {
		return domain.NewEnvelope("gds sync checkouts verify", domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_SYNC_PLAN_INVALID", Severity: domain.SeverityHigh,
			Message: "The operation does not reference a valid checkout synchronization plan.",
		})
	}
	handler, err := gitops.NewFastForwardCheckoutHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError("gds sync checkouts verify", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		syncObserver{services: services, store: store, roots: roots, maxAge: maximumRefreshMaxAge},
		map[string]operations.ActionHandler{gitops.FastForwardCheckoutAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope("gds sync checkouts verify", err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success("gds sync checkouts verify", result)
	envelope.OperationID = operationID
	envelope.Scope["repositories"] = plan.Scope.Repositories
	return envelope
}

func (services *Services) syncCheckoutContext(
	ctx context.Context,
	path string,
	store *state.Store,
	maxAge time.Duration,
) (syncCheckoutContext, []domain.Finding) {
	assessment := SyncCheckoutAssessment{WorktreeRoot: path, Reason: "not-proven"}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return syncCheckoutContext{assessment: assessment}, []domain.Finding{syncSkipFinding(path, "git-boundary-not-proven")}
	}
	assessment.WorktreeRoot = info.WorktreeRoot
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil {
		return syncCheckoutContext{root: info.WorktreeRoot, assessment: assessment}, []domain.Finding{syncSkipFinding(info.WorktreeRoot, "git-state-not-proven")}
	}
	assessment.Status = status
	estateRoot, anchor, policyFindings := services.policyInputs(ctx, info.WorktreeRoot)
	if len(policyFindings) != 0 {
		assessment.Reason = "policy-not-proven"
		return syncCheckoutContext{root: info.WorktreeRoot, assessment: assessment}, policyFindings
	}
	assessment.RepositoryID = anchor.Repository.ID
	current := syncCheckoutContext{
		root: info.WorktreeRoot, repositoryID: anchor.Repository.ID,
		assessment: assessment,
	}
	reason := syncStatusIneligibleReason(status)
	if reason != "" {
		current.assessment.Reason = reason
		return current, []domain.Finding{syncSkipFinding(info.WorktreeRoot, reason)}
	}
	branchRef := "refs/heads/" + status.Branch.Name
	upstreamRef := "refs/remotes/" + status.Branch.Upstream
	if err := gitprovider.ValidateFastForwardRefs(branchRef, upstreamRef); err != nil {
		current.assessment.Reason = "branch-ref-unsupported"
		return current, []domain.Finding{syncSkipFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	remoteEvidence, reason := services.durableOriginEvidence(
		ctx, store, anchor.Repository.ID, info.WorktreeRoot, status.Head.OID, maxAge,
	)
	if reason != "" {
		current.assessment.Reason = reason
		return current, []domain.Finding{syncSkipFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	targetOID := ""
	for _, ref := range remoteEvidence.Refs {
		if ref.Reference == upstreamRef {
			targetOID = ref.OID
			break
		}
	}
	if targetOID == "" {
		current.assessment.Reason = "upstream-ref-missing"
		return current, []domain.Finding{syncSkipFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	remoteBranchRef := "refs/heads/" + strings.TrimPrefix(upstreamRef, "refs/remotes/origin/")
	remoteOID, err := services.GitMutations.ObserveRemoteBranch(
		ctx, info.WorktreeRoot, "origin", remoteBranchRef,
	)
	if err != nil {
		current.assessment.Reason = "remote-state-not-proven"
		return current, []domain.Finding{syncSkipFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	if remoteOID != targetOID {
		current.assessment.Reason = "remote-advanced-after-refresh"
		return current, []domain.Finding{syncSkipFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	evidenceDigest := remoteEvidence.EvidenceDigest
	fingerprint, err := canonicaljson.Digest(struct {
		Head       gitprovider.HeadState      `json:"head"`
		Branch     gitprovider.BranchState    `json:"branch"`
		Changes    gitprovider.ChangeState    `json:"changes"`
		Submodules gitprovider.SubmoduleState `json:"submodules"`
	}{status.Head, status.Branch, status.Changes, status.Submodules})
	if err != nil {
		current.assessment.Reason = "worktree-fingerprint-not-proven"
		return current, []domain.Finding{syncSkipFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	manifestDigest, err := fileDigest(filepath.Join(info.WorktreeRoot, ".gds", "repository.yaml"))
	if err != nil {
		current.assessment.Reason = "manifest-not-proven"
		return current, []domain.Finding{syncSkipFinding(info.WorktreeRoot, current.assessment.Reason)}
	}
	compiled := services.Compiler.CompileDirectory(
		estateRoot, anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		current.assessment.Reason = "policy-not-proven"
		return current, compiled.Findings
	}
	current.branchRef = branchRef
	current.upstreamRef = upstreamRef
	current.targetOID = targetOID
	current.observation = operations.Observation{
		RepositoryID: anchor.Repository.ID, HeadOID: status.Head.OID,
		WorktreeFingerprint: fingerprint, UpstreamOID: targetOID,
		RemoteDefaultOID: remoteOID, RemoteEvidenceDigest: evidenceDigest,
		ManifestDigest: manifestDigest, PolicyDigest: compiled.Document.CompiledPolicy.Digest,
	}
	current.assessment.Eligible = true
	current.assessment.Reason = "fast-forward"
	current.assessment.RefreshDigest = evidenceDigest
	return current, nil
}

func (services *Services) selectedCheckoutRoots(
	ctx context.Context,
	selected []string,
) ([]string, []domain.Finding) {
	seen := map[string]struct{}{}
	roots := []string{}
	findings := []domain.Finding{}
	for _, path := range selected {
		if strings.TrimSpace(path) == "" {
			findings = append(findings, domain.Finding{
				Code: "GDS_SYNC_CHECKOUT_INVALID", Severity: domain.SeverityHigh,
				Message: "An explicitly selected checkout path is empty.",
			})
			continue
		}
		info, err := services.Git.RepositoryInfo(ctx, path)
		if err != nil {
			findings = append(findings, domain.Finding{
				Code: "GDS_SYNC_CHECKOUT_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  "An explicitly selected path is not a proven Git checkout.",
				Evidence: map[string]any{"path": path},
			})
			continue
		}
		root := filepath.Clean(info.WorktreeRoot)
		if _, found := seen[root]; found {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots, findings
}

func syncStatusIneligibleReason(status gitprovider.Status) string {
	if status.Head.Mode == "detached" {
		return "detached"
	}
	if status.Head.Mode == "unborn" {
		return "unborn"
	}
	if status.Head.Mode != "branch" {
		return "head-not-proven"
	}
	if status.Changes.Conflicted != 0 || status.Submodules.Conflicted != 0 {
		return "conflicted"
	}
	if status.Changes.Staged != 0 || status.Changes.Unstaged != 0 ||
		status.Changes.Untracked != 0 || status.Changes.SubmoduleChanges != 0 ||
		status.Submodules.Modified != 0 {
		return "dirty"
	}
	if status.Branch.UpstreamState != "present" ||
		!strings.HasPrefix(status.Branch.Upstream, "origin/") ||
		status.Branch.Upstream == "origin/HEAD" {
		return "no-upstream"
	}
	if status.Branch.Diverged || status.Branch.Ahead > 0 {
		return "diverged"
	}
	if status.Branch.Behind <= 0 {
		return "not-behind"
	}
	return ""
}

func syncSkipFinding(path string, reason string) domain.Finding {
	return domain.Finding{
		Code: "GDS_SYNC_CHECKOUT_SKIPPED", Severity: domain.SeverityInfo,
		Message:  "The selected checkout is not eligible for automatic fast-forward and was preserved.",
		Evidence: map[string]any{"path": path, "reason": reason},
	}
}

func validateRefreshMaxAge(value time.Duration) (time.Duration, *domain.Finding) {
	if value == 0 {
		value = defaultRefreshMaxAge
	}
	if value <= 0 || value > maximumRefreshMaxAge {
		return 0, &domain.Finding{
			Code: "GDS_SYNC_REFRESH_MAX_AGE_INVALID", Severity: domain.SeverityHigh,
			Message: "Refresh evidence maximum age must be greater than zero and no more than one hour.",
		}
	}
	return value, nil
}

func validateOperationActor(deviceID string, sessionID string) *domain.Finding {
	if !identity.Valid("device", deviceID) {
		return &domain.Finding{
			Code: "GDS_DEVICE_ID_INVALID", Severity: domain.SeverityHigh,
			Message: "A canonical device id is required for a local operation.",
		}
	}
	if strings.TrimSpace(sessionID) == "" || len(sessionID) > 512 ||
		strings.ContainsAny(sessionID, "\x00\r\n") {
		return &domain.Finding{
			Code: "GDS_SESSION_ID_INVALID", Severity: domain.SeverityHigh,
			Message: "A non-empty bounded session id is required for a local operation.",
		}
	}
	return nil
}

func loadSyncPlan(
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
	if err != nil || plan.Operation != "sync-checkouts" || plan.PlanDigest != record.PlanDigest {
		return operations.Plan{}, nil, 0, errors.New("stored plan is not a sync-checkouts plan")
	}
	if findings := plan.Validate(schemas); len(findings) != 0 {
		return operations.Plan{}, nil, 0, errors.New("stored sync plan failed schema validation")
	}
	roots := make(map[string]string, len(plan.Steps))
	var maxAge time.Duration
	for _, step := range plan.Steps {
		if step.Action != gitops.FastForwardCheckoutAction {
			return operations.Plan{}, nil, 0, errors.New("sync plan contains another action")
		}
		raw, ok := step.Parameters["checkout_sync"].(map[string]any)
		if !ok {
			return operations.Plan{}, nil, 0, errors.New("sync parameters are missing")
		}
		root, _ := raw["worktree_root"].(string)
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return operations.Plan{}, nil, 0, errors.New("sync worktree root is invalid")
		}
		if _, duplicate := roots[step.RepositoryID]; duplicate {
			return operations.Plan{}, nil, 0, errors.New("sync repository is duplicated")
		}
		seconds, ok := raw["refresh_max_age_seconds"].(float64)
		if !ok || seconds < 1 || seconds > 3600 || seconds != float64(int64(seconds)) {
			return operations.Plan{}, nil, 0, errors.New("sync refresh age is invalid")
		}
		stepMaxAge := time.Duration(int64(seconds)) * time.Second
		if maxAge != 0 && maxAge != stepMaxAge {
			return operations.Plan{}, nil, 0, errors.New("sync refresh ages differ")
		}
		maxAge = stepMaxAge
		roots[step.RepositoryID] = root
	}
	return plan, roots, maxAge, nil
}

func syncIdentifierRequired(command string, kind string, flag string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
		Code:     "GDS_SYNC_" + strings.ToUpper(kind) + "_ID_REQUIRED",
		Severity: domain.SeverityHigh,
		Message:  flag + " requires an exact " + kind + " id.",
	})
}
