package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type CheckoutQuarantineEvidence struct {
	WorkspaceRoot  string `json:"workspace_root"`
	CheckoutPath   string `json:"checkout_path"`
	StateRoot      string `json:"state_root"`
	QuarantinePath string `json:"quarantine_path"`
	HeadOID        string `json:"head_oid"`
	BranchRef      string `json:"branch_ref"`
	RemoteOID      string `json:"remote_oid"`
	AnchorDigest   string `json:"anchor_digest"`
	Location       string `json:"location"`
}

type CheckoutQuarantineReport struct {
	Before CheckoutQuarantineEvidence `json:"before"`
	After  CheckoutQuarantineEvidence `json:"after"`
}

func (runner *MutationRunner) ObserveCheckoutQuarantine(
	ctx context.Context,
	workspaceRoot string,
	checkoutPath string,
	stateRoot string,
	quarantinePath string,
	expectedHeadOID string,
	branchRef string,
	anchorDigest string,
) (CheckoutQuarantineEvidence, error) {
	workspace, checkout, err := checkoutTargetPaths(workspaceRoot, checkoutPath)
	if err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	info, err := os.Lstat(checkout)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return CheckoutQuarantineEvidence{}, errors.New("checkout removal target must be a real directory")
	}
	state, quarantine, err := quarantineTargetPaths(stateRoot, quarantinePath)
	if err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		return CheckoutQuarantineEvidence{}, errors.New("checkout quarantine target must be absent")
	}
	evidence, err := runner.observeCheckoutForQuarantine(
		ctx, checkout, expectedHeadOID, branchRef, anchorDigest,
	)
	if err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	evidence.WorkspaceRoot = workspace
	evidence.CheckoutPath = checkout
	evidence.StateRoot = state
	evidence.QuarantinePath = quarantine
	evidence.Location = "workspace"
	return evidence, nil
}

func (runner *MutationRunner) QuarantineCheckout(
	ctx context.Context,
	workspaceRoot string,
	checkoutPath string,
	stateRoot string,
	quarantinePath string,
	expectedHeadOID string,
	branchRef string,
	anchorDigest string,
) (CheckoutQuarantineReport, error) {
	before, err := runner.ObserveCheckoutQuarantine(
		ctx, workspaceRoot, checkoutPath, stateRoot, quarantinePath,
		expectedHeadOID, branchRef, anchorDigest,
	)
	if err != nil {
		return CheckoutQuarantineReport{}, err
	}
	if err := ensureQuarantineParent(before.StateRoot, filepath.Dir(before.QuarantinePath)); err != nil {
		return CheckoutQuarantineReport{Before: before}, err
	}
	if err := os.Rename(before.CheckoutPath, before.QuarantinePath); err != nil {
		return CheckoutQuarantineReport{Before: before}, fmt.Errorf("move checkout to quarantine: %w", err)
	}
	if _, err := os.Lstat(before.CheckoutPath); !errors.Is(err, os.ErrNotExist) {
		return CheckoutQuarantineReport{Before: before}, errors.New("workspace checkout still exists after quarantine move")
	}
	after, err := runner.observeCheckoutForQuarantine(
		ctx, before.QuarantinePath, expectedHeadOID, branchRef, anchorDigest,
	)
	if err != nil {
		return CheckoutQuarantineReport{Before: before}, err
	}
	after.WorkspaceRoot = before.WorkspaceRoot
	after.CheckoutPath = before.CheckoutPath
	after.StateRoot = before.StateRoot
	after.QuarantinePath = before.QuarantinePath
	after.Location = "quarantine"
	return CheckoutQuarantineReport{Before: before, After: after}, nil
}

func (runner *MutationRunner) ObserveQuarantinedCheckout(
	ctx context.Context,
	workspaceRoot string,
	checkoutPath string,
	stateRoot string,
	quarantinePath string,
	expectedHeadOID string,
	branchRef string,
	anchorDigest string,
) (CheckoutQuarantineEvidence, error) {
	workspace, checkout, err := checkoutTargetPaths(workspaceRoot, checkoutPath)
	if err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	if _, statErr := os.Lstat(checkout); !errors.Is(statErr, os.ErrNotExist) {
		return CheckoutQuarantineEvidence{}, errors.New("workspace checkout still exists")
	}
	state, quarantine, err := quarantineTargetPaths(stateRoot, quarantinePath)
	if err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	evidence, err := runner.observeCheckoutForQuarantine(
		ctx, quarantine, expectedHeadOID, branchRef, anchorDigest,
	)
	if err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	evidence.WorkspaceRoot = workspace
	evidence.CheckoutPath = checkout
	evidence.StateRoot = state
	evidence.QuarantinePath = quarantine
	evidence.Location = "quarantine"
	return evidence, nil
}

