package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

// PublishGitHubReleaseAction publishes one immutable module version through the
// GitHub Release provider. Unlike the local version-tag action, this is a single
// atomic provider step: GitHub's create-release API creates the immutable tag AND
// the release together from the exact target commit, so there is no separate
// local or remote git-tag push.
const PublishGitHubReleaseAction = "publish-github-release"

// GitHubReleaseMutator is the narrow contract the handler needs from a repository
// bound GitHub mutator. It is satisfied by *github.RepositoryMutator.
type GitHubReleaseMutator interface {
	CreateRelease(context.Context, githubprovider.ReleaseInput) (githubprovider.Release, githubprovider.MutationMeta, error)
	UploadReleaseAsset(context.Context, int64, githubprovider.ReleaseAssetInput) (githubprovider.ReleaseAsset, githubprovider.MutationMeta, error)
	UpdateRelease(context.Context, int64, githubprovider.ReleaseInput) (githubprovider.Release, githubprovider.MutationMeta, error)
	DeleteRelease(context.Context, int64) (githubprovider.MutationMeta, error)
	GetReleaseByTag(context.Context, string) (githubprovider.Release, error)
	ListReleaseAssets(context.Context, int64) ([]githubprovider.ReleaseAsset, error)
	Scope() githubprovider.RepositoryMutationScope
}

// GitHubReleaseObserver is the read-only contract verification needs. It is
// satisfied by the repository-scoped read client, so a verify re-reads live
// provider state without holding mutation authority.
type GitHubReleaseObserver interface {
	GetReleaseByTag(context.Context, string) (githubprovider.Release, error)
	ListReleaseAssets(context.Context, int64) ([]githubprovider.ReleaseAsset, error)
	Scope() githubprovider.RepositoryMutationScope
}

// PublishGitHubReleaseHandler applies and verifies one immutable GitHub release
// for a module version.
//
// The mutator is required to apply. Verification instead uses the read-only
// observer: recorded evidence alone only proves what GDS wrote down at apply
// time, so a release that was later deleted, retagged, or had an asset replaced
// would still verify. Re-reading the live tag, release metadata, and every asset
// is what makes verification evidence about the provider rather than about the
// journal.
type PublishGitHubReleaseHandler struct {
	mutator  GitHubReleaseMutator
	observer GitHubReleaseObserver
}

// githubReleaseEvidence is the recorded after-state of one applied GitHub
// release. It is persisted as the step's after evidence and re-validated on
// verification against the exact immutable plan.
type githubReleaseEvidence struct {
	Release githubprovider.Release        `json:"release"`
	Meta    *githubprovider.MutationMeta  `json:"meta,omitempty"`
	Assets  []githubprovider.ReleaseAsset `json:"assets"`
}

// NewPublishGitHubReleaseHandler binds a mutator for the apply path. The mutator
// also observes, so an apply verifies its own result against live state.
func NewPublishGitHubReleaseHandler(mutator GitHubReleaseMutator) (*PublishGitHubReleaseHandler, error) {
	handler := &PublishGitHubReleaseHandler{mutator: mutator}
	if mutator != nil {
		handler.observer = mutator
	}
	return handler, nil
}

// NewVerifyGitHubReleaseHandler binds a read-only observer for the standalone
// verify path. It cannot apply, so verification never requires mutation
// authority, and it still proves the live release rather than the journal.
func NewVerifyGitHubReleaseHandler(observer GitHubReleaseObserver) (*PublishGitHubReleaseHandler, error) {
	if observer == nil {
		return nil, errors.New("GitHub release verification requires a provider observer")
	}
	return &PublishGitHubReleaseHandler{observer: observer}, nil
}

func (handler *PublishGitHubReleaseHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	parameters, err := moduleReleaseParameters(step, PublishGitHubReleaseAction)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if handler.mutator == nil {
		return operations.ApplyEvidence{}, errors.New("GitHub release mutation writer is unavailable")
	}
	tagName, releaseName, err := githubReleaseTagAndName(parameters)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	release, _, err := handler.mutator.CreateRelease(ctx, githubprovider.ReleaseInput{
		TagName:         tagName,
		TargetCommitish: parameters.CommitOID,
		Name:            releaseName,
		Draft:           true,
		MakeLatest:      "false",
	})
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if release.TagName != tagName || release.TargetCommitish != parameters.CommitOID ||
		!release.Draft || release.ID < 1 {
		return operations.ApplyEvidence{}, errors.New(
			"GitHub release provider response does not echo the exact immutable plan",
		)
	}
	assets := make([]githubprovider.ReleaseAsset, 0, len(parameters.Assets))
	cleanup := func(cause error) error {
		_, cleanupErr := handler.mutator.DeleteRelease(context.WithoutCancel(ctx), release.ID)
		if cleanupErr != nil {
			return errors.Join(cause, fmt.Errorf("draft release %d cleanup failed; explicit recovery is required: %w", release.ID, cleanupErr))
		}
		return cause
	}
	for _, asset := range parameters.Assets {
		bytes, readErr := os.ReadFile(asset.Path)
		if readErr != nil || int64(len(bytes)) != asset.Size || fmt.Sprintf("sha256:%x", sha256.Sum256(bytes)) != asset.SHA256 {
			return operations.ApplyEvidence{}, cleanup(errors.Join(readErr, errors.New("release asset bytes differ from the immutable plan")))
		}
		uploaded, _, uploadErr := handler.mutator.UploadReleaseAsset(ctx, release.ID, githubprovider.ReleaseAssetInput{
			Name: asset.Name, Bytes: bytes, SHA256: asset.SHA256,
		})
		if uploadErr != nil {
			return operations.ApplyEvidence{}, cleanup(uploadErr)
		}
		assets = append(assets, uploaded)
	}
	published, publishMeta, err := handler.mutator.UpdateRelease(ctx, release.ID, githubprovider.ReleaseInput{
		TagName: tagName, TargetCommitish: parameters.CommitOID, Name: releaseName,
		MakeLatest: "true",
	})
	if err != nil {
		return operations.ApplyEvidence{}, cleanup(err)
	}
	if published.Draft || published.ID != release.ID || !published.Immutable {
		return operations.ApplyEvidence{}, errors.New("published GitHub release is not exact and immutable")
	}
	evidence := githubReleaseEvidence{Release: published, Meta: &publishMeta, Assets: assets}
	return operations.ApplyEvidence{After: evidence}, nil
}

