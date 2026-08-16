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

const PublishVersionTagAction = "publish-version-tag"

type PublishVersionTagHandler struct {
	runner *gitprovider.MutationRunner
}

func NewPublishVersionTagHandler(runner *gitprovider.MutationRunner) (*PublishVersionTagHandler, error) {
	if runner == nil {
		return nil, errors.New("version tag handler requires a Git mutation runner")
	}
	return &PublishVersionTagHandler{runner: runner}, nil
}

func (handler *PublishVersionTagHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := versionTagParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	report, err := handler.runner.PublishVersionTag(
		ctx, parameters.ModuleRoot, parameters.TagRef, parameters.CommitOID,
	)
	return operations.ApplyEvidence{Before: report.Before, After: report.After}, err
}

func (handler *PublishVersionTagHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := versionTagParameters(step)
	if err != nil {
		return err
	}
	var expected gitprovider.TagEvidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil {
		return errors.New("version tag after evidence is missing or invalid")
	}
	if expected.WorktreeRoot != parameters.ModuleRoot || expected.TagRef != parameters.TagRef ||
		expected.CommitOID != parameters.CommitOID || expected.LocalOID != parameters.CommitOID ||
		expected.RemoteOID != parameters.CommitOID {
		return errors.New("version tag after evidence differs from the exact step")
	}
	observed, err := handler.runner.ObserveVersionTag(
		ctx, parameters.ModuleRoot, parameters.TagRef, parameters.CommitOID,
	)
	if err != nil {
		return err
	}
	if observed != expected {
		return errors.New("version tag no longer matches recorded evidence")
	}
	return nil
}

type versionTagStepParameters struct {
	ModuleRoot string
	Version    string
	TagStyle   string
	TagRef     string
	CommitOID  string
	Assets     []releaseAssetParameters
}

type releaseAssetParameters struct {
	Path   string
	Name   string
	Size   int64
	SHA256 string
}

func versionTagParameters(step operations.Step) (versionTagStepParameters, error) {
	return moduleReleaseParameters(step, PublishVersionTagAction)
}

// moduleReleaseParameters decodes and fully validates the shared immutable
// module-release step parameters (module_root, version, tag_ref, commit_oid) for
// whichever release action the plan declares. Both the local version-tag handler
// and the GitHub Release handler consume the exact same bounded parameter shape,
// so the only difference is the action name the step must carry.
func moduleReleaseParameters(step operations.Step, expectedAction string) (versionTagStepParameters, error) {
	if step.Action != expectedAction {
		return versionTagStepParameters{}, fmt.Errorf("unexpected module release action %q", step.Action)
	}
	raw, ok := step.Parameters["module_release"].(map[string]any)
	if !ok {
		return versionTagStepParameters{}, errors.New("module release parameters are missing")
	}
	result := versionTagStepParameters{}
	result.ModuleRoot, _ = raw["module_root"].(string)
	result.Version, _ = raw["version"].(string)
	result.TagStyle, _ = raw["tag_style"].(string)
	result.TagRef, _ = raw["tag_ref"].(string)
	result.CommitOID, _ = raw["commit_oid"].(string)
	assetValues, assetsOK := raw["assets"].([]any)
	if !assetsOK || len(assetValues) > 16 {
		return versionTagStepParameters{}, errors.New("module release asset parameters are invalid")
	}
	for _, value := range assetValues {
		entry, ok := value.(map[string]any)
		if !ok {
			return versionTagStepParameters{}, errors.New("module release asset parameters are invalid")
		}
		asset := releaseAssetParameters{}
		asset.Path, _ = entry["path"].(string)
		asset.Name, _ = entry["name"].(string)
		asset.SHA256, _ = entry["sha256"].(string)
		size, ok := entry["size"].(float64)
		asset.Size = int64(size)
		if !ok || float64(asset.Size) != size || !filepath.IsAbs(asset.Path) ||
			filepath.Base(asset.Path) != asset.Name || asset.Size < 1 || asset.Size > 64<<20 ||
			!strings.HasPrefix(asset.SHA256, "sha256:") || len(asset.SHA256) != 71 {
			return versionTagStepParameters{}, errors.New("module release asset parameters are invalid")
		}
		result.Assets = append(result.Assets, asset)
	}
	expectedTag, err := gitprovider.VersionTagRefWithStyle(result.Version, result.TagStyle)
	if result.ModuleRoot == "" || !filepath.IsAbs(result.ModuleRoot) ||
		filepath.Clean(result.ModuleRoot) != result.ModuleRoot || err != nil ||
		expectedTag != result.TagRef || result.CommitOID == "" {
		return versionTagStepParameters{}, errors.New("module release parameters are invalid")
	}
	return result, nil
}
