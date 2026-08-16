package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const CompleteTaskBranchAction = "complete-task-branch"

type CompleteTaskBranchHandler struct {
	runner *gitprovider.MutationRunner
}

func NewCompleteTaskBranchHandler(
	runner *gitprovider.MutationRunner,
) (*CompleteTaskBranchHandler, error) {
	if runner == nil {
		return nil, errors.New("completion handler requires a Git mutation runner")
	}
	return &CompleteTaskBranchHandler{runner: runner}, nil
}

func (handler *CompleteTaskBranchHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := completionParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.runner.CompleteTaskBranch(
		ctx, parameters.WorktreeRoot, parameters.DefaultBranchRef,
		parameters.TaskBranchRef, parameters.ExpectedDefaultOID,
		parameters.ExpectedTaskOID,
	)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *CompleteTaskBranchHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := completionParameters(step)
	if err != nil {
		return err
	}
	var expected gitprovider.CompletionEvidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil {
		return errors.New("completion after evidence is missing or invalid")
	}
	zero := zeroOIDFor(parameters.ExpectedTaskOID)
	if expected.WorktreeRoot != parameters.WorktreeRoot ||
		expected.CurrentBranchRef != parameters.DefaultBranchRef ||
		expected.DefaultBranchRef != parameters.DefaultBranchRef ||
		expected.TaskBranchRef != parameters.TaskBranchRef ||
		expected.HeadOID != parameters.ExpectedTaskOID ||
		expected.LocalDefaultOID != parameters.ExpectedTaskOID ||
		expected.RemoteDefaultOID != parameters.ExpectedTaskOID ||
		expected.LocalTaskOID != zero || expected.RemoteTaskOID != zero || !expected.Clean {
		return errors.New("completion after evidence differs from the exact step")
	}
	observed, err := handler.runner.ObserveCompletion(
		ctx, parameters.WorktreeRoot, parameters.DefaultBranchRef, parameters.TaskBranchRef,
	)
	if err != nil {
		return err
	}
	if observed != expected {
		return errors.New("completion no longer matches recorded evidence")
	}
	return nil
}

type completionStepParameters struct {
	WorktreeRoot       string
	DefaultBranchRef   string
	TaskBranchRef      string
	ExpectedDefaultOID string
	ExpectedTaskOID    string
}

func completionParameters(step operations.Step) (completionStepParameters, error) {
	if step.Action != CompleteTaskBranchAction {
		return completionStepParameters{}, fmt.Errorf("unexpected completion action %q", step.Action)
	}
	raw, ok := step.Parameters["completion"].(map[string]any)
	if !ok {
		return completionStepParameters{}, errors.New("completion parameters are missing")
	}
	parameters := completionStepParameters{}
	parameters.WorktreeRoot, _ = raw["worktree_root"].(string)
	parameters.DefaultBranchRef, _ = raw["default_branch_ref"].(string)
	parameters.TaskBranchRef, _ = raw["task_branch_ref"].(string)
	parameters.ExpectedDefaultOID, _ = raw["expected_default_oid"].(string)
	parameters.ExpectedTaskOID, _ = raw["expected_task_oid"].(string)
	if parameters.WorktreeRoot == "" || !filepath.IsAbs(parameters.WorktreeRoot) ||
		filepath.Clean(parameters.WorktreeRoot) != parameters.WorktreeRoot ||
		parameters.DefaultBranchRef == "" || parameters.TaskBranchRef == "" ||
		parameters.ExpectedDefaultOID == "" || parameters.ExpectedTaskOID == "" {
		return completionStepParameters{}, errors.New("completion parameters are incomplete")
	}
	return parameters, nil
}

func zeroOIDFor(oid string) string {
	return strings.Repeat("0", len(oid))
}