func (handler *PublishGitHubReleaseHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	parameters, err := moduleReleaseParameters(step, PublishGitHubReleaseAction)
	if err != nil {
		return err
	}
	tagName, _, err := githubReleaseTagAndName(parameters)
	if err != nil {
		return err
	}
	var evidence githubReleaseEvidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &evidence) != nil {
		return errors.New("GitHub release after evidence is missing or invalid")
	}
	if evidence.Release.TagName != tagName || evidence.Release.TargetCommitish != parameters.CommitOID ||
		evidence.Release.Draft || !evidence.Release.Immutable || evidence.Release.ID < 1 ||
		len(evidence.Assets) != len(parameters.Assets) {
		return errors.New("GitHub release after evidence differs from the exact step")
	}
	for index, planned := range parameters.Assets {
		actual := evidence.Assets[index]
		if actual.Name != planned.Name || actual.Size != planned.Size || actual.SHA256 != planned.SHA256 ||
			actual.ID < 1 || actual.State != "uploaded" {
			return errors.New("GitHub release asset evidence differs from the exact step")
		}
	}
	// Live re-read is mandatory. Accepting recorded evidence alone would let a
	// deleted or rewritten release verify successfully.
	if handler.observer == nil {
		return errors.New("GitHub release verification requires a provider observer")
	}
	scope := handler.observer.Scope()
	expectedPath := "/" + scope.Owner + "/" + scope.Name + "/releases/tag/" + tagName
	if !strings.HasSuffix(evidence.Release.HTMLURL, expectedPath) {
		return errors.New("GitHub release evidence is bound to a different repository scope")
	}
	observedRelease, err := handler.observer.GetReleaseByTag(ctx, tagName)
	if err != nil || observedRelease.ID != evidence.Release.ID || observedRelease.TagName != tagName ||
		observedRelease.TargetCommitish != parameters.CommitOID || observedRelease.Draft || !observedRelease.Immutable {
		return errors.Join(errors.New("published GitHub release no longer matches recorded evidence"), err)
	}
	observedAssets, err := handler.observer.ListReleaseAssets(ctx, evidence.Release.ID)
	if err != nil || len(observedAssets) != len(evidence.Assets) {
		return errors.Join(errors.New("published GitHub release asset inventory differs from recorded evidence"), err)
	}
	// Key by name rather than by position: the provider's listing order is not the
	// upload order, so comparing index-wise would report drift for an identical
	// inventory that merely came back in a different sequence.
	observedByName := make(map[string]githubprovider.ReleaseAsset, len(observedAssets))
	for _, asset := range observedAssets {
		if _, duplicate := observedByName[asset.Name]; duplicate {
			return fmt.Errorf("published GitHub release repeats asset %q", asset.Name)
		}
		observedByName[asset.Name] = asset
	}
	recordedByName := make(map[string]githubprovider.ReleaseAsset, len(evidence.Assets))
	for _, asset := range evidence.Assets {
		recordedByName[asset.Name] = asset
	}
	// Compare against the immutable plan, not only against the evidence: the
	// evidence was written by the same apply whose result is in question.
	for _, planned := range parameters.Assets {
		observed, found := observedByName[planned.Name]
		if !found || observed.Size != planned.Size || observed.SHA256 != planned.SHA256 ||
			observed.State != "uploaded" {
			return fmt.Errorf("published GitHub release asset %q differs from the immutable plan", planned.Name)
		}
		recorded := recordedByName[planned.Name]
		// GitHub rewrites a draft asset's download URL from the temporary
		// `untagged-*` namespace to the final tag namespace when the release is
		// published. That URL transition is expected; the immutable identity and
		// content fields must remain exact.
		if observed.ID != recorded.ID || observed.Name != recorded.Name ||
			observed.Size != recorded.Size || observed.State != recorded.State ||
			observed.SHA256 != recorded.SHA256 {
			return fmt.Errorf("published GitHub release asset %q differs from recorded evidence", planned.Name)
		}
	}
	return nil
}

// githubReleaseTagAndName derives the v-prefixed tag name and a deterministic,
// bounded release name from the immutable release parameters. The tag name is
// taken from the already validated tag_ref so it exactly matches the canonical
// version-tag contract enforced elsewhere.
func githubReleaseTagAndName(parameters versionTagStepParameters) (string, string, error) {
	tagName := strings.TrimPrefix(parameters.TagRef, "refs/tags/")
	if tagName == parameters.TagRef || tagName == "" {
		return "", "", fmt.Errorf("GitHub release tag ref is not a canonical version tag")
	}
	return tagName, "Release " + tagName, nil
}
