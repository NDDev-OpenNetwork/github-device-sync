package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Deciding that a repository is finished, or that one task branch is, needs the
// whole of what the provider holds about it: every branch, every pull request in
// every state, every open issue, and whether any review conversation is still
// unresolved. A partial answer is the dangerous one -- an unread page is
// indistinguishable from an empty one, and both read as "nothing left".
//
// So every collection here is enumerated to exhaustion or refused. `nextPage`
// is already strict about where a pagination link may point and how far it may
// run; exceeding that bound is an error rather than a truncated list.
//
// Review-thread resolution is not in the REST surface at all. It exists only in
// GraphQL, so that one question is asked there, over the same credential.

const (
	maxObservedBranches     = 2000
	maxObservedPullRequests = 2000
	maxObservedIssues       = 2000
	maxObservedReviewPages  = 20
)

// ObservedBranch is one branch as the provider holds it.
type ObservedBranch struct {
	Name      string `json:"name"`
	SHA       string `json:"sha"`
	Protected bool   `json:"protected"`
}

// ObservedPullRequest is one pull request in any state.
//
// `merged_at` is carried separately from `state` because GitHub reports a merged
// pull request as `closed`, and "closed" covers both "landed" and "abandoned
// with its commits still only on a branch". Collapsing them would let unmerged
// work read as finished.
type ObservedPullRequest struct {
	Number   int    `json:"number"`
	State    string `json:"state"`
	Draft    bool   `json:"draft"`
	Merged   bool   `json:"merged"`
	HeadRef  string `json:"head_ref"`
	HeadSHA  string `json:"head_sha"`
	BaseRef  string `json:"base_ref"`
	Title    string `json:"title"`
	HTMLURL  string `json:"html_url"`
	MergedAt string `json:"merged_at,omitempty"`
}

// ObservedIssue is one issue. Pull requests are excluded: the issues endpoint
// returns them too, and counting a pull request twice would report work as
// blocking in two places.
type ObservedIssue struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
}

// ListBranches enumerates every branch.
func (client *Client) ListBranches(
	ctx context.Context,
	owner string,
	name string,
) ([]ObservedBranch, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) {
		return nil, ResponseMeta{}, fmt.Errorf("invalid GitHub repository identity")
	}
	var raw []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
		Protected bool `json:"protected"`
	}
	meta, err := client.collect(
		ctx, "repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/branches",
		nil, maxObservedBranches, &raw,
	)
	if err != nil {
		return nil, meta, err
	}
	branches := make([]ObservedBranch, 0, len(raw))
	for _, item := range raw {
		if !safeBranchName(item.Name) || !safeObjectName(item.Commit.SHA) {
			return nil, meta, fmt.Errorf("GitHub branch response is not exact")
		}
		branches = append(branches, ObservedBranch{
			Name: item.Name, SHA: item.Commit.SHA, Protected: item.Protected,
		})
	}
	return branches, meta, nil
}

// ListPullRequests enumerates every pull request in every state.
func (client *Client) ListPullRequests(
	ctx context.Context,
	owner string,
	name string,
) ([]ObservedPullRequest, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) {
		return nil, ResponseMeta{}, fmt.Errorf("invalid GitHub repository identity")
	}
	var raw []struct {
		Number   int    `json:"number"`
		State    string `json:"state"`
		Draft    bool   `json:"draft"`
		Title    string `json:"title"`
		HTMLURL  string `json:"html_url"`
		MergedAt string `json:"merged_at"`
		Head     struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	meta, err := client.collect(
		ctx, "repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/pulls",
		url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"asc"}},
		maxObservedPullRequests, &raw,
	)
	if err != nil {
		return nil, meta, err
	}
	requests := make([]ObservedPullRequest, 0, len(raw))
	for _, item := range raw {
		if item.Number <= 0 || (item.State != "open" && item.State != "closed") ||
			!safeObjectName(item.Head.SHA) {
			return nil, meta, fmt.Errorf("GitHub pull-request response is not exact")
		}
		requests = append(requests, ObservedPullRequest{
			Number: item.Number, State: item.State, Draft: item.Draft,
			Merged: item.MergedAt != "", HeadRef: item.Head.Ref, HeadSHA: item.Head.SHA,
			BaseRef: item.Base.Ref, Title: item.Title, HTMLURL: item.HTMLURL,
			MergedAt: item.MergedAt,
		})
	}
	return requests, meta, nil
}

// ListOpenIssues enumerates every open issue that is not a pull request.
func (client *Client) ListOpenIssues(
	ctx context.Context,
	owner string,
	name string,
) ([]ObservedIssue, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) {
		return nil, ResponseMeta{}, fmt.Errorf("invalid GitHub repository identity")
	}
	var raw []struct {
		Number      int             `json:"number"`
		State       string          `json:"state"`
		Title       string          `json:"title"`
		HTMLURL     string          `json:"html_url"`
		PullRequest json.RawMessage `json:"pull_request"`
	}
	meta, err := client.collect(
		ctx, "repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/issues",
		url.Values{"state": {"open"}, "sort": {"created"}, "direction": {"asc"}},
		maxObservedIssues, &raw,
	)
	if err != nil {
		return nil, meta, err
	}
	issues := make([]ObservedIssue, 0, len(raw))
	for _, item := range raw {
		if len(item.PullRequest) != 0 && string(item.PullRequest) != "null" {
			continue
		}
		if item.Number <= 0 || item.State != "open" {
			return nil, meta, fmt.Errorf("GitHub issue response is not exact")
		}
		issues = append(issues, ObservedIssue{
			Number: item.Number, State: item.State, Title: item.Title, HTMLURL: item.HTMLURL,
		})
	}
	return issues, meta, nil
}

