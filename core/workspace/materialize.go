package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const MaterializeCheckoutAction = "materialize-workspace-checkout"

type MaterializeParameters struct {
	WorkspaceRoot string
	TargetPath    string
	SourcePath    string
	BranchRef     string
	HeadOID       string
	Filter        string
	Mode          string
	DeviceID      string
	RepositoryID  string
	AnchorDigest  string
	AnchorContent string
	DevicePath    string
	DeviceDigest  string
}

func MaterializeStepParameters(
	placement Placement,
	source gitprovider.LocalCloneSource,
	filter string,
	anchorContent string,
	anchorDigest string,
	device DeviceCandidate,
) map[string]any {
	return map[string]any{"checkout_materialization": map[string]any{
		"workspace_root": placement.WorkspaceRoot, "target_path": placement.TargetPath,
		"source_path": source.Path, "branch_ref": source.BranchRef,
		"head_oid": source.HeadOID, "filter": filter, "mode": placement.Mode,
		"device_id": placement.DeviceID, "repository_id": placement.RepositoryID,
		"anchor_content": anchorContent, "anchor_digest": anchorDigest,
		"device_descriptor_path": device.Path, "device_descriptor_digest": device.Digest,
	}}
}

type MaterializeHandler struct {
	runner *gitprovider.MutationRunner
}

func NewMaterializeHandler(runner *gitprovider.MutationRunner) (*MaterializeHandler, error) {
	if runner == nil {
		return nil, errors.New("workspace materialization handler requires a Git mutation runner")
	}
	return &MaterializeHandler{runner: runner}, nil
}

func (handler *MaterializeHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := StepMaterializeParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.runner.MaterializeLocalCheckout(
		ctx, parameters.WorkspaceRoot, parameters.TargetPath,
		gitprovider.LocalCloneSource{
			Path: parameters.SourcePath, BranchRef: parameters.BranchRef, HeadOID: parameters.HeadOID,
		}, parameters.HeadOID, parameters.Filter,
		[]gitprovider.ExpectedCheckoutFile{{Path: anchor.Path, Digest: parameters.AnchorDigest}},
	)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *MaterializeHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := StepMaterializeParameters(step)
	if err != nil {
		return err
	}
	var expected gitprovider.CheckoutMaterializationEvidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil {
		return errors.New("workspace materialization after evidence is missing or invalid")
	}
	observed, err := handler.runner.ObserveMaterializedCheckout(
		ctx, parameters.WorkspaceRoot, parameters.TargetPath,
		gitprovider.LocalCloneSource{
			Path: parameters.SourcePath, BranchRef: parameters.BranchRef, HeadOID: parameters.HeadOID,
		}, parameters.HeadOID, parameters.Filter,
		[]gitprovider.ExpectedCheckoutFile{{Path: anchor.Path, Digest: parameters.AnchorDigest}},
	)
	if err != nil {
		return err
	}
	if !equalCheckoutEvidence(expected, observed) {
		return errors.New("workspace checkout no longer matches recorded evidence")
	}
	return nil
}

func StepMaterializeParameters(step operations.Step) (MaterializeParameters, error) {
	if step.Action != MaterializeCheckoutAction {
		return MaterializeParameters{}, fmt.Errorf("unexpected workspace action %q", step.Action)
	}
	raw, ok := step.Parameters["checkout_materialization"].(map[string]any)
	if !ok {
		return MaterializeParameters{}, errors.New("workspace materialization parameters are missing")
	}
	result := MaterializeParameters{}
	result.WorkspaceRoot, _ = raw["workspace_root"].(string)
	result.TargetPath, _ = raw["target_path"].(string)
	result.SourcePath, _ = raw["source_path"].(string)
	result.BranchRef, _ = raw["branch_ref"].(string)
	result.HeadOID, _ = raw["head_oid"].(string)
	result.Filter, _ = raw["filter"].(string)
	result.Mode, _ = raw["mode"].(string)
	result.DeviceID, _ = raw["device_id"].(string)
	result.RepositoryID, _ = raw["repository_id"].(string)
	result.AnchorDigest, _ = raw["anchor_digest"].(string)
	result.AnchorContent, _ = raw["anchor_content"].(string)
	result.DevicePath, _ = raw["device_descriptor_path"].(string)
	result.DeviceDigest, _ = raw["device_descriptor_digest"].(string)
	if result.WorkspaceRoot == "" || !filepath.IsAbs(result.WorkspaceRoot) ||
		filepath.Clean(result.WorkspaceRoot) != result.WorkspaceRoot || result.TargetPath == "" ||
		!filepath.IsAbs(result.TargetPath) || filepath.Clean(result.TargetPath) != result.TargetPath ||
		result.SourcePath == "" || !filepath.IsAbs(result.SourcePath) ||
		(result.Filter != "full" && result.Filter != "blob-none") ||
		(result.Mode != "active" && result.Mode != "reference" && result.Mode != "ephemeral") ||
		result.RepositoryID != step.RepositoryID || result.DeviceID == "" ||
		result.BranchRef == "" || result.HeadOID == "" || result.AnchorContent == "" ||
		len(result.AnchorContent) > 512<<10 || len(result.AnchorDigest) != 71 ||
		result.DevicePath == "" || !filepath.IsAbs(result.DevicePath) || len(result.DeviceDigest) != 71 ||
		fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(result.AnchorContent))) != result.AnchorDigest {
		return MaterializeParameters{}, errors.New("workspace materialization parameters are invalid")
	}
	return result, nil
}

func equalCheckoutEvidence(
	left gitprovider.CheckoutMaterializationEvidence,
	right gitprovider.CheckoutMaterializationEvidence,
) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftRaw) == string(rightRaw)
}
