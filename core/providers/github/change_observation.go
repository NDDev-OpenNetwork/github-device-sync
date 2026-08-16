package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type ContentState struct {
	Path    string `json:"path"`
	BlobSHA string `json:"blob_sha"`
	Content []byte `json:"-"`
}

type contentObservationResponse struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Size     int    `json:"size"`
	Content  string `json:"content"`
}

func (client *Client) GetBranchRef(
	ctx context.Context,
	owner string,
	name string,
	branch string,
) (RefResult, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) || !safeBranchName(branch) {
		return RefResult{}, ResponseMeta{}, fmt.Errorf("invalid GitHub branch identity")
	}
	escaped := strings.Join(escapePathSegments(strings.Split("heads/"+branch, "/")), "/")
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/git/ref/"+escaped, nil,
	)
	if err != nil {
		return RefResult{}, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return RefResult{}, response.Meta, err
	}
	var raw refMutationResponse
	expectedRef := "refs/heads/" + branch
	if err := decodeJSON(response.Body, &raw); err != nil || raw.Ref != expectedRef ||
		!validGitOID(raw.Object.SHA) {
		return RefResult{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	return RefResult{Ref: raw.Ref, SHA: raw.Object.SHA}, response.Meta, nil
}

func (client *Client) GetContent(
	ctx context.Context,
	owner string,
	name string,
	path string,
	ref string,
) (ContentState, ResponseMeta, error) {
	escapedPath, err := escapeRepositoryContentPath(path)
	if err != nil || !safePathSegment(owner) || !safePathSegment(name) || !safeBranchName(ref) {
		return ContentState{}, ResponseMeta{}, fmt.Errorf("invalid GitHub content identity")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/contents/"+escapedPath,
		url.Values{"ref": {ref}},
	)
	if err != nil {
		return ContentState{}, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return ContentState{}, response.Meta, err
	}
	var raw contentObservationResponse
	if err := decodeJSON(response.Body, &raw); err != nil || raw.Type != "file" ||
		raw.Encoding != "base64" || raw.Path != path || !validGitOID(raw.SHA) ||
		raw.Size < 0 || raw.Size > maxMutationContentBytes {
		return ContentState{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	content, err := base64.StdEncoding.DecodeString(raw.Content)
	if err != nil || len(content) != raw.Size || len(content) > maxMutationContentBytes {
		return ContentState{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	return ContentState{Path: raw.Path, BlobSHA: raw.SHA, Content: content}, response.Meta, nil
}

func (client *Client) ListOpenPullRequests(
	ctx context.Context,
	owner string,
	name string,
	head string,
	base string,
) ([]DraftPullRequest, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) ||
		!safeBranchName(head) || !safeBranchName(base) || head == base {
		return nil, ResponseMeta{}, fmt.Errorf("invalid GitHub pull-request identity")
	}
	query := url.Values{
		"state": {"open"}, "head": {owner + ":" + head}, "base": {base},
		"per_page": {"2"}, "page": {"1"},
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/pulls", query,
	)
	if err != nil {
		return nil, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return nil, response.Meta, err
	}
	var raw []pullRequestMutationResponse
	if err := decodeJSON(response.Body, &raw); err != nil || len(raw) > 1 {
		return nil, response.Meta, invalidGovernanceResponse(response, err)
	}
	result := make([]DraftPullRequest, 0, len(raw))
	for _, item := range raw {
		value, err := normalizeDraftPullRequest(item, owner, name, head, base)
		if err != nil {
			return nil, response.Meta, invalidGovernanceResponse(response, err)
		}
		result = append(result, value)
	}
	return result, response.Meta, nil
}

func (client *Client) GetCustomPropertyValues(
	ctx context.Context,
	owner string,
	name string,
) ([]CustomPropertyValue, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) {
		return nil, ResponseMeta{}, fmt.Errorf("invalid GitHub repository identity")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/properties/values", nil,
	)
	if err != nil {
		return nil, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return nil, response.Meta, err
	}
	var values []CustomPropertyValue
	if err := decodeJSON(response.Body, &values); err != nil || len(values) > 100 {
		return nil, response.Meta, invalidGovernanceResponse(response, err)
	}
	seen := map[string]struct{}{}
	for index := range values {
		value := &values[index]
		normalized, valid := normalizeObservedCustomPropertyValue(value.Value)
		if !ValidCustomPropertyName(value.Name) || !valid {
			return nil, response.Meta, invalidGovernanceResponse(response, nil)
		}
		value.Value = normalized
		if _, duplicate := seen[value.Name]; duplicate {
			return nil, response.Meta, invalidGovernanceResponse(response, nil)
		}
		seen[value.Name] = struct{}{}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].Name < values[right].Name })
	return values, response.Meta, nil
}

func normalizeObservedCustomPropertyValue(value any) (any, bool) {
	switch typed := value.(type) {
	case nil, string:
		return typed, ValidCustomPropertyValue(typed)
	case []any:
		items := make([]string, len(typed))
		for index, item := range typed {
			stringValue, ok := item.(string)
			if !ok {
				return nil, false
			}
			items[index] = stringValue
		}
		return items, ValidCustomPropertyValue(items)
	default:
		return nil, false
	}
}

func (client *Client) GetRepositoryRulesetSummary(
	ctx context.Context,
	owner string,
	name string,
	rulesetID int64,
) (RulesetSummary, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) || rulesetID <= 0 {
		return RulesetSummary{}, ResponseMeta{}, fmt.Errorf("invalid GitHub repository ruleset identity")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/rulesets/"+
			strconv.FormatInt(rulesetID, 10), nil,
	)
	if err != nil {
		return RulesetSummary{}, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return RulesetSummary{}, response.Meta, err
	}
	var value RulesetSummary
	if err := decodeJSON(response.Body, &value); err != nil || value.ID != rulesetID ||
		!boundedProviderText(value.Name, 256) || value.Target != "branch" ||
		value.SourceType != "Repository" ||
		!strings.EqualFold(value.Source, owner+"/"+name) || value.Enforcement != "active" {
		return RulesetSummary{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	return value, response.Meta, nil
}
