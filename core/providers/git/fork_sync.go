package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type ForkSyncEvidence struct {
	WorktreeRoot   string `json:"worktree_root"`
	BranchRef      string `json:"branch_ref"`
	OriginOID      string `json:"origin_oid"`
	UpstreamOID    string `json:"upstream_oid"`
	HeadOID        string `json:"head_oid"`
	CanFastForward bool   `json:"can_fast_forward"`
}

type ForkSyncReport struct {
	Before ForkSyncEvidence `json:"before"`
	After  ForkSyncEvidence `json:"after"`
}

func (runner *MutationRunner) ObserveForkFastForward(
	ctx context.Context,
	directory string,
	branchRef string,
) (ForkSyncEvidence, error) {
	if err := validateLocalBranchRef(branchRef); err != nil {
		return ForkSyncEvidence{}, err
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return ForkSyncEvidence{}, err
	}
	if err := runner.validateWorktreeMutationConfiguration(ctx, root); err != nil {
		return ForkSyncEvidence{}, err
	}
	readRunner := runner.readRunner()
	status, err := readRunner.InspectStatus(ctx, root)
	branchName := strings.TrimPrefix(branchRef, "refs/heads/")
	if err != nil || status.Head.Mode != "branch" || "refs/heads/"+status.Branch.Name != branchRef ||
		status.Branch.Upstream != "origin/"+branchName || !checkoutStatusClean(status) ||
		len(status.Worktrees) != 1 || status.Worktrees[0].Path != root || status.Worktrees[0].Locked {
		return ForkSyncEvidence{}, errors.New("fork sync requires one clean exclusive origin-tracking branch")
	}
	originURL, err := runner.validatedPushURL(ctx, root, "origin")
	if err != nil {
		return ForkSyncEvidence{}, err
	}
	_, upstreamURL, err := runner.validatedRemoteURL(ctx, root, "upstream")
	if err != nil {
		return ForkSyncEvidence{}, err
	}
	originPath, err := isolatedRemotePath(originURL)
	if err != nil {
		return ForkSyncEvidence{}, err
	}
	upstreamPath, err := isolatedRemotePath(upstreamURL)
	if err != nil {
		return ForkSyncEvidence{}, err
	}
	originOID, err := runner.observeRemoteRef(ctx, root, originPath, branchRef, len(status.Head.OID))
	if err != nil || originOID != status.Head.OID {
		return ForkSyncEvidence{}, errors.New("fork checkout HEAD differs from origin")
	}
	upstreamOID, err := runner.observeRemoteRef(ctx, root, upstreamPath, branchRef, len(status.Head.OID))
	if err != nil || upstreamOID == zeroOID(len(status.Head.OID)) {
		return ForkSyncEvidence{}, errors.New("fork upstream branch is unavailable")
	}
	canFastForward := originOID == upstreamOID
	if !canFastForward {
		contains, err := runner.run(
			ctx, upstreamPath, map[int]struct{}{0: {}, 1: {}},
			"cat-file", "-e", originOID+"^{commit}",
		)
		if err != nil {
			return ForkSyncEvidence{}, err
		}
		if contains.ExitCode == 0 {
			ancestor, err := runner.run(
				ctx, upstreamPath, map[int]struct{}{0: {}, 1: {}},
				"merge-base", "--is-ancestor", originOID, upstreamOID,
			)
			if err != nil {
				return ForkSyncEvidence{}, err
			}
			canFastForward = ancestor.ExitCode == 0
		}
	}
	if !canFastForward {
		return ForkSyncEvidence{}, errors.New("fork-only commits would be lost; automatic sync is blocked")
	}
	return ForkSyncEvidence{
		WorktreeRoot: root, BranchRef: branchRef, OriginOID: originOID,
		UpstreamOID: upstreamOID, HeadOID: status.Head.OID, CanFastForward: true,
	}, nil
}

