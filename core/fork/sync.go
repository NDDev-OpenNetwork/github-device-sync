package fork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const SyncAction = "sync-fork-fast-forward"

type SyncParameters struct {
	WorktreeRoot        string
	BranchRef           string
	ExpectedOriginOID   string
	ExpectedUpstreamOID string
	Policy              string
	PreserveForkCommits bool
}

func SyncStepParameters(
	evidence gitprovider.ForkSyncEvidence,
	policy string,
	preserveForkCommits bool,
) map[string]any {
	return map[string]any{"fork_sync": map[string]any{
		"worktree_root": evidence.WorktreeRoot, "branch_ref": evidence.BranchRef,
		"expected_origin_oid":   evidence.OriginOID,
		"expected_upstream_oid": evidence.UpstreamOID,
		"policy":                policy, "preserve_fork_commits": preserveForkCommits,
	}}
}

type SyncHandler struct {
	runner *gitprovider.MutationRunner
}

func NewSyncHandler(runner *gitprovider.MutationRunner) (*SyncHandler, error) {
	if runner == nil {
		return nil, errors.New("fork sync handler requires a Git mutation runner")
	}
	return &SyncHandler{runner: runner}, nil
}

func (handler *SyncHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := StepSyncParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.runner.SyncForkFastForward(
		ctx, parameters.WorktreeRoot, parameters.BranchRef,
		parameters.ExpectedOriginOID, parameters.ExpectedUpstreamOID,
	)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *SyncHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := StepSyncParameters(step)
	if err != nil {
		return err
	}
	var expected gitprovider.ForkSyncEvidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil ||
		expected.HeadOID != parameters.ExpectedUpstreamOID ||
		expected.OriginOID != parameters.ExpectedUpstreamOID ||
		expected.UpstreamOID != parameters.ExpectedUpstreamOID || !expected.CanFastForward {
		return errors.New("fork sync after evidence is missing or invalid")
	}
	observed, err := handler.runner.ObserveForkFastForward(
		ctx, parameters.WorktreeRoot, parameters.BranchRef,
	)
	if err != nil || observed != expected {
		return errors.New("fork sync no longer matches recorded evidence")
	}
	return nil
}

func StepSyncParameters(step operations.Step) (SyncParameters, error) {
	if step.Action != SyncAction {
		return SyncParameters{}, fmt.Errorf("unexpected fork action %q", step.Action)
	}
	raw, ok := step.Parameters["fork_sync"].(map[string]any)
	if !ok {
		return SyncParameters{}, errors.New("fork sync parameters are missing")
	}
	result := SyncParameters{}
	result.WorktreeRoot, _ = raw["worktree_root"].(string)
	result.BranchRef, _ = raw["branch_ref"].(string)
	result.ExpectedOriginOID, _ = raw["expected_origin_oid"].(string)
	result.ExpectedUpstreamOID, _ = raw["expected_upstream_oid"].(string)
	result.Policy, _ = raw["policy"].(string)
	result.PreserveForkCommits, _ = raw["preserve_fork_commits"].(bool)
	if result.WorktreeRoot == "" || !filepath.IsAbs(result.WorktreeRoot) ||
		result.BranchRef == "" || result.ExpectedOriginOID == "" ||
		result.ExpectedUpstreamOID == "" ||
		(result.Policy != "upstream-tracking" && result.Policy != "maintained-patch") ||
		!result.PreserveForkCommits {
		return SyncParameters{}, errors.New("fork sync parameters are invalid")
	}
	return result, nil
}