func (runner *MutationRunner) observeCheckoutForQuarantine(
	ctx context.Context,
	root string,
	expectedHeadOID string,
	branchRef string,
	anchorDigest string,
) (CheckoutQuarantineEvidence, error) {
	if validateOID(expectedHeadOID, false) != nil || validateLocalBranchRef(branchRef) != nil ||
		len(anchorDigest) != 71 || !strings.HasPrefix(anchorDigest, "sha256:") {
		return CheckoutQuarantineEvidence{}, errors.New("checkout quarantine expectation is invalid")
	}
	physical, err := validateMutationRoot(root)
	if err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	if err := runner.validateWorktreeMutationConfiguration(ctx, physical); err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	readRunner := runner.readRunner()
	status, err := readRunner.InspectStatus(ctx, physical)
	if err != nil || status.Head.Mode != "branch" || status.Head.OID != expectedHeadOID ||
		"refs/heads/"+status.Branch.Name != branchRef ||
		status.Branch.Upstream != "origin/"+strings.TrimPrefix(branchRef, "refs/heads/") ||
		status.Branch.Ahead != 0 || status.Branch.Behind != 0 || status.Branch.Diverged ||
		!checkoutStatusClean(status) || len(status.Worktrees) != 1 || status.Worktrees[0].Path != physical ||
		status.Worktrees[0].Locked || status.Worktrees[0].Prunable {
		return CheckoutQuarantineEvidence{}, errors.New("checkout is not clean, current, and exclusive")
	}
	remoteURL, err := runner.validatedPushURL(ctx, physical, "origin")
	if err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	remoteOID, err := runner.observeRemoteRef(ctx, physical, remoteURL, branchRef, len(expectedHeadOID))
	if err != nil || remoteOID != expectedHeadOID {
		return CheckoutQuarantineEvidence{}, errors.New("checkout HEAD is not exactly published on origin")
	}
	if err := verifyCheckoutFiles(physical, []ExpectedCheckoutFile{{
		Path: ".gds/repository.yaml", Digest: anchorDigest,
	}}); err != nil {
		return CheckoutQuarantineEvidence{}, err
	}
	return CheckoutQuarantineEvidence{
		HeadOID: expectedHeadOID, BranchRef: branchRef, RemoteOID: remoteOID,
		AnchorDigest: anchorDigest,
	}, nil
}

func quarantineTargetPaths(stateRoot string, quarantinePath string) (string, string, error) {
	absoluteState, err := filepath.Abs(stateRoot)
	if err != nil {
		return "", "", errors.New("device state root cannot be resolved")
	}
	absoluteState = filepath.Clean(absoluteState)
	info, err := os.Lstat(absoluteState)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || absoluteState == string(filepath.Separator) {
		return "", "", errors.New("device state root must be a safe real directory")
	}
	physicalState, err := filepath.EvalSymlinks(absoluteState)
	if err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(quarantinePath) || filepath.Clean(quarantinePath) != quarantinePath {
		return "", "", errors.New("checkout quarantine path must be absolute and clean")
	}
	relative, err := filepath.Rel(absoluteState, quarantinePath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") ||
		!strings.HasPrefix(filepath.ToSlash(relative), "quarantine/checkouts/") {
		return "", "", errors.New("checkout quarantine path is outside the device quarantine root")
	}
	physicalTarget := filepath.Join(filepath.Clean(physicalState), relative)
	return filepath.Clean(physicalState), physicalTarget, nil
}

func ensureQuarantineParent(stateRoot string, parent string) error {
	relative, err := filepath.Rel(stateRoot, parent)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return errors.New("checkout quarantine parent is unsafe")
	}
	current := stateRoot
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if component == "" || component == "." || component == ".." {
			return errors.New("checkout quarantine parent is unsafe")
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
		case statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
			return errors.New("checkout quarantine parent contains an unsafe component")
		}
	}
	return nil
}
