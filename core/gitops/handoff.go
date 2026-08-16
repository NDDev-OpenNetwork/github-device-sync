package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const CheckpointHandoffAction = "checkpoint-handoff"

type HandoffHandler struct {
	runner *gitprovider.MutationRunner
}

func NewHandoffHandler(runner *gitprovider.MutationRunner) (*HandoffHandler, error) {
	if runner == nil {
		return nil, errors.New("handoff handler requires a Git mutation runner")
	}
	return &HandoffHandler{runner: runner}, nil
}

func (handler *HandoffHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := handoffParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.runner.CommitAndPushHandoff(
		ctx, parameters.WorktreeRoot, parameters.BranchRef,
		parameters.ExpectedHeadOID, parameters.RemoteRef,
		parameters.ExpectedRemoteOID, parameters.Files, parameters.Message,
		parameters.Author, parameters.CommitTime,
	)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *HandoffHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := handoffParameters(step)
	if err != nil {
		return err
	}
	var expected gitprovider.HandoffEvidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil {
		return errors.New("handoff after evidence is missing or invalid")
	}
	if expected.WorktreeRoot != parameters.WorktreeRoot ||
		expected.BranchRef != parameters.BranchRef || expected.RemoteRef != parameters.RemoteRef ||
		expected.HeadOID == "" || expected.HeadOID != expected.RemoteOID ||
		!equalFiles(expected.Files, parameters.Files) {
		return errors.New("handoff after evidence differs from the exact step")
	}
	observed, err := handler.runner.ObserveHandoff(
		ctx, parameters.WorktreeRoot, parameters.BranchRef,
		parameters.RemoteRef, parameters.Files,
	)
	if err != nil {
		return err
	}
	if observed.HeadOID != expected.HeadOID || observed.RemoteOID != expected.RemoteOID ||
		observed.BranchRef != expected.BranchRef || observed.RemoteRef != expected.RemoteRef ||
		!equalFiles(observed.Files, expected.Files) {
		return errors.New("handoff no longer matches recorded evidence")
	}
	return nil
}

type handoffStepParameters struct {
	WorktreeRoot         string
	BranchRef            string
	RemoteRef            string
	ExpectedHeadOID      string
	ExpectedRemoteOID    string
	Files                []string
	Message              string
	Author               gitprovider.CommitIdentity
	CommitTime           time.Time
	RemoteEvidenceDigest string
}

func handoffParameters(step operations.Step) (handoffStepParameters, error) {
	if step.Action != CheckpointHandoffAction {
		return handoffStepParameters{}, fmt.Errorf("unexpected handoff action %q", step.Action)
	}
	raw, ok := step.Parameters["handoff"].(map[string]any)
	if !ok {
		return handoffStepParameters{}, errors.New("handoff parameters are missing")
	}
	parameters := handoffStepParameters{}
	parameters.WorktreeRoot, _ = raw["worktree_root"].(string)
	parameters.BranchRef, _ = raw["branch_ref"].(string)
	parameters.RemoteRef, _ = raw["remote_ref"].(string)
	parameters.ExpectedHeadOID, _ = raw["expected_head_oid"].(string)
	parameters.ExpectedRemoteOID, _ = raw["expected_remote_oid"].(string)
	parameters.Message, _ = raw["message"].(string)
	parameters.RemoteEvidenceDigest, _ = raw["remote_evidence_digest"].(string)
	commitTime, _ := raw["commit_time"].(string)
	parameters.CommitTime, _ = time.Parse(time.RFC3339, commitTime)
	author, _ := raw["author"].(map[string]any)
	parameters.Author.Name, _ = author["name"].(string)
	parameters.Author.Email, _ = author["email"].(string)
	fileValues, _ := raw["files"].([]any)
	for _, value := range fileValues {
		file, _ := value.(map[string]any)
		path, _ := file["path"].(string)
		parameters.Files = append(parameters.Files, path)
	}
	sort.Strings(parameters.Files)
	if parameters.WorktreeRoot == "" || !filepath.IsAbs(parameters.WorktreeRoot) ||
		filepath.Clean(parameters.WorktreeRoot) != parameters.WorktreeRoot ||
		parameters.BranchRef == "" || parameters.RemoteRef == "" ||
		parameters.ExpectedHeadOID == "" || parameters.ExpectedRemoteOID == "" ||
		len(parameters.Files) == 0 || parameters.Message == "" ||
		parameters.Author.Name == "" || parameters.Author.Email == "" ||
		parameters.CommitTime.IsZero() || parameters.RemoteEvidenceDigest == "" {
		return handoffStepParameters{}, errors.New("handoff parameters are incomplete")
	}
	return parameters, nil
}

func equalFiles(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
