package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
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

const maxHandoffFileBytes = 64 << 20

type HandoffOptions struct {
	StatePath         string
	DeviceID          string
	SessionID         string
	ApprovalReference string
	Files             []string
	Message           string
	AuthorName        string
	AuthorEmail       string
	RefreshMaxAge     time.Duration
}

type HandoffFile struct {
	Path          string `json:"path"`
	State         string `json:"state"`
	ContentDigest string `json:"content_digest"`
	StatusDigest  string `json:"status_digest"`
}

type HandoffCheck struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Commands []string `json:"commands"`
}

type HandoffAssessment struct {
	RepositoryID       string             `json:"repository_id,omitempty"`
	WorktreeRoot       string             `json:"worktree_root"`
	Eligible           bool               `json:"eligible"`
	Reason             string             `json:"reason"`
	ApplySupported     bool               `json:"apply_supported"`
	Status             gitprovider.Status `json:"status"`
	BranchRef          string             `json:"branch_ref,omitempty"`
	RemoteRef          string             `json:"remote_ref,omitempty"`
	ExpectedRemoteOID  string             `json:"expected_remote_oid,omitempty"`
	Files              []HandoffFile      `json:"files"`
	RequiredChecks     []HandoffCheck     `json:"required_checks"`
	DraftPRPolicy      string             `json:"draft_pr_policy,omitempty"`
	RemoteEvidenceHash string             `json:"remote_evidence_digest,omitempty"`
}

type HandoffPlanData struct {
	Plan       *operations.Plan  `json:"plan,omitempty"`
	StatePath  string            `json:"state_path"`
	Assessment HandoffAssessment `json:"assessment"`
}

type handoffContext struct {
	root         string
	repositoryID string
	observation  operations.Observation
	assessment   HandoffAssessment
}

type handoffObserver struct {
	services *Services
	store    *state.Store
	root     string
	files    []string
	maxAge   time.Duration
}

func (observer handoffObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, _ := observer.services.handoffContext(
		ctx, observer.root, observer.files, observer.store, observer.maxAge,
	)
	if !current.assessment.Eligible || current.repositoryID != repositoryID {
		return operations.Observation{}, errors.New("handoff checkout is no longer eligible")
	}
	return current.observation, nil
}

