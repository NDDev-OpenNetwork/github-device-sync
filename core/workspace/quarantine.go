package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const QuarantineCheckoutAction = "quarantine-checkout"

type QuarantineParameters struct {
	WorkspaceRoot  string
	CheckoutPath   string
	StateRoot      string
	QuarantinePath string
	HeadOID        string
	BranchRef      string
	AnchorDigest   string
	DeviceID       string
	RepositoryID   string
	DevicePath     string
	DeviceDigest   string
}

func QuarantineStepParameters(
	placement Placement,
	quarantinePath string,
	headOID string,
	branchRef string,
	anchorDigest string,
	device DeviceCandidate,
) map[string]any {
	return map[string]any{"checkout_quarantine": map[string]any{
		"workspace_root": placement.WorkspaceRoot, "checkout_path": placement.TargetPath,
		"state_root": placement.StateRoot, "quarantine_path": quarantinePath,
		"head_oid": headOID, "branch_ref": branchRef, "anchor_digest": anchorDigest,
		"device_id": placement.DeviceID, "repository_id": placement.RepositoryID,
		"device_descriptor_path": device.Path, "device_descriptor_digest": device.Digest,
	}}
}

type QuarantineHandler struct {
	runner *gitprovider.MutationRunner
}

func NewQuarantineHandler(runner *gitprovider.MutationRunner) (*QuarantineHandler, error) {
	if runner == nil {
		return nil, errors.New("checkout quarantine handler requires a Git mutation runner")
	}
	return &QuarantineHandler{runner: runner}, nil
}

func (handler *QuarantineHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := StepQuarantineParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.runner.QuarantineCheckout(
		ctx, parameters.WorkspaceRoot, parameters.CheckoutPath,
		parameters.StateRoot, parameters.QuarantinePath, parameters.HeadOID,
		parameters.BranchRef, parameters.AnchorDigest,
	)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *QuarantineHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := StepQuarantineParameters(step)
	if err != nil {
		return err
	}
	var expected gitprovider.CheckoutQuarantineEvidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil {
		return errors.New("checkout quarantine after evidence is missing or invalid")
	}
	observed, err := handler.runner.ObserveQuarantinedCheckout(
		ctx, parameters.WorkspaceRoot, parameters.CheckoutPath,
		parameters.StateRoot, parameters.QuarantinePath, parameters.HeadOID,
		parameters.BranchRef, parameters.AnchorDigest,
	)
	if err != nil {
		return err
	}
	left, _ := json.Marshal(expected)
	right, _ := json.Marshal(observed)
	if string(left) != string(right) {
		return errors.New("quarantined checkout no longer matches recorded evidence")
	}
	return nil
}

func StepQuarantineParameters(step operations.Step) (QuarantineParameters, error) {
	if step.Action != QuarantineCheckoutAction {
		return QuarantineParameters{}, fmt.Errorf("unexpected checkout quarantine action %q", step.Action)
	}
	raw, ok := step.Parameters["checkout_quarantine"].(map[string]any)
	if !ok {
		return QuarantineParameters{}, errors.New("checkout quarantine parameters are missing")
	}
	result := QuarantineParameters{}
	result.WorkspaceRoot, _ = raw["workspace_root"].(string)
	result.CheckoutPath, _ = raw["checkout_path"].(string)
	result.StateRoot, _ = raw["state_root"].(string)
	result.QuarantinePath, _ = raw["quarantine_path"].(string)
	result.HeadOID, _ = raw["head_oid"].(string)
	result.BranchRef, _ = raw["branch_ref"].(string)
	result.AnchorDigest, _ = raw["anchor_digest"].(string)
	result.DeviceID, _ = raw["device_id"].(string)
	result.RepositoryID, _ = raw["repository_id"].(string)
	result.DevicePath, _ = raw["device_descriptor_path"].(string)
	result.DeviceDigest, _ = raw["device_descriptor_digest"].(string)
	if result.WorkspaceRoot == "" || !filepath.IsAbs(result.WorkspaceRoot) ||
		result.CheckoutPath == "" || !filepath.IsAbs(result.CheckoutPath) ||
		result.StateRoot == "" || !filepath.IsAbs(result.StateRoot) ||
		result.QuarantinePath == "" || !filepath.IsAbs(result.QuarantinePath) ||
		result.HeadOID == "" || result.BranchRef == "" || len(result.AnchorDigest) != 71 ||
		result.DeviceID == "" || result.RepositoryID != step.RepositoryID ||
		result.DevicePath == "" || !filepath.IsAbs(result.DevicePath) || len(result.DeviceDigest) != 71 {
		return QuarantineParameters{}, errors.New("checkout quarantine parameters are invalid")
	}
	return result, nil
}