func (runner *MutationRunner) SyncForkFastForward(
	ctx context.Context,
	directory string,
	branchRef string,
	expectedOriginOID string,
	expectedUpstreamOID string,
) (ForkSyncReport, error) {
	before, err := runner.ObserveForkFastForward(ctx, directory, branchRef)
	if err != nil {
		return ForkSyncReport{}, err
	}
	report := ForkSyncReport{Before: before}
	if before.OriginOID != expectedOriginOID || before.UpstreamOID != expectedUpstreamOID ||
		before.HeadOID != expectedOriginOID || expectedOriginOID == expectedUpstreamOID {
		return report, errors.New("fork sync precondition changed or update is already complete")
	}
	if _, err := runner.FetchRemote(ctx, before.WorktreeRoot, "upstream"); err != nil {
		return report, err
	}
	upstreamTracking := "refs/remotes/upstream/" + strings.TrimPrefix(branchRef, "refs/heads/")
	if err := runner.fastForwardForkCheckout(
		ctx, before.WorktreeRoot, branchRef, upstreamTracking,
		expectedOriginOID, expectedUpstreamOID,
	); err != nil {
		return report, err
	}
	originURL, err := runner.validatedPushURL(ctx, before.WorktreeRoot, "origin")
	if err != nil {
		return report, err
	}
	currentOrigin, err := runner.observeRemoteRef(
		ctx, before.WorktreeRoot, originURL, branchRef, len(expectedOriginOID),
	)
	if err != nil || currentOrigin != expectedOriginOID {
		return report, errors.New("origin changed before fork sync push")
	}
	if _, err := runner.runWithEnvironment(
		ctx, before.WorktreeRoot, map[int]struct{}{0: {}}, nil, "-c", "protocol.allow=never",
		"-c", "protocol.file.allow=always", "push", "--porcelain", "--no-verify",
		originURL, expectedUpstreamOID+":"+branchRef,
	); err != nil {
		return report, err
	}
	remoteOID, err := runner.observeRemoteRef(
		ctx, before.WorktreeRoot, originURL, branchRef, len(expectedUpstreamOID),
	)
	if err != nil || remoteOID != expectedUpstreamOID {
		return report, errors.New("fork sync origin verification failed")
	}
	if err := runner.updateRemoteTrackingAndUpstream(
		ctx, before.WorktreeRoot, branchRef, branchRef,
		expectedUpstreamOID, expectedOriginOID,
	); err != nil {
		return report, err
	}
	after, err := runner.ObserveForkFastForward(ctx, before.WorktreeRoot, branchRef)
	if err != nil {
		return report, err
	}
	report.After = after
	if after.HeadOID != expectedUpstreamOID || after.OriginOID != expectedUpstreamOID ||
		after.UpstreamOID != expectedUpstreamOID {
		return report, errors.New("fork sync postcondition failed")
	}
	return report, nil
}

func (runner *MutationRunner) fastForwardForkCheckout(
	ctx context.Context,
	root string,
	branchRef string,
	upstreamTrackingRef string,
	expectedHeadOID string,
	targetOID string,
) error {
	if validateLocalBranchRef(branchRef) != nil || !safeRemoteTrackingRef(upstreamTrackingRef) ||
		!strings.HasPrefix(upstreamTrackingRef, "refs/remotes/upstream/") ||
		validateOID(expectedHeadOID, false) != nil || validateOID(targetOID, false) != nil {
		return errors.New("fork fast-forward input is invalid")
	}
	readRunner := runner.readRunner()
	status, err := readRunner.InspectStatus(ctx, root)
	if err != nil || status.Head.OID != expectedHeadOID ||
		"refs/heads/"+status.Branch.Name != branchRef || !checkoutStatusClean(status) {
		return errors.New("fork checkout changed before fast-forward")
	}
	tracking, err := runner.run(
		ctx, root, map[int]struct{}{0: {}}, "rev-parse", "--verify", upstreamTrackingRef,
	)
	if err != nil || strings.TrimSpace(string(tracking.Stdout)) != targetOID {
		return errors.New("fork upstream tracking ref differs from plan")
	}
	ancestor, err := runner.isAncestor(ctx, root, expectedHeadOID, targetOID)
	if err != nil || !ancestor || expectedHeadOID == targetOID {
		return errors.New("fork sync target is not a strict fast-forward descendant")
	}
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}},
		[]string{"GIT_MERGE_AUTOEDIT=no"},
		"-c", "core.hooksPath=/dev/null", "-c", "merge.gpgSign=false",
		"merge", "--ff-only", "--no-edit", upstreamTrackingRef,
	); err != nil {
		return err
	}
	after, err := readRunner.InspectStatus(ctx, root)
	if err != nil || after.Head.OID != targetOID || "refs/heads/"+after.Branch.Name != branchRef ||
		!checkoutStatusClean(after) {
		return errors.New("fork checkout fast-forward verification failed")
	}
	return nil
}

func isolatedRemotePath(raw string) (string, error) {
	if !filepath.IsAbs(raw) {
		return "", ErrNetworkMutationDisabled
	}
	physical, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(physical)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("isolated fork remote must be a real local directory")
	}
	return filepath.Clean(physical), nil
}
