package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// releaseSemverPattern mirrors the canonical SemVer contract enforced by the git
// provider's VersionTagRef so that a GitHub release tag name is validated with the
// exact same shape as a published immutable version tag ("v" + canonical SemVer).
const releaseSemverPattern = `(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
	`(?:-(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)` +
	`(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?` +
	`(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?`

var releaseTagNamePattern = regexp.MustCompile(`^v?` + releaseSemverPattern + `$`)
var releaseAssetSHA256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ReleaseInput is the bounded payload for POST /repos/{owner}/{repo}/releases.
// TargetCommitish must be the exact commit OID the immutable release is pinned to;
// GitHub creates the tag from that commit when TagName does not already exist.
type ReleaseInput struct {
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish"`
	Name            string `json:"name"`
	Body            string `json:"body,omitempty"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
	MakeLatest      string `json:"make_latest,omitempty"`
}

// Release is the normalized, bounded view of a created GitHub release.
type Release struct {
	ID              int64  `json:"id"`
	NodeID          string `json:"node_id"`
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish"`
	Name            string `json:"name"`
	Body            string `json:"body"`
	HTMLURL         string `json:"html_url"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
	Immutable       bool   `json:"immutable"`
	UploadURL       string `json:"upload_url,omitempty"`
}

type releaseResponse struct {
	ID              int64  `json:"id"`
	NodeID          string `json:"node_id"`
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish"`
	Name            string `json:"name"`
	Body            string `json:"body"`
	HTMLURL         string `json:"html_url"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
	Immutable       bool   `json:"immutable"`
	UploadURL       string `json:"upload_url"`
}

type ReleaseAssetInput struct {
	Name   string
	Bytes  []byte
	SHA256 string
}

type ReleaseAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	State              string `json:"state"`
	BrowserDownloadURL string `json:"browser_download_url"`
	SHA256             string `json:"sha256"`
}

type releaseAssetResponse struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	State              string `json:"state"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

func (mutator *RepositoryMutator) GetReleaseByTag(
	ctx context.Context, tagName string,
) (Release, error) {
	if !releaseTagNamePattern.MatchString(tagName) {
		return Release{}, fmt.Errorf("GitHub release tag name is invalid")
	}
	target, err := mutator.endpoint("releases/tags/" + url.PathEscape(tagName))
	if err != nil {
		return Release{}, err
	}
	response, err := mutator.factory.client.get(ctx, target, "")
	if err != nil {
		return Release{}, err
	}
	var raw releaseResponse
	if err := decodeJSON(response.Body, &raw); err != nil {
		return Release{}, invalidMutationResponse(response, err)
	}
	release, err := normalizeRelease(raw, mutator.scope.Owner, mutator.scope.Name)
	if err != nil || release.TagName != tagName {
		return Release{}, invalidMutationResponse(response, err)
	}
	return release, nil
}

func (mutator *RepositoryMutator) ListReleaseAssets(
	ctx context.Context, releaseID int64,
) ([]ReleaseAsset, error) {
	if releaseID < 1 {
		return nil, fmt.Errorf("GitHub release id is invalid")
	}
	target, err := mutator.endpoint(fmt.Sprintf("releases/%d/assets", releaseID))
	if err != nil {
		return nil, err
	}
	query := target.Query()
	query.Set("per_page", "100")
	query.Set("page", "1")
	target.RawQuery = query.Encode()
	response, err := mutator.factory.client.get(ctx, target, "")
	if err != nil {
		return nil, err
	}
	if strings.Contains(response.Header.Get("Link"), `rel="next"`) {
		return nil, fmt.Errorf("GitHub release asset inventory exceeds the bounded page")
	}
	var raw []releaseAssetResponse
	if err := decodeJSON(response.Body, &raw); err != nil || len(raw) > 100 {
		return nil, invalidMutationResponse(response, err)
	}
	assets := make([]ReleaseAsset, 0, len(raw))
	for _, item := range raw {
		asset, normalizeErr := normalizeObservedReleaseAsset(item, mutator.scope.Owner, mutator.scope.Name)
		if normalizeErr != nil {
			return nil, invalidMutationResponse(response, normalizeErr)
		}
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	return assets, nil
}

// CreateRelease publishes a GitHub release for the bound repository. The input is
// fully validated before any request is issued, and the decoded response is
// verified to echo the requested tag, target commit, and draft/prerelease intent
// before the release is returned.
func (mutator *RepositoryMutator) CreateRelease(
	ctx context.Context,
	input ReleaseInput,
) (Release, MutationMeta, error) {
	if err := validateReleaseInput(input); err != nil {
		return Release{}, MutationMeta{}, err
	}
	target, err := mutator.endpoint("releases")
	if err != nil {
		return Release{}, MutationMeta{}, err
	}
	response, meta, err := mutator.mutate(
		ctx, MutationRepositoryRelease, http.MethodPost, target, input,
	)
	if err != nil {
		return Release{}, meta, err
	}
	var raw releaseResponse
	decodeErr := decodeJSON(response.Body, &raw)
	release, normalizeErr := normalizeRelease(raw, mutator.scope.Owner, mutator.scope.Name)
	if decodeErr != nil || normalizeErr != nil ||
		raw.TagName != input.TagName || raw.TargetCommitish != input.TargetCommitish ||
		raw.Draft != input.Draft || raw.Prerelease != input.Prerelease {
		return Release{}, meta, invalidMutationResponse(response, errors.Join(decodeErr, normalizeErr))
	}
	return release, meta, nil
}

func (mutator *RepositoryMutator) UploadReleaseAsset(
	ctx context.Context, releaseID int64, input ReleaseAssetInput,
) (ReleaseAsset, MutationMeta, error) {
	if releaseID < 1 || !safeReleaseAssetName(input.Name) || len(input.Bytes) == 0 ||
		len(input.Bytes) > 64<<20 || !releaseAssetSHA256Pattern.MatchString(input.SHA256) {
		return ReleaseAsset{}, MutationMeta{}, fmt.Errorf("GitHub release asset input is invalid")
	}
	if _, allowed := mutator.operations[MutationRepositoryRelease]; !allowed {
		return ReleaseAsset{}, MutationMeta{}, fmt.Errorf("GitHub release upload is outside bound repository scope")
	}
	target, err := mutator.factory.client.releaseUploadEndpoint(
		mutator.scope.Owner, mutator.scope.Name, releaseID, input.Name,
	)
	if err != nil {
		return ReleaseAsset{}, MutationMeta{}, err
	}
	if err := mutator.factory.waitForMutationSlot(ctx); err != nil {
		return ReleaseAsset{}, MutationMeta{}, err
	}
	response, err := mutator.factory.client.requestWithContentType(
		ctx, http.MethodPost, target, input.Bytes, "", "application/octet-stream",
	)
	meta := MutationMeta{RepositoryID: mutator.scope.RepositoryID, StatusCode: response.StatusCode,
		RequestID: response.Meta.RequestID, Rate: response.Meta.Rate}
	if err != nil {
		return ReleaseAsset{}, meta, err
	}
	var raw releaseAssetResponse
	if err := decodeJSON(response.Body, &raw); err != nil {
		return ReleaseAsset{}, meta, invalidMutationResponse(response, err)
	}
	asset, err := normalizeReleaseAsset(raw, mutator.scope.Owner, mutator.scope.Name, input)
	if err != nil {
		return ReleaseAsset{}, meta, invalidMutationResponse(response, err)
	}
	return asset, meta, nil
}

func (mutator *RepositoryMutator) UpdateRelease(
	ctx context.Context, releaseID int64, input ReleaseInput,
) (Release, MutationMeta, error) {
	if releaseID < 1 || input.Draft {
		return Release{}, MutationMeta{}, fmt.Errorf("GitHub release publication input is invalid")
	}
	if err := validateReleaseInput(input); err != nil {
		return Release{}, MutationMeta{}, fmt.Errorf("GitHub release publication input is invalid")
	}
	target, err := mutator.endpoint(fmt.Sprintf("releases/%d", releaseID))
	if err != nil {
		return Release{}, MutationMeta{}, err
	}
	response, meta, err := mutator.mutate(ctx, MutationRepositoryRelease, http.MethodPatch, target, input)
	if err != nil {
		return Release{}, meta, err
	}
	var raw releaseResponse
	decodeErr := decodeJSON(response.Body, &raw)
	release, normalizeErr := normalizeRelease(raw, mutator.scope.Owner, mutator.scope.Name)
	if decodeErr != nil || normalizeErr != nil || release.ID != releaseID || release.Draft ||
		release.TagName != input.TagName || release.TargetCommitish != input.TargetCommitish {
		return Release{}, meta, invalidMutationResponse(response, errors.Join(decodeErr, normalizeErr))
	}
	return release, meta, nil
}

func (mutator *RepositoryMutator) DeleteRelease(ctx context.Context, releaseID int64) (MutationMeta, error) {
	if releaseID < 1 {
		return MutationMeta{}, fmt.Errorf("GitHub release id is invalid")
	}
	target, err := mutator.endpoint(fmt.Sprintf("releases/%d", releaseID))
	if err != nil {
		return MutationMeta{}, err
	}
	_, meta, err := mutator.mutate(ctx, MutationRepositoryRelease, http.MethodDelete, target, nil)
	return meta, err
}

func validateReleaseInput(input ReleaseInput) error {
	if !releaseTagNamePattern.MatchString(input.TagName) {
		return fmt.Errorf("GitHub release tag name is not canonical SemVer")
	}
	if !validGitOID(input.TargetCommitish) {
		return fmt.Errorf("GitHub release target commitish is not an exact commit OID")
	}
	if !boundedProviderText(input.Name, 256) {
		return fmt.Errorf("GitHub release name is empty, oversized, or contains control characters")
	}
	if len(input.Body) > 64<<10 || strings.ContainsRune(input.Body, '\x00') {
		return fmt.Errorf("GitHub release body is oversized or contains a null byte")
	}
	if input.MakeLatest != "" && input.MakeLatest != "true" &&
		input.MakeLatest != "false" && input.MakeLatest != "legacy" {
		return fmt.Errorf("GitHub release make_latest value is invalid")
	}
	return nil
}

func normalizeRelease(raw releaseResponse, owner string, name string) (Release, error) {
	releaseURL, urlErr := url.Parse(raw.HTMLURL)
	if urlErr != nil || releaseURL == nil {
		return Release{}, responseContractError{code: "release-url-invalid"}
	}
	releasePathPrefix := "/" + owner + "/" + name + "/releases/tag/"
	expectedPath := releasePathPrefix + raw.TagName
	validPath := releaseURL.Path == expectedPath
	if raw.Draft {
		draftID := strings.TrimPrefix(releaseURL.Path, releasePathPrefix+"untagged-")
		validPath = len(draftID) == 20
		for _, character := range draftID {
			validPath = validPath && ((character >= '0' && character <= '9') ||
				(character >= 'a' && character <= 'f'))
		}
	}
	if raw.ID < 1 || raw.NodeID == "" ||
		!releaseTagNamePattern.MatchString(raw.TagName) || !validGitOID(raw.TargetCommitish) ||
		!boundedProviderText(raw.Name, 256) || len(raw.Body) > 64<<10 ||
		strings.ContainsRune(raw.Body, '\x00') ||
		releaseURL.Scheme != "https" || !strings.EqualFold(releaseURL.Host, "github.com") ||
		releaseURL.User != nil || releaseURL.RawQuery != "" || releaseURL.Fragment != "" ||
		!validPath {
		return Release{}, responseContractError{code: "release-url-invalid"}
	}
	return Release{
		ID: raw.ID, NodeID: raw.NodeID, TagName: raw.TagName,
		TargetCommitish: raw.TargetCommitish, Name: raw.Name, Body: raw.Body,
		HTMLURL: raw.HTMLURL, Draft: raw.Draft, Prerelease: raw.Prerelease,
		Immutable: raw.Immutable, UploadURL: raw.UploadURL,
	}, nil
}

func safeReleaseAssetName(name string) bool {
	return name != "" && len(name) <= 255 && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\x00\r\n")
}

func normalizeReleaseAsset(raw releaseAssetResponse, owner, repository string, input ReleaseAssetInput) (ReleaseAsset, error) {
	asset, err := normalizeObservedReleaseAsset(raw, owner, repository)
	if err != nil {
		return ReleaseAsset{}, err
	}
	if asset.Name != input.Name {
		return ReleaseAsset{}, responseContractError{code: "release-asset-upload-name-mismatch"}
	}
	if asset.Size != int64(len(input.Bytes)) {
		return ReleaseAsset{}, responseContractError{code: "release-asset-upload-size-mismatch"}
	}
	if asset.SHA256 != input.SHA256 {
		return ReleaseAsset{}, responseContractError{code: "release-asset-upload-digest-mismatch"}
	}
	return asset, nil
}

func normalizeObservedReleaseAsset(raw releaseAssetResponse, owner, repository string) (ReleaseAsset, error) {
	parsed, err := url.Parse(raw.BrowserDownloadURL)
	expectedPath := "/" + owner + "/" + repository + "/releases/download/"
	if raw.ID < 1 {
		return ReleaseAsset{}, responseContractError{code: "release-asset-id-invalid"}
	}
	if !safeReleaseAssetName(raw.Name) {
		return ReleaseAsset{}, responseContractError{code: "release-asset-name-invalid"}
	}
	if raw.Size < 1 || raw.Size > 64<<20 {
		return ReleaseAsset{}, responseContractError{code: "release-asset-size-invalid"}
	}
	if raw.State != "uploaded" {
		return ReleaseAsset{}, responseContractError{code: "release-asset-state-invalid"}
	}
	if !releaseAssetSHA256Pattern.MatchString(raw.Digest) {
		return ReleaseAsset{}, responseContractError{code: "release-asset-digest-invalid"}
	}
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ReleaseAsset{}, responseContractError{code: "release-asset-url-invalid"}
	}
	if !strings.HasPrefix(parsed.Path, expectedPath) || !strings.HasSuffix(parsed.Path, "/"+url.PathEscape(raw.Name)) {
		return ReleaseAsset{}, responseContractError{code: "release-asset-url-scope-mismatch"}
	}
	return ReleaseAsset{ID: raw.ID, Name: raw.Name, Size: raw.Size, State: raw.State,
		BrowserDownloadURL: raw.BrowserDownloadURL, SHA256: raw.Digest}, nil
}
