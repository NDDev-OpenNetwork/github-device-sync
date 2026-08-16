package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type DraftPullRequest struct {
	Number  int    `json:"number"`
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	HeadRef string `json:"head_ref"`
	HeadSHA string `json:"head_sha"`
	BaseRef string `json:"base_ref"`
	Draft   bool   `json:"draft"`
	State   string `json:"state"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

type pullRequestMutationResponse struct {
	Number  int    `json:"number"`
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	State   string `json:"state"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (mutator *RepositoryMutator) CreateDraftPullRequest(
	ctx context.Context,
	title string,
	body string,
	head string,
	base string,
) (DraftPullRequest, MutationMeta, error) {
	if !boundedProviderText(title, 256) || len(body) > 64<<10 ||
		!safeBranchName(head) || !safeBranchName(base) || head == base {
		return DraftPullRequest{}, MutationMeta{}, fmt.Errorf("GitHub draft pull request input is invalid")
	}
	target, err := mutator.endpoint("pulls")
	if err != nil {
		return DraftPullRequest{}, MutationMeta{}, err
	}
	response, meta, err := mutator.mutate(
		ctx, MutationPullRequest, http.MethodPost, target,
		map[string]any{
			"title": title, "body": body, "head": head, "base": base,
			"draft": true, "maintainer_can_modify": true,
		},
	)
	if err != nil {
		return DraftPullRequest{}, meta, err
	}
	var raw pullRequestMutationResponse
	decodeErr := decodeJSON(response.Body, &raw)
	result, normalizeErr := normalizeDraftPullRequest(raw, mutator.scope.Owner, mutator.scope.Name, head, base)
	if decodeErr != nil || normalizeErr != nil || raw.Title != title || raw.Body != body {
		return DraftPullRequest{}, meta, invalidMutationResponse(response, errors.Join(decodeErr, normalizeErr))
	}
	return result, meta, nil
}

func normalizeDraftPullRequest(
	raw pullRequestMutationResponse,
	owner string,
	name string,
	head string,
	base string,
) (DraftPullRequest, error) {
	pullURL, urlErr := url.Parse(raw.HTMLURL)
	expectedPath := "/" + owner + "/" + name +
		"/pull/" + strconv.Itoa(raw.Number)
	if urlErr != nil || raw.Number < 1 || raw.ID < 1 ||
		!raw.Draft || raw.State != "open" || raw.Head.Ref != head || raw.Base.Ref != base ||
		!validGitOID(raw.Head.SHA) || pullURL.Scheme != "https" ||
		!strings.EqualFold(pullURL.Host, "github.com") || pullURL.Path != expectedPath ||
		pullURL.User != nil || pullURL.RawQuery != "" || pullURL.Fragment != "" ||
		!boundedProviderText(raw.Title, 256) || len(raw.Body) > 64<<10 {
		return DraftPullRequest{}, fmt.Errorf("GitHub draft pull request response is invalid")
	}
	return DraftPullRequest{
		Number: raw.Number, ID: raw.ID, HTMLURL: raw.HTMLURL,
		HeadRef: raw.Head.Ref, HeadSHA: raw.Head.SHA, BaseRef: raw.Base.Ref,
		Draft: raw.Draft, State: raw.State, Title: raw.Title, Body: raw.Body,
	}, nil
}
