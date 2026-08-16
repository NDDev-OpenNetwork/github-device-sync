package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const FastForwardCheckoutAction = "fast-forward-checkout"

type FastForwardCheckoutHandler struct {
	runner *gitprovider.MutationRunner
}

func NewFastForwardCheckoutHandler(
	runner *gitprovider.MutationRunner,
) (*FastForwardCheckoutHandler, error) {
	if runner == nil {
		return nil, errors.New("fast-forward handler requires a Git mutation runner")
	}
	return &FastForwardCheckoutHandler{runner: runner}, nil
}

func (handler *FastForwardCheckoutHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := fastForwardParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.runner.FastForwardCheckout(
		ctx, parameters.WorktreeRoot, parameters.BranchRef,
		parameters.ExpectedHeadOID, parameters.UpstreamRef, parameters.TargetOID,
	)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *FastForwardCheckoutHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := fastForwardParameters(step)
	if err != nil {
		return err
	}
	var expected gitprovider.CheckoutEvidence
	if len(afterRaw) != 0 {
		if err := json.Unmarshal(afterRaw, &expected); err != nil {
			return fmt.Errorf("decode fast-forward evidence: %w", err)
		}
	}
	if expected.WorktreeRoot != parameters.WorktreeRoot ||
		expected.BranchRef != parameters.BranchRef || expected.HeadOID != parameters.TargetOID ||
		expected.UpstreamRef != parameters.UpstreamRef || expected.UpstreamOID != parameters.TargetOID ||
		!expected.Clean {
		return errors.New("fast-forward after evidence differs from the exact step")
	}
	observed, err := handler.runner.ObserveCheckout(ctx, parameters.WorktreeRoot)
	if err != nil {
		return err
	}
	if observed != expected {
		return errors.New("fast-forward checkout no longer matches recorded evidence")
	}
	return nil
}

type fastForwardStepParameters struct {
	WorktreeRoot         string
	BranchRef            string
	UpstreamRef          string
	ExpectedHeadOID      string
	TargetOID            string
	RemoteEvidenceDigest string
}

func fastForwardParameters(step operations.Step) (fastForwardStepParameters, error) {
	if step.Action != FastForwardCheckoutAction {
		return fastForwardStepParameters{}, fmt.Errorf("unexpected local Git action %q", step.Action)
	}
	raw, ok := step.Parameters["checkout_sync"].(map[string]any)
	if !ok {
		return fastForwardStepParameters{}, errors.New("checkout_sync parameters are missing")
	}
	parameters := fastForwardStepParameters{}
	parameters.WorktreeRoot, _ = raw["worktree_root"].(string)
	parameters.BranchRef, _ = raw["branch_ref"].(string)
	parameters.UpstreamRef, _ = raw["upstream_ref"].(string)
	parameters.ExpectedHeadOID, _ = raw["expected_head_oid"].(string)
	parameters.TargetOID, _ = raw["target_oid"].(string)
	parameters.RemoteEvidenceDigest, _ = raw["remote_evidence_digest"].(string)
	if parameters.WorktreeRoot == "" || !filepath.IsAbs(parameters.WorktreeRoot) ||
		filepath.Clean(parameters.WorktreeRoot) != parameters.WorktreeRoot ||
		parameters.BranchRef == "" || parameters.UpstreamRef == "" ||
		parameters.ExpectedHeadOID == "" || parameters.TargetOID == "" ||
		parameters.RemoteEvidenceDigest == "" {
		return fastForwardStepParameters{}, errors.New("checkout_sync parameters are incomplete")
	}
	return parameters, nil
}