// collect enumerates one paginated collection to exhaustion.
//
// The bound is a refusal, not a truncation. A caller deciding whether anything
// unfinished remains cannot tell a short list from a complete one, so a
// collection larger than the bound is an error and the caller reports the answer
// as unknown rather than as empty.
func (client *Client) collect(
	ctx context.Context,
	relative string,
	query url.Values,
	limit int,
	into any,
) (ResponseMeta, error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("per_page", "100")
	query.Set("page", "1")
	target, err := client.endpoint(relative, query)
	if err != nil {
		return ResponseMeta{}, err
	}
	aggregate := []json.RawMessage{}
	var meta ResponseMeta
	for target != nil {
		response, err := client.get(ctx, target, "")
		if err != nil {
			return response.Meta, err
		}
		meta = response.Meta
		var page []json.RawMessage
		if err := decodeJSON(response.Body, &page); err != nil {
			return meta, invalidGovernanceResponse(response, err)
		}
		aggregate = append(aggregate, page...)
		if len(aggregate) > limit {
			return meta, fmt.Errorf(
				"GitHub collection %q exceeds the %d-item bound", relative, limit,
			)
		}
		next, err := client.nextPage(response.Header, target)
		if err != nil {
			return meta, err
		}
		target = next
	}
	encoded, err := json.Marshal(aggregate)
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(encoded, into); err != nil {
		return meta, fmt.Errorf("GitHub collection %q could not be bound: %w", relative, err)
	}
	return meta, nil
}

// CountUnresolvedReviewThreads reports how many review conversations are still
// open across every pull request.
//
// Thread resolution is not in the REST surface. It exists only in GraphQL, and
// an unresolved conversation on a merged pull request is exactly the kind of
// unfinished work a retirement decision must not step over, so the question is
// asked where it can be answered rather than assumed away.
func (client *Client) CountUnresolvedReviewThreads(
	ctx context.Context,
	owner string,
	name string,
) (int, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) {
		return 0, ResponseMeta{}, fmt.Errorf("invalid GitHub repository identity")
	}
	target, err := client.endpoint("graphql", nil)
	if err != nil {
		return 0, ResponseMeta{}, err
	}
	const document = `query($owner:String!,$name:String!,$after:String){
  repository(owner:$owner,name:$name){
    pullRequests(first:50,after:$after,states:[OPEN,CLOSED,MERGED]){
      pageInfo{hasNextPage endCursor}
      nodes{number reviewThreads(first:100){
        pageInfo{hasNextPage}
        nodes{isResolved isOutdated}
      }}
    }
  }
}`
	unresolved := 0
	cursor := ""
	var meta ResponseMeta
	for page := 0; page < maxObservedReviewPages; page++ {
		variables := map[string]any{"owner": owner, "name": name}
		if cursor != "" {
			variables["after"] = cursor
		}
		body, err := json.Marshal(map[string]any{"query": document, "variables": variables})
		if err != nil {
			return 0, meta, err
		}
		response, err := client.request(ctx, http.MethodPost, target, body, "")
		if err != nil {
			return 0, response.Meta, err
		}
		meta = response.Meta
		var decoded struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
			Data struct {
				Repository struct {
					PullRequests struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []struct {
							Number        int `json:"number"`
							ReviewThreads struct {
								PageInfo struct {
									HasNextPage bool `json:"hasNextPage"`
								} `json:"pageInfo"`
								Nodes []struct {
									IsResolved bool `json:"isResolved"`
									IsOutdated bool `json:"isOutdated"`
								} `json:"nodes"`
							} `json:"reviewThreads"`
						} `json:"nodes"`
					} `json:"pullRequests"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := decodeJSON(response.Body, &decoded); err != nil {
			return 0, meta, invalidGovernanceResponse(response, err)
		}
		if len(decoded.Errors) != 0 {
			return 0, meta, fmt.Errorf(
				"GitHub GraphQL review-thread query failed: %s", decoded.Errors[0].Message,
			)
		}
		for _, request := range decoded.Data.Repository.PullRequests.Nodes {
			if request.ReviewThreads.PageInfo.HasNextPage {
				// More conversations than one page holds. Counting the visible
				// ones would report a smaller number than exists, which is the
				// direction that lets unfinished work through.
				return 0, meta, fmt.Errorf(
					"pull request #%d has more review threads than one page holds", request.Number,
				)
			}
			for _, thread := range request.ReviewThreads.Nodes {
				// An outdated thread is one whose lines were rewritten; it is
				// still an open conversation nobody closed.
				if !thread.IsResolved {
					unresolved++
				}
			}
		}
		if !decoded.Data.Repository.PullRequests.PageInfo.HasNextPage {
			return unresolved, meta, nil
		}
		cursor = decoded.Data.Repository.PullRequests.PageInfo.EndCursor
		if cursor == "" {
			return 0, meta, fmt.Errorf("GitHub GraphQL pagination cursor is missing")
		}
	}
	return 0, meta, fmt.Errorf(
		"GitHub pull-request count exceeds the %d-page review-thread bound", maxObservedReviewPages,
	)
}

func safeObjectName(value string) bool {
	if len(value) != 40 {
		return false
	}
	return strings.Trim(value, "0123456789abcdef") == ""
}