func (services *Services) PlanHandoff(
	ctx context.Context,
	path string,
	options HandoffOptions,
) domain.Envelope {
	if finding := validateOperationActor(options.DeviceID, options.SessionID); finding != nil {
		return domain.NewEnvelope("gds handoff plan", domain.ExitInput, nil, *finding)
	}
	maxAge, finding := validateRefreshMaxAge(options.RefreshMaxAge)
	if finding != nil {
		return domain.NewEnvelope("gds handoff plan", domain.ExitInput, nil, *finding)
	}
	if finding := validateHandoffRequest(options); finding != nil {
		return domain.NewEnvelope("gds handoff plan", domain.ExitInput, nil, *finding)
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds handoff plan", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	current, findings := services.handoffContext(ctx, path, options.Files, store, maxAge)
	data := HandoffPlanData{StatePath: statePath, Assessment: current.assessment}
	if !current.assessment.Eligible {
		return domain.NewEnvelope("gds handoff plan", domain.ExitNotProven, data, findings...)
	}
	now := services.Now().UTC().Truncate(time.Second)
	fileParameters := make([]map[string]any, 0, len(current.assessment.Files))
	for _, file := range current.assessment.Files {
		fileParameters = append(fileParameters, map[string]any{
			"path": file.Path, "state": file.State,
			"content_digest": file.ContentDigest, "status_digest": file.StatusDigest,
		})
	}
	checkParameters := make([]map[string]any, 0, len(current.assessment.RequiredChecks))
	for _, check := range current.assessment.RequiredChecks {
		checkParameters = append(checkParameters, map[string]any{
			"name": check.Name, "status": check.Status, "commands": check.Commands,
		})
	}
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError("gds handoff plan", err)
	}
	plan, err := operations.NewPlan(planID, now, now.Add(syncPlanLifetime), operations.PlanInput{
		Operation: "handoff-work",
		Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
		Preconditions: []operations.Precondition{{
			RepositoryID:         current.observation.RepositoryID,
			HeadOID:              current.observation.HeadOID,
			WorktreeFingerprint:  current.observation.WorktreeFingerprint,
			UpstreamOID:          current.observation.UpstreamOID,
			RemoteDefaultOID:     current.observation.RemoteDefaultOID,
			RemoteEvidenceDigest: current.observation.RemoteEvidenceDigest,
			ManifestDigest:       current.observation.ManifestDigest,
			PolicyDigest:         current.observation.PolicyDigest,
		}},
		Steps: []operations.Step{{
			StepID: "checkpoint-handoff", RepositoryID: current.repositoryID,
			Action: gitops.CheckpointHandoffAction, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "manual"},
			Parameters: map[string]any{"handoff": map[string]any{
				"worktree_root":       current.root,
				"branch_ref":          current.assessment.BranchRef,
				"remote_ref":          current.assessment.RemoteRef,
				"expected_head_oid":   current.observation.HeadOID,
				"expected_remote_oid": current.assessment.ExpectedRemoteOID,
				"files":               fileParameters, "message": options.Message,
				"author":                  map[string]any{"name": options.AuthorName, "email": options.AuthorEmail},
				"commit_time":             now.Format(time.RFC3339),
				"required_checks":         checkParameters,
				"draft_pr_policy":         current.assessment.DraftPRPolicy,
				"remote_evidence_digest":  current.observation.RemoteEvidenceDigest,
				"refresh_max_age_seconds": int64(maxAge / time.Second),
			}},
		}},
		ApprovalClass: "commit-push-unfinished-work",
	})
	if err != nil {
		return domain.InternalError("gds handoff plan", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		handoffObserver{services: services, store: store, root: current.root, files: options.Files, maxAge: maxAge},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope("gds handoff plan", err)
	}
	data.Plan = &plan
	envelope := domain.Success("gds handoff plan", data, findings...)
	envelope.Scope["repository_id"] = current.repositoryID
	return envelope
}

