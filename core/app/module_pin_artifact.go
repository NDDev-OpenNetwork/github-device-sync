package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

// ModulePinArtifact records publication identity, not mutable release prose or
// observation timestamps. Every field participates in the plan precondition.
type ModulePinArtifact struct {
	Version           string                        `json:"version"`
	Tag               gitprovider.VersionArtifact   `json:"tag"`
	ManifestDigest    string                        `json:"manifest_digest"`
	ReleaseID         int64                         `json:"release_id,omitempty"`
	ReleaseImmutable  bool                          `json:"release_immutable,omitempty"`
	ReleasePrerelease bool                          `json:"release_prerelease,omitempty"`
	Assets            []githubprovider.ReleaseAsset `json:"assets,omitempty"`
}

func (services *Services) observeModulePinArtifact(ctx context.Context, moduleRoot, version, runtimeConfig string) (*ModulePinArtifact, error) {
	estateRoot, anchor, findings := services.policyInputs(ctx, moduleRoot)
	if len(findings) != 0 || anchor.Module == nil || anchor.Module.PinPolicy != "version-tag" || !hasRole(anchor.Repository.Roles, "module") {
		return nil, errors.New("version artifact module policy is not proven")
	}
	tagRef, err := gitprovider.VersionTagRefWithStyle(version, anchor.Release.TagStyle)
	if err != nil {
		return nil, err
	}
	tag, err := services.GitMutations.ObserveVersionArtifact(ctx, moduleRoot, tagRef)
	if err != nil {
		return nil, err
	}
	status, err := services.Git.InspectStatus(ctx, moduleRoot)
	if err != nil || !checkoutStatusIsClean(status) || status.Head.OID != tag.CommitOID {
		return nil, errors.New("module checkout must be clean at the selected published version commit")
	}
	manifest, err := fileDigest(filepath.Join(moduleRoot, ".gds", "repository.yaml"))
	if err != nil {
		return nil, err
	}
	artifact := &ModulePinArtifact{Version: version, Tag: tag, ManifestDigest: manifest}
	if anchor.Module.Publication.GitHubRelease != "required" && anchor.Release.Mode != "github-release" {
		return artifact, nil
	}
	desired, findings := estate.Load(estateRoot, services.Schemas)
	if len(findings) != 0 {
		return nil, errors.New("version artifact estate is not proven")
	}
	config, err := githubruntime.Load(runtimeConfig, desired, services.Schemas)
	if err != nil {
		return nil, err
	}
	readers, err := githubruntime.BuildReaders(config, desired, services.GitHubRuntimeBuildOptions)
	if err != nil {
		return nil, err
	}
	reader, found := readers[anchor.Provider.Installation]
	if !found {
		return nil, errors.New("module installation has no configured GitHub reader")
	}
	repository, _, _, err := reader.GetRepository(ctx, anchor.Provider.Owner, anchor.Provider.Name, "")
	if err != nil || repository.ID != anchor.Provider.RepositoryID {
		return nil, errors.New("version artifact repository identity is not proven")
	}
	tagName := strings.TrimPrefix(tagRef, "refs/tags/")
	providerTag, _, found, err := reader.GetVersionTagRefOptional(ctx, anchor.Provider.Owner, anchor.Provider.Name, tagName)
	if err != nil || !found || providerTag.SHA != tag.TagOID {
		return nil, errors.New("GitHub tag does not match the selected origin artifact")
	}
	release, err := reader.GetReleaseByTag(ctx, anchor.Provider.Owner, anchor.Provider.Name, tagName)
	if err != nil || release.Draft {
		return nil, errors.New("selected artifact requires a published GitHub release")
	}
	assets, err := reader.ListReleaseAssets(ctx, anchor.Provider.Owner, anchor.Provider.Name, release.ID)
	if err != nil || len(assets) == 0 {
		return nil, errors.New("selected release requires a complete digest-bearing asset inventory")
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	assetIDs := map[int64]bool{}
	for i, asset := range assets {
		parsed, parseErr := url.Parse(asset.BrowserDownloadURL)
		expectedPath := "/" + anchor.Provider.Owner + "/" + anchor.Provider.Name + "/releases/download/" + tagName + "/" + asset.Name
		if (i > 0 && assets[i-1].Name == asset.Name) || assetIDs[asset.ID] || parseErr != nil || parsed.Path != expectedPath {
			return nil, errors.New("release asset identity is ambiguous or belongs to another tag")
		}
		assetIDs[asset.ID] = true
	}
	artifact.ReleaseID, artifact.ReleaseImmutable, artifact.ReleasePrerelease, artifact.Assets = release.ID, release.Immutable, release.Prerelease, assets
	return artifact, nil
}

// Artifact verification is part of the journaled handler: both immediate
// postconditions and later explicit verify re-read the selected publication.
type modulePinArtifactHandler struct {
	operations.ActionHandler
	services         *Services
	assessment       ModulePinAssessment
	consumerManifest string
	runtimeConfig    string
}

func (handler modulePinArtifactHandler) Verify(ctx context.Context, step operations.Step, after json.RawMessage) error {
	if err := handler.ActionHandler.Verify(ctx, step, after); err != nil {
		return err
	}
	manifest, err := fileDigest(filepath.Join(handler.assessment.ConsumerRoot, ".gds", "repository.yaml"))
	if err != nil || manifest != handler.consumerManifest {
		return errors.New("consumer relationship manifest changed after the artifact plan")
	}
	current, err := handler.services.observeModulePinArtifact(ctx, handler.assessment.ModuleRoot, handler.assessment.Artifact.Version, handler.runtimeConfig)
	if err != nil {
		return err
	}
	want, err := canonicaljson.Digest(handler.assessment.Artifact)
	if err != nil {
		return err
	}
	got, err := canonicaljson.Digest(current)
	if err != nil || got != want {
		return errors.New("version artifact no longer matches the immutable pin plan")
	}
	return nil
}

func modulePinArtifactFinding(err error) domain.Finding {
	return modulePinFinding("GDS_MODULE_PIN_ARTIFACT_NOT_PROVEN", err.Error())
}

func modulePinVersion(assessment ModulePinAssessment) string {
	if assessment.Artifact == nil {
		return ""
	}
	return assessment.Artifact.Version
}

func (services *Services) modulePinHandler(handler operations.ActionHandler, assessment ModulePinAssessment, plan operations.Plan, runtimeConfig string) operations.ActionHandler {
	if assessment.Artifact == nil {
		return handler
	}
	return modulePinArtifactHandler{ActionHandler: handler, services: services, assessment: assessment, consumerManifest: plan.Preconditions[0].ManifestDigest, runtimeConfig: runtimeConfig}
}
