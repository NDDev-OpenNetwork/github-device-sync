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

const UpdateGitlinkAction = "update-gitlink-pin"

type UpdateGitlinkHandler struct {
	runner *gitprovider.MutationRunner
}

func NewUpdateGitlinkHandler(runner *gitprovider.MutationRunner) (*UpdateGitlinkHandler, error) {
	if runner == nil {
		return nil, errors.New("gitlink handler requires a Git mutation runner")
	}
	return &UpdateGitlinkHandler{runner: runner}, nil
}

func (handler *UpdateGitlinkHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := gitlinkParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.runner.UpdateGitlink(
		ctx, parameters.ConsumerRoot, parameters.GitmodulesName,
		parameters.ExpectedOldOID, parameters.TargetOID,
	)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *UpdateGitlinkHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := gitlinkParameters(step)
	if err != nil {
		return err
	}
	var expected gitprovider.GitlinkEvidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil {
		return errors.New("gitlink after evidence is missing or invalid")
	}
	if expected.WorktreeRoot != parameters.ConsumerRoot || expected.Name != parameters.GitmodulesName ||
		expected.GitlinkOID != parameters.TargetOID || expected.GitlinkStage != 0 ||
		(expected.WorktreeState != "uninitialized" && expected.WorktreeState != "at-gitlink") ||
		expected.Staged != 1 ||
		expected.Unstaged != 0 || expected.Untracked != 0 || expected.Conflicted != 0 {
		return errors.New("gitlink after evidence differs from the exact step")
	}
	observed, err := handler.runner.ObserveGitlink(ctx, parameters.ConsumerRoot, parameters.GitmodulesName)
	if err != nil {
		return err
	}
	if observed != expected {
		return errors.New("gitlink pin no longer matches recorded evidence")
	}
	return nil
}

type gitlinkStepParameters struct {
	ConsumerRoot   string
	ModuleRoot     string
	ModuleID       string
	GitmodulesName string
	ExpectedOldOID string
	TargetOID      string
	TargetRef      string
}

func gitlinkParameters(step operations.Step) (gitlinkStepParameters, error) {
	if step.Action != UpdateGitlinkAction {
		return gitlinkStepParameters{}, fmt.Errorf("unexpected gitlink action %q", step.Action)
	}
	raw, ok := step.Parameters["gitlink_pin"].(map[string]any)
	if !ok {
		return gitlinkStepParameters{}, errors.New("gitlink pin parameters are missing")
	}
	result := gitlinkStepParameters{}
	result.ConsumerRoot, _ = raw["consumer_root"].(string)
	result.ModuleRoot, _ = raw["module_root"].(string)
	result.ModuleID, _ = raw["module_id"].(string)
	result.GitmodulesName, _ = raw["gitmodules_name"].(string)
	result.ExpectedOldOID, _ = raw["expected_old_oid"].(string)
	result.TargetOID, _ = raw["target_oid"].(string)
	result.TargetRef, _ = raw["target_ref"].(string)
	if result.ConsumerRoot == "" || !filepath.IsAbs(result.ConsumerRoot) ||
		filepath.Clean(result.ConsumerRoot) != result.ConsumerRoot ||
		result.ModuleRoot == "" || !filepath.IsAbs(result.ModuleRoot) ||
		filepath.Clean(result.ModuleRoot) != result.ModuleRoot ||
		result.ModuleID == "" || result.GitmodulesName == "" ||
		result.ExpectedOldOID == "" || result.TargetOID == "" || result.TargetRef == "" {
		return gitlinkStepParameters{}, errors.New("gitlink pin parameters are incomplete")
	}
	return result, nil
}