func (services *Services) ApplyHandoff(
	ctx context.Context,
	planID string,
	options HandoffOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return syncIdentifierRequired("gds handoff apply", "plan", "--apply")
	}
	if finding := validateOperationActor(options.DeviceID, options.SessionID); finding != nil {
		return domain.NewEnvelope("gds handoff apply", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds handoff apply", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	plan, root, files, maxAge, err := loadHandoffPlan(ctx, store, planID, services.Schemas)
	if err != nil {
		return handoffPlanInvalid("gds handoff apply")
	}
	if err := services.GitMutations.LocalPushSupported(ctx, root); err != nil {
		class := domain.ExitValidation
		code := "GDS_HANDOFF_LOCAL_PROVIDER_NOT_PROVEN"
		if errors.Is(err, gitprovider.ErrNetworkMutationDisabled) {
			class = domain.ExitUnsupported
			code = "GDS_HANDOFF_LIVE_PUSH_DISABLED"
		}
		return domain.NewEnvelope("gds handoff apply", class, nil, domain.Finding{
			Code: code, Severity: domain.SeverityHigh,
			Message: "Handoff apply is blocked before commit because this remote is outside the isolated C4 provider boundary.",
		})
	}
	handler, err := gitops.NewHandoffHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError("gds handoff apply", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		handoffObserver{services: services, store: store, root: root, files: files, maxAge: maxAge},
		map[string]operations.ActionHandler{gitops.CheckpointHandoffAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, plan.PlanID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope("gds handoff apply", err)
		envelope.Data = result
		envelope.OperationID = result.OperationID
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		return envelope
	}
	envelope := domain.Success("gds handoff apply", result)
	envelope.OperationID = result.OperationID
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.Scope["repository_id"] = plan.Scope.Repositories[0]
	return envelope
}

func (services *Services) VerifyHandoff(
	ctx context.Context,
	operationID string,
	options HandoffOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return syncIdentifierRequired("gds handoff verify", "operation", "--verify")
	}
	if finding := validateOperationActor(options.DeviceID, options.SessionID); finding != nil {
		return domain.NewEnvelope("gds handoff verify", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds handoff verify", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	operation, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return domain.NewEnvelope("gds handoff verify", domain.ExitNotProven, nil, domain.Finding{
			Code: "GDS_HANDOFF_OPERATION_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "The selected handoff operation is unavailable.",
		})
	}
	plan, root, files, _, err := loadHandoffPlan(ctx, store, operation.PlanID, services.Schemas)
	if err != nil {
		return handoffPlanInvalid("gds handoff verify")
	}
	handler, err := gitops.NewHandoffHandler(services.GitMutations)
	if err != nil {
		return domain.InternalError("gds handoff verify", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		handoffObserver{services: services, store: store, root: root, files: files, maxAge: maximumRefreshMaxAge},
		map[string]operations.ActionHandler{gitops.CheckpointHandoffAction: handler},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope("gds handoff verify", err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success("gds handoff verify", result)
	envelope.OperationID = operationID
	envelope.Scope["repository_id"] = plan.Scope.Repositories[0]
	return envelope
}

func (services *Services) handoffContext(
	ctx context.Context,
	path string,
	selected []string,
	store *state.Store,
	maxAge time.Duration,
) (handoffContext, []domain.Finding) {
	assessment := HandoffAssessment{
		WorktreeRoot: path, Reason: "not-proven", Files: []HandoffFile{},
		RequiredChecks: []HandoffCheck{},
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return handoffContext{assessment: assessment}, []domain.Finding{handoffFinding("git-boundary-not-proven", path)}
	}
	assessment.WorktreeRoot = info.WorktreeRoot
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil {
		return handoffContext{root: info.WorktreeRoot, assessment: assessment}, []domain.Finding{handoffFinding("git-state-not-proven", info.WorktreeRoot)}
	}
	assessment.Status = status
	estateRoot, anchor, policyFindings := services.policyInputs(ctx, info.WorktreeRoot)
	if len(policyFindings) != 0 {
		assessment.Reason = "policy-not-proven"
		return handoffContext{root: info.WorktreeRoot, assessment: assessment}, policyFindings
	}
	assessment.RepositoryID = anchor.Repository.ID
	assessment.DraftPRPolicy = anchor.Git.HandoffPR
	current := handoffContext{root: info.WorktreeRoot, repositoryID: anchor.Repository.ID, assessment: assessment}
	if status.Head.Mode != "branch" || status.Head.OID == "" || status.Branch.Name == "" {
		current.assessment.Reason = "attached-task-branch-required"
		return current, []domain.Finding{handoffFinding(current.assessment.Reason, info.WorktreeRoot)}
	}
	if status.Branch.Name == anchor.Git.DefaultBranch {
		current.assessment.Reason = "protected-default-branch"
		return current, []domain.Finding{handoffFinding(current.assessment.Reason, info.WorktreeRoot)}
	}
	if status.Changes.Conflicted != 0 || status.Submodules.Conflicted != 0 {
		current.assessment.Reason = "conflicted"
		return current, []domain.Finding{handoffFinding(current.assessment.Reason, info.WorktreeRoot)}
	}
	branchRef := "refs/heads/" + status.Branch.Name
	remoteRef := branchRef
	if status.Branch.UpstreamState == "present" {
		if !strings.HasPrefix(status.Branch.Upstream, "origin/") || status.Branch.Upstream == "origin/HEAD" {
			current.assessment.Reason = "unsupported-upstream"
			return current, []domain.Finding{handoffFinding(current.assessment.Reason, info.WorktreeRoot)}
		}
		remoteRef = "refs/heads/" + strings.TrimPrefix(status.Branch.Upstream, "origin/")
	}
	trackingRef := "refs/remotes/origin/" + strings.TrimPrefix(remoteRef, "refs/heads/")
	if err := gitprovider.ValidateFastForwardRefs(branchRef, trackingRef); err != nil {
		current.assessment.Reason = "branch-ref-unsupported"
		return current, []domain.Finding{handoffFinding(current.assessment.Reason, info.WorktreeRoot)}
	}
	remoteEvidence, reason := services.durableOriginEvidence(
		ctx, store, anchor.Repository.ID, info.WorktreeRoot, status.Head.OID, maxAge,
	)
	if reason != "" {
		current.assessment.Reason = reason
		return current, []domain.Finding{handoffFinding(reason, info.WorktreeRoot)}
	}
	remoteOID, remoteFound, err := services.GitMutations.ObserveRemoteBranchOptional(
		ctx, info.WorktreeRoot, "origin", remoteRef,
	)
	if err != nil {
		current.assessment.Reason = "remote-state-not-proven"
		return current, []domain.Finding{handoffFinding(current.assessment.Reason, info.WorktreeRoot)}
	}
	if !remoteFound {
		remoteOID = strings.Repeat("0", len(status.Head.OID))
	} else {
		trackingOID := ""
		for _, ref := range remoteEvidence.Refs {
			if ref.Reference == trackingRef {
				trackingOID = ref.OID
				break
			}
		}
		if trackingOID != remoteOID {
			current.assessment.Reason = "remote-ref-drift"
			return current, []domain.Finding{handoffFinding(current.assessment.Reason, info.WorktreeRoot)}
		}
		ancestor, err := services.GitMutations.IsAncestor(
			ctx, info.WorktreeRoot, remoteOID, status.Head.OID,
		)
		if err != nil || !ancestor {
			current.assessment.Reason = "remote-diverged"
			return current, []domain.Finding{handoffFinding(current.assessment.Reason, info.WorktreeRoot)}
		}
	}
	files, err := services.handoffFiles(ctx, info.WorktreeRoot, selected)
	if err != nil {
		current.assessment.Reason = "file-set-not-proven"
		return current, []domain.Finding{handoffFinding(current.assessment.Reason, info.WorktreeRoot)}
	}
	current.assessment.Files = files
	checks := requiredHandoffChecks(anchor.Verification)
	current.assessment.RequiredChecks = checks
	findings := []domain.Finding{}
	for _, check := range checks {
		findings = append(findings, domain.Finding{
			Code: "GDS_HANDOFF_CHECK_NOT_PROVEN", Severity: domain.SeverityInfo,
			Message:  "A required checkpoint check is recorded as NOT_PROVEN; exact approval must account for it.",
			Evidence: map[string]any{"check": check.Name, "commands": check.Commands},
		})
	}
	if anchor.Git.HandoffPR == "required" {
		current.assessment.Reason = "draft-pr-provider-unavailable"
		findings = append(findings, domain.Finding{
			Code: "GDS_HANDOFF_DRAFT_PR_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Repository policy requires a draft PR, but live PR mutation remains disabled until C8.",
		})
		return current, findings
	}
	if anchor.Git.HandoffPR == "preferred" {
		findings = append(findings, domain.Finding{
			Code: "GDS_HANDOFF_DRAFT_PR_NOT_PROVEN", Severity: domain.SeverityInfo,
			Message: "Draft PR publication is preferred but not included in the isolated C4 apply boundary.",
		})
	}
	applySupported := true
	if err := services.GitMutations.LocalPushSupported(ctx, info.WorktreeRoot); err != nil {
		applySupported = false
		if !errors.Is(err, gitprovider.ErrNetworkMutationDisabled) {
			current.assessment.Reason = "push-provider-not-proven"
			return current, append(findings, handoffFinding(current.assessment.Reason, info.WorktreeRoot))
		}
		findings = append(findings, domain.Finding{
			Code: "GDS_HANDOFF_LIVE_PUSH_DISABLED", Severity: domain.SeverityInfo,
			Message: "The plan is inspectable, but apply to a network remote remains disabled until C8.",
		})
	}
	fingerprint, err := canonicaljson.Digest(struct {
		Head       gitprovider.HeadState      `json:"head"`
		Branch     gitprovider.BranchState    `json:"branch"`
		Changes    gitprovider.ChangeState    `json:"changes"`
		Submodules gitprovider.SubmoduleState `json:"submodules"`
		Files      []HandoffFile              `json:"files"`
	}{status.Head, status.Branch, status.Changes, status.Submodules, files})
	if err != nil {
		current.assessment.Reason = "worktree-fingerprint-not-proven"
		return current, append(findings, handoffFinding(current.assessment.Reason, info.WorktreeRoot))
	}
	manifestDigest, err := fileDigest(filepath.Join(info.WorktreeRoot, ".gds", "repository.yaml"))
	if err != nil {
		current.assessment.Reason = "manifest-not-proven"
		return current, append(findings, handoffFinding(current.assessment.Reason, info.WorktreeRoot))
	}
	compiled := services.Compiler.CompileDirectory(
		estateRoot, anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		current.assessment.Reason = "policy-not-proven"
		return current, append(findings, compiled.Findings...)
	}
	current.assessment.Eligible = true
	current.assessment.ApplySupported = applySupported
	current.assessment.Reason = "checkpoint-commit-push"
	current.assessment.BranchRef = branchRef
	current.assessment.RemoteRef = remoteRef
	current.assessment.ExpectedRemoteOID = remoteOID
	current.assessment.RemoteEvidenceHash = remoteEvidence.EvidenceDigest
	current.observation = operations.Observation{
		RepositoryID: anchor.Repository.ID, HeadOID: status.Head.OID,
		WorktreeFingerprint: fingerprint, UpstreamOID: remoteOID,
		RemoteDefaultOID: remoteOID, RemoteEvidenceDigest: remoteEvidence.EvidenceDigest,
		ManifestDigest: manifestDigest, PolicyDigest: compiled.Document.CompiledPolicy.Digest,
	}
	return current, findings
}

func (services *Services) handoffFiles(
	ctx context.Context,
	root string,
	selected []string,
) ([]HandoffFile, error) {
	if len(selected) == 0 || len(selected) > 256 {
		return nil, errors.New("handoff requires 1-256 explicit files")
	}
	paths := append([]string(nil), selected...)
	sort.Strings(paths)
	for index, path := range paths {
		if index > 0 && path == paths[index-1] {
			return nil, errors.New("handoff file paths must be unique")
		}
		if filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path ||
			path == "." || path == ".." || strings.HasPrefix(path, "../") ||
			path == ".git" || strings.HasPrefix(path, ".git/") ||
			strings.ContainsAny(path, "\x00\r\n") {
			return nil, errors.New("handoff file path is unsafe")
		}
	}
	indexResult, err := services.Git.Run(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	tracked := map[string]struct{}{}
	for _, record := range strings.Split(string(indexResult.Stdout), "\x00") {
		_, path, found := strings.Cut(record, "\t")
		if found && path != "" {
			tracked[path] = struct{}{}
		}
	}
	files := make([]HandoffFile, 0, len(paths))
	for _, path := range paths {
		statusResult, err := services.Git.Run(
			ctx, root, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--", path,
		)
		if err != nil || len(statusResult.Stdout) == 0 {
			return nil, errors.New("selected handoff file is not changed")
		}
		_, isTracked := tracked[path]
		absolute := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(absolute)
		stateName := "modified"
		content := []byte{}
		switch {
		case errors.Is(err, os.ErrNotExist) && isTracked:
			stateName = "deleted"
		case err != nil:
			return nil, err
		case !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxHandoffFileBytes:
			return nil, errors.New("selected handoff file is not a bounded regular file")
		default:
			content, err = os.ReadFile(absolute)
			if err != nil {
				return nil, err
			}
			if !isTracked {
				stateName = "untracked"
			}
		}
		files = append(files, HandoffFile{
			Path: path, State: stateName,
			ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(content)),
			StatusDigest:  fmt.Sprintf("sha256:%x", sha256.Sum256(statusResult.Stdout)),
		})
	}
	return files, nil
}

func requiredHandoffChecks(verification domain.VerificationPolicy) []HandoffCheck {
	commands := map[string][]string{
		"lint": verification.Commands.Lint, "typecheck": verification.Commands.Typecheck,
		"test": verification.Commands.Test, "build": verification.Commands.Build,
		"compatibility": verification.Commands.Compatibility,
		"package":       verification.Commands.Package,
	}
	checks := make([]HandoffCheck, 0, len(verification.Required))
	for _, name := range verification.Required {
		checks = append(checks, HandoffCheck{
			Name: name, Status: "not-proven", Commands: append([]string(nil), commands[name]...),
		})
	}
	sort.Slice(checks, func(left, right int) bool { return checks[left].Name < checks[right].Name })
	return checks
}

func validateHandoffRequest(options HandoffOptions) *domain.Finding {
	if len(options.Files) == 0 {
		return &domain.Finding{
			Code: "GDS_HANDOFF_FILES_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Handoff planning requires an explicit --file set.",
		}
	}
	if strings.TrimSpace(options.Message) == "" || len(options.Message) > 4096 ||
		strings.ContainsRune(options.Message, '\x00') {
		return &domain.Finding{
			Code: "GDS_HANDOFF_MESSAGE_INVALID", Severity: domain.SeverityHigh,
			Message: "Handoff planning requires a bounded explicit commit message.",
		}
	}
	if strings.TrimSpace(options.AuthorName) == "" || strings.TrimSpace(options.AuthorEmail) == "" ||
		strings.ContainsAny(options.AuthorName+options.AuthorEmail, "\x00\r\n<>") ||
		!strings.Contains(options.AuthorEmail, "@") {
		return &domain.Finding{
			Code: "GDS_HANDOFF_AUTHOR_INVALID", Severity: domain.SeverityHigh,
			Message: "Handoff planning requires an explicit safe author name and email.",
		}
	}
	return nil
}

func handoffFinding(reason string, path string) domain.Finding {
	return domain.Finding{
		Code: "GDS_HANDOFF_NOT_PROVEN", Severity: domain.SeverityHigh,
		Message:  "The current checkout cannot produce a safe handoff plan.",
		Evidence: map[string]any{"path": path, "reason": reason},
	}
}

func loadHandoffPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
	schemas *validation.Set,
) (operations.Plan, string, []string, time.Duration, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return operations.Plan{}, "", nil, 0, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil || plan.Operation != "handoff-work" || plan.PlanDigest != record.PlanDigest ||
		len(plan.Steps) != 1 || len(plan.Validate(schemas)) != 0 {
		return operations.Plan{}, "", nil, 0, errors.New("stored plan is not a valid handoff plan")
	}
	step := plan.Steps[0]
	if step.Action != gitops.CheckpointHandoffAction {
		return operations.Plan{}, "", nil, 0, errors.New("stored plan has another action")
	}
	raw, ok := step.Parameters["handoff"].(map[string]any)
	if !ok {
		return operations.Plan{}, "", nil, 0, errors.New("handoff parameters are missing")
	}
	root, _ := raw["worktree_root"].(string)
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return operations.Plan{}, "", nil, 0, errors.New("handoff root is invalid")
	}
	fileValues, _ := raw["files"].([]any)
	files := make([]string, 0, len(fileValues))
	for _, value := range fileValues {
		file, _ := value.(map[string]any)
		path, _ := file["path"].(string)
		if path == "" {
			return operations.Plan{}, "", nil, 0, errors.New("handoff file path is invalid")
		}
		files = append(files, path)
	}
	seconds, ok := raw["refresh_max_age_seconds"].(float64)
	if !ok || seconds < 1 || seconds > 3600 || seconds != float64(int64(seconds)) {
		return operations.Plan{}, "", nil, 0, errors.New("handoff refresh age is invalid")
	}
	return plan, root, files, time.Duration(int64(seconds)) * time.Second, nil
}

func handoffPlanInvalid(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
		Code: "GDS_HANDOFF_PLAN_INVALID", Severity: domain.SeverityHigh,
		Message: "The selected plan is not a valid immutable handoff plan.",
	})
}
