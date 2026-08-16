package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const UpdateRemoteAction = "update-git-remote"

type UpdateRemoteHandler struct {
	Git *gitprovider.MutationRunner
}

func RemoteUpdateParameters(root, remote, expectedURL, targetURL string) map[string]any {
	return map[string]any{"remote_update": map[string]any{
		"root": root, "remote": remote, "expected_url": expectedURL, "target_url": targetURL,
	}}
}

func (handler *UpdateRemoteHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	root, remote, expectedURL, targetURL, err := RemoteUpdateStep(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.Git.UpdateRemote(ctx, root, remote, expectedURL, targetURL)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *UpdateRemoteHandler) Verify(
	ctx context.Context,
	step operations.Step,
	recorded json.RawMessage,
) error {
	root, remote, expectedURL, targetURL, err := RemoteUpdateStep(step)
	if err != nil {
		return err
	}
	var evidence gitprovider.RemoteUpdateEvidence
	if err := json.Unmarshal(recorded, &evidence); err != nil {
		return err
	}
	if evidence.Remote != remote ||
		evidence.ExpectedURL != expectedURL || evidence.TargetURL != targetURL || evidence.State != "target" {
		return errors.New("recorded remote update evidence differs from the immutable plan")
	}
	current, err := handler.Git.ObserveRemoteUpdate(ctx, root, remote, expectedURL, targetURL)
	if err != nil {
		return err
	}
	if current.State != "target" || current.WorktreeRoot != evidence.WorktreeRoot {
		return errors.New("current Git remote does not match the operation target")
	}
	return nil
}

func RemoteUpdateStep(step operations.Step) (string, string, string, string, error) {
	if step.Action != UpdateRemoteAction {
		return "", "", "", "", errors.New("unexpected remote update action")
	}
	raw, ok := step.Parameters["remote_update"].(map[string]any)
	if !ok {
		return "", "", "", "", errors.New("remote update parameters are missing")
	}
	root, _ := raw["root"].(string)
	remote, _ := raw["remote"].(string)
	expectedURL, _ := raw["expected_url"].(string)
	targetURL, _ := raw["target_url"].(string)
	if root == "" || remote == "" || expectedURL == "" || targetURL == "" {
		return "", "", "", "", fmt.Errorf("remote update parameters are incomplete")
	}
	return root, remote, expectedURL, targetURL, nil
}
