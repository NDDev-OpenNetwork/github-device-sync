package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitref"
)

type CheckoutEvidence struct {
	WorktreeRoot string `json:"worktree_root"`
	BranchRef    string `json:"branch_ref"`
	HeadOID      string `json:"head_oid"`
	UpstreamRef  string `json:"upstream_ref"`
	UpstreamOID  string `json:"upstream_oid"`
	Clean        bool   `json:"clean"`
}

type FastForwardReport struct {
	Before CheckoutEvidence `json:"before"`
	After  CheckoutEvidence `json:"after"`
}

func ValidateFastForwardRefs(branchRef string, upstreamRef string) error {
	if err := validateLocalBranchRef(branchRef); err != nil {
		return err
	}
	if !safeRemoteTrackingRef(upstreamRef) ||
		!strings.HasPrefix(upstreamRef, "refs/remotes/origin/") ||
		upstreamRef == "refs/remotes/origin/HEAD" {
		return errors.New("fast-forward upstream ref is unsafe")
	}
	return nil
}

func (runner *MutationRunner) ObserveCheckout(
	ctx context.Context,
	directory string,
) (CheckoutEvidence, error) {
	root, err := validateMutationRoot(directory)
	if err != nil {
		return CheckoutEvidence{}, err
	}
	readRunner := runner.readRunner()
	status, err := readRunner.InspectStatus(ctx, root)
	if err != nil {
		return CheckoutEvidence{}, err
	}
	if status.Head.Mode != "branch" || status.Head.OID == "" || status.Branch.Name == "" {
		return CheckoutEvidence{}, errors.New("checkout HEAD is not an attached branch")
	}
	branchRef := "refs/heads/" + status.Branch.Name
	if err := validateLocalBranchRef(branchRef); err != nil {
		return CheckoutEvidence{}, err
	}
	upstreamRef, err := upstreamReference(status.Branch.Upstream)
	if err != nil {
		return CheckoutEvidence{}, err
	}
	refs, err := runner.remoteTrackingRefs(ctx, root, "origin")
	if err != nil {
		return CheckoutEvidence{}, err
	}
	upstreamOID := ""
	for _, ref := range refs {
		if ref.Reference == upstreamRef {
			upstreamOID = ref.OID
			break
		}
	}
	if upstreamOID == "" {
		return CheckoutEvidence{}, fmt.Errorf("upstream ref is absent from origin tracking refs")
	}
	return CheckoutEvidence{
		WorktreeRoot: root, BranchRef: branchRef, HeadOID: status.Head.OID,
		UpstreamRef: upstreamRef, UpstreamOID: upstreamOID,
		Clean: checkoutStatusClean(status),
	}, nil
}

func (runner *MutationRunner) FastForwardCheckout(
	ctx context.Context,
	directory string,
	expectedBranchRef string,
	expectedHeadOID string,
	upstreamRef string,
	targetOID string,
) (FastForwardReport, error) {
	if err := ValidateFastForwardRefs(expectedBranchRef, upstreamRef); err != nil {
		return FastForwardReport{}, err
	}
	if err := validateOID(expectedHeadOID, false); err != nil {
		return FastForwardReport{}, fmt.Errorf("invalid expected HEAD OID: %w", err)
	}
	if err := validateOID(targetOID, false); err != nil {
		return FastForwardReport{}, fmt.Errorf("invalid target OID: %w", err)
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return FastForwardReport{}, err
	}
	if err := runner.validateWorktreeMutationConfiguration(ctx, root); err != nil {
		return FastForwardReport{}, err
	}
	before, err := runner.ObserveCheckout(ctx, root)
	if err != nil {
		return FastForwardReport{}, err
	}
	report := FastForwardReport{Before: before}
	if before.BranchRef != expectedBranchRef || before.HeadOID != expectedHeadOID ||
		before.UpstreamRef != upstreamRef || before.UpstreamOID != targetOID || !before.Clean {
		return report, errors.New("fast-forward checkout precondition changed")
	}
	ancestor, err := runner.isAncestor(ctx, root, expectedHeadOID, targetOID)
	if err != nil {
		return report, err
	}
	if !ancestor || expectedHeadOID == targetOID {
		return report, errors.New("target is not a strict fast-forward descendant")
	}
	hooks, err := os.MkdirTemp("", "gds-disabled-hooks-")
	if err != nil {
		return report, fmt.Errorf("create isolated hooks directory: %w", err)
	}
	defer os.RemoveAll(hooks)
	if info, inspectErr := os.Lstat(hooks); inspectErr != nil || !info.IsDir() ||
		info.Mode()&os.ModeSymlink != 0 {
		return report, errors.New("isolated hooks directory is unsafe")
	}
	_, commandErr := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}},
		[]string{"GIT_MERGE_AUTOEDIT=no"},
		"-c", "core.hooksPath="+hooks,
		"-c", "submodule.recurse=false",
		"merge", "--ff-only", "--no-edit", "--no-stat", targetOID,
	)
	after, observeErr := runner.ObserveCheckout(context.WithoutCancel(ctx), root)
	if observeErr == nil {
		report.After = after
	}
	if commandErr != nil {
		return report, commandErr
	}
	if observeErr != nil {
		return report, observeErr
	}
	if after.BranchRef != expectedBranchRef || after.HeadOID != targetOID ||
		after.UpstreamRef != upstreamRef || after.UpstreamOID != targetOID || !after.Clean {
		return report, errors.New("fast-forward checkout postcondition failed")
	}
	return report, nil
}

func (runner *MutationRunner) validateWorktreeMutationConfiguration(
	ctx context.Context,
	root string,
) error {
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}, 1: {}},
		"config", "--local", "--no-includes", "--null", "--name-only", "--list",
	)
	if err != nil {
		return err
	}
	if result.ExitCode == 1 {
		return nil
	}
	for _, raw := range strings.Split(string(result.Stdout), "\x00") {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		unsafe := strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") ||
			strings.HasPrefix(key, "alias.") || strings.HasPrefix(key, "filter.") ||
			strings.HasPrefix(key, "merge.") || strings.HasPrefix(key, "credential.") ||
			strings.HasPrefix(key, "url.") || strings.HasSuffix(key, ".update") ||
			strings.HasSuffix(key, ".mergeoptions") ||
			key == "core.hookspath" || key == "core.attributesfile" ||
			key == "core.fsmonitor" || key == "core.sshcommand" || key == "core.worktree"
		if unsafe {
			return fmt.Errorf("fast-forward blocked by unsafe local Git config key %q", key)
		}
	}
	return nil
}

func checkoutStatusClean(status Status) bool {
	return status.Changes.Staged == 0 && status.Changes.Unstaged == 0 &&
		status.Changes.Untracked == 0 && status.Changes.Conflicted == 0 &&
		status.Changes.SubmoduleChanges == 0 && status.Submodules.Modified == 0 &&
		status.Submodules.Conflicted == 0
}

func upstreamReference(upstream string) (string, error) {
	if !strings.HasPrefix(upstream, "origin/") || upstream == "origin/HEAD" {
		return "", errors.New("checkout upstream must be an origin branch")
	}
	reference := "refs/remotes/" + upstream
	if !safeRemoteTrackingRef(reference) {
		return "", errors.New("checkout upstream ref is unsafe")
	}
	return reference, nil
}

func validateLocalBranchRef(reference string) error {
	return gitref.ValidateLocalBranchRef(reference)
}
