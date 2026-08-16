package fork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const DetachAction = "remove-fork-upstream-remote"

func ValidateDetach(
	current domain.RepositoryAnchor,
	candidate domain.RepositoryAnchor,
) []domain.Finding {
	if current.Repository.ID != candidate.Repository.ID || current.Fork == nil || candidate.Fork == nil ||
		current.Fork.Policy == "detached" || candidate.Fork.Policy != "detached" {
		return []domain.Finding{detachFinding(
			"GDS_FORK_DETACH_DELTA_INVALID",
			"Fork detach must preserve stable identity and move a non-detached fork to detached.",
		)}
	}
	normalized := candidate
	normalized.Fork = &domain.ForkPolicy{}
	*normalized.Fork = *candidate.Fork
	normalized.Fork.Policy = current.Fork.Policy
	if !reflect.DeepEqual(current, normalized) {
		return []domain.Finding{detachFinding(
			"GDS_FORK_DETACH_SCOPE_EXCEEDED",
			"Fork detach candidate changes facts other than the fork lifecycle policy.",
		)}
	}
	return nil
}

type DetachParameters struct {
	WorktreeRoot string
	Remote       string
	ExpectedURL  string
}

func DetachStepParameters(worktreeRoot string, expectedURL string) map[string]any {
	return map[string]any{"fork_detach": map[string]any{
		"worktree_root": worktreeRoot, "remote": "upstream", "expected_url": expectedURL,
	}}
}

type DetachHandler struct {
	runner *gitprovider.MutationRunner
}

func NewDetachHandler(runner *gitprovider.MutationRunner) (*DetachHandler, error) {
	if runner == nil {
		return nil, errors.New("fork detach handler requires a Git mutation runner")
	}
	return &DetachHandler{runner: runner}, nil
}

func (handler *DetachHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := StepDetachParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.runner.RemoveRemote(
		ctx, parameters.WorktreeRoot, parameters.Remote, parameters.ExpectedURL,
	)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *DetachHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := StepDetachParameters(step)
	if err != nil {
		return err
	}
	var expected gitprovider.RemoteRemovalEvidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil || expected.State != "missing" {
		return errors.New("fork detach after evidence is missing or invalid")
	}
	observed, err := handler.runner.ObserveRemoteRemoval(
		ctx, parameters.WorktreeRoot, parameters.Remote, parameters.ExpectedURL,
	)
	if err != nil || observed != expected {
		return errors.New("fork detach no longer matches recorded evidence")
	}
	return nil
}

func StepDetachParameters(step operations.Step) (DetachParameters, error) {
	if step.Action != DetachAction {
		return DetachParameters{}, fmt.Errorf("unexpected fork detach action %q", step.Action)
	}
	raw, ok := step.Parameters["fork_detach"].(map[string]any)
	if !ok {
		return DetachParameters{}, errors.New("fork detach parameters are missing")
	}
	result := DetachParameters{}
	result.WorktreeRoot, _ = raw["worktree_root"].(string)
	result.Remote, _ = raw["remote"].(string)
	result.ExpectedURL, _ = raw["expected_url"].(string)
	if result.WorktreeRoot == "" || !filepath.IsAbs(result.WorktreeRoot) ||
		result.Remote != "upstream" || result.ExpectedURL == "" {
		return DetachParameters{}, errors.New("fork detach parameters are invalid")
	}
	return result, nil
}

func detachFinding(code string, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
