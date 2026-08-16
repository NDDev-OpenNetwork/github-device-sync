package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Release observation is read-only and therefore lives on the read client rather
// than the repository mutator. Verification and release planning must not need
// mutation credentials merely to observe the provider state.
//
// The consequence is what makes the split worth having: a routine re-check would
// otherwise carry write authority it never uses, and an operator holding only
// read access could not verify a release at all. These methods reuse the
// mutation path's normalizers, so the two agree on what a valid release is
// instead of drifting into two definitions.
func (client *Client) GetReleaseByTag(
	ctx context.Context,
	owner string,
	name string,
	tagName string,
) (Release, error) {
	if !releaseTagNamePattern.MatchString(tagName) {
		return Release{}, fmt.Errorf("GitHub release tag name is invalid")
	}
	if !boundedProviderText(owner, 128) || !boundedProviderText(name, 128) {
		return Release{}, fmt.Errorf("GitHub repository scope is invalid")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+
			"/releases/tags/"+url.PathEscape(tagName), url.Values{},
	)
	if err != nil {
		return Release{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return Release{}, err
	}
	var raw releaseResponse
	if err := decodeJSON(response.Body, &raw); err != nil {
		return Release{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	release, err := normalizeRelease(raw, owner, name)
	if err != nil || release.TagName != tagName {
		return Release{}, fmt.Errorf("GitHub release response is invalid")
	}
	return release, nil
}

func (client *Client) ListReleaseAssets(
	ctx context.Context,
	owner string,
	name string,
	releaseID int64,
) ([]ReleaseAsset, error) {
	if releaseID < 1 {
		return nil, fmt.Errorf("GitHub release id is invalid")
	}
	if !boundedProviderText(owner, 128) || !boundedProviderText(name, 128) {
		return nil, fmt.Errorf("GitHub repository scope is invalid")
	}
	query := url.Values{}
	query.Set("per_page", "100")
	query.Set("page", "1")
	target, err := client.endpoint(
		fmt.Sprintf("repos/%s/%s/releases/%d/assets",
			url.PathEscape(owner), url.PathEscape(name), releaseID), query,
	)
	if err != nil {
		return nil, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return nil, err
	}
	// A release whose asset inventory does not fit one bounded page is not a
	// release this contract can verify exactly, so it fails rather than compares a
	// truncated inventory. Without this stated, the check reads as an arbitrary
	// limit and the obvious repair is to add pagination -- which would silently
	// turn an exact verification into a partial one.
	if strings.Contains(response.Header.Get("Link"), `rel="next"`) {
		return nil, fmt.Errorf("GitHub release asset inventory exceeds the bounded page")
	}
	var raw []releaseAssetResponse
	if err := decodeJSON(response.Body, &raw); err != nil || len(raw) > 100 {
		return nil, fmt.Errorf("GitHub release asset response is invalid")
	}
	assets := make([]ReleaseAsset, 0, len(raw))
	for _, item := range raw {
		asset, normalizeErr := normalizeObservedReleaseAsset(item, owner, name)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

// GetVersionTagRefOptional observes one exact canonical release tag. A 404 is
// reported as absence only after the caller has separately proven access to
// the repository; GitHub intentionally conflates missing and inaccessible
// resources at this endpoint.
func (client *Client) GetVersionTagRefOptional(
	ctx context.Context,
	owner string,
	name string,
	tagName string,
) (RefResult, ResponseMeta, bool, error) {
	if !safePathSegment(owner) || !safePathSegment(name) || !releaseTagNamePattern.MatchString(tagName) {
		return RefResult{}, ResponseMeta{}, false, fmt.Errorf("invalid GitHub release tag identity")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/git/ref/tags/"+url.PathEscape(tagName),
		nil,
	)
	if err != nil {
		return RefResult{}, ResponseMeta{}, false, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Kind == ErrorNotFoundOrInaccessible {
			return RefResult{}, response.Meta, false, nil
		}
		return RefResult{}, response.Meta, false, err
	}
	var raw refMutationResponse
	expectedRef := "refs/tags/" + tagName
	if err := decodeJSON(response.Body, &raw); err != nil || raw.Ref != expectedRef ||
		!validGitOID(raw.Object.SHA) {
		return RefResult{}, response.Meta, false, invalidGovernanceResponse(response, err)
	}
	return RefResult{Ref: raw.Ref, SHA: strings.ToLower(raw.Object.SHA)}, response.Meta, true, nil
}

// GetReleaseByTagOptional observes a GitHub Release without using mutation
// credentials. See GetVersionTagRefOptional for the 404 ambiguity contract.
func (client *Client) GetReleaseByTagOptional(
	ctx context.Context,
	owner string,
	name string,
	tagName string,
) (Release, ResponseMeta, bool, error) {
	if !safePathSegment(owner) || !safePathSegment(name) || !releaseTagNamePattern.MatchString(tagName) {
		return Release{}, ResponseMeta{}, false, fmt.Errorf("invalid GitHub release identity")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/releases/tags/"+url.PathEscape(tagName),
		nil,
	)
	if err != nil {
		return Release{}, ResponseMeta{}, false, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Kind == ErrorNotFoundOrInaccessible {
			return Release{}, response.Meta, false, nil
		}
		return Release{}, response.Meta, false, err
	}
	var raw releaseResponse
	if err := decodeJSON(response.Body, &raw); err != nil {
		return Release{}, response.Meta, false, invalidGovernanceResponse(response, err)
	}
	release, err := normalizeRelease(raw, owner, name)
	if err != nil || release.TagName != tagName {
		return Release{}, response.Meta, false, invalidGovernanceResponse(response, err)
	}
	return release, response.Meta, true, nil
}
