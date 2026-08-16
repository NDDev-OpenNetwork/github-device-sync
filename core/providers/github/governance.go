package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const maxRepositoryRulesets = 100
const maxSelectedActionPatterns = 100

func (client *Client) GetRepositoryGovernance(
	ctx context.Context,
	owner string,
	name string,
) (GovernanceSnapshot, error) {
	if !safePathSegment(owner) || !safePathSegment(name) {
		return GovernanceSnapshot{}, fmt.Errorf("invalid GitHub owner or repository name")
	}
	permissions, err := client.permissionEvidence(ctx)
	if err != nil {
		return GovernanceSnapshot{}, err
	}
	repository, repositoryMeta, _, err := client.GetRepository(ctx, owner, name, "")
	if err != nil {
		return GovernanceSnapshot{}, err
	}
	snapshot := GovernanceSnapshot{
		InstallationID: client.installationID,
		Repository:     repository,
		Permissions:    permissions,
		ObservedAt:     client.now().UTC(),
	}
	appendMeta := func(meta ResponseMeta) {
		snapshot.Rate = meta.Rate
		if meta.RequestID != "" {
			snapshot.RequestIDs = append(snapshot.RequestIDs, meta.RequestID)
		}
	}
	appendMeta(repositoryMeta)
	base := "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/"

	snapshot.Actions, repositoryMeta, err = client.getActionsPermissions(ctx, base)
	if err != nil {
		return GovernanceSnapshot{}, err
	}
	appendMeta(repositoryMeta)
	if snapshot.Actions.AllowedActions == "selected" {
		var selected SelectedActionsPermissions
		selected, repositoryMeta, err = client.getSelectedActionsPermissions(ctx, base)
		if err != nil {
			return GovernanceSnapshot{}, err
		}
		snapshot.SelectedActions = &selected
		appendMeta(repositoryMeta)
	}
	snapshot.Workflow, repositoryMeta, err = client.getWorkflowPermissions(ctx, base)
	if err != nil {
		return GovernanceSnapshot{}, err
	}
	appendMeta(repositoryMeta)
	snapshot.ImmutableReleases, repositoryMeta, err = client.getImmutableReleases(ctx, base)
	if err != nil {
		return GovernanceSnapshot{}, err
	}
	appendMeta(repositoryMeta)
	snapshot.Rulesets, repositoryMeta, err = client.getRulesets(ctx, base)
	if err != nil {
		return GovernanceSnapshot{}, err
	}
	appendMeta(repositoryMeta)
	return snapshot, nil
}

func (client *Client) getImmutableReleases(
	ctx context.Context,
	base string,
) (ImmutableReleases, ResponseMeta, error) {
	target, err := client.endpoint(base+"immutable-releases", nil)
	if err != nil {
		return ImmutableReleases{}, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound {
			// GitHub documents 404 as the repository-level disabled state for
			// this endpoint. Repository identity was already proven above.
			return ImmutableReleases{}, response.Meta, nil
		}
		return ImmutableReleases{}, response.Meta, err
	}
	var value ImmutableReleases
	if err := decodeJSON(response.Body, &value); err != nil {
		return ImmutableReleases{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	if !value.Enabled && value.EnforcedByOwner {
		return ImmutableReleases{}, response.Meta, invalidGovernanceResponse(response, nil)
	}
	return value, response.Meta, nil
}

func (client *Client) getSelectedActionsPermissions(
	ctx context.Context,
	base string,
) (SelectedActionsPermissions, ResponseMeta, error) {
	target, err := client.endpoint(base+"actions/permissions/selected-actions", nil)
	if err != nil {
		return SelectedActionsPermissions{}, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return SelectedActionsPermissions{}, response.Meta, err
	}
	var value SelectedActionsPermissions
	if err := decodeJSON(response.Body, &value); err != nil {
		return SelectedActionsPermissions{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	if len(value.PatternsAllowed) > maxSelectedActionPatterns {
		return SelectedActionsPermissions{}, response.Meta, fmt.Errorf(
			"GitHub selected action pattern count exceeds the %d-item bound",
			maxSelectedActionPatterns,
		)
	}
	seen := make(map[string]struct{}, len(value.PatternsAllowed))
	for _, pattern := range value.PatternsAllowed {
		if !boundedProviderText(pattern, 1024) {
			return SelectedActionsPermissions{}, response.Meta, invalidGovernanceResponse(response, nil)
		}
		if _, duplicate := seen[pattern]; duplicate {
			return SelectedActionsPermissions{}, response.Meta, invalidGovernanceResponse(response, nil)
		}
		seen[pattern] = struct{}{}
	}
	sort.Strings(value.PatternsAllowed)
	return value, response.Meta, nil
}

func (client *Client) getActionsPermissions(
	ctx context.Context,
	base string,
) (ActionsPermissions, ResponseMeta, error) {
	target, err := client.endpoint(base+"actions/permissions", nil)
	if err != nil {
		return ActionsPermissions{}, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return ActionsPermissions{}, response.Meta, err
	}
	var value ActionsPermissions
	decodeErr := decodeJSON(response.Body, &value)
	allowedActionsValid := value.AllowedActions == "all" ||
		value.AllowedActions == "local_only" || value.AllowedActions == "selected"
	if decodeErr != nil || (value.Enabled && !allowedActionsValid) ||
		(value.AllowedActions != "" && !allowedActionsValid) {
		return ActionsPermissions{}, response.Meta, invalidGovernanceResponse(response, decodeErr)
	}
	return value, response.Meta, nil
}

func (client *Client) getWorkflowPermissions(
	ctx context.Context,
	base string,
) (WorkflowPermissions, ResponseMeta, error) {
	target, err := client.endpoint(base+"actions/permissions/workflow", nil)
	if err != nil {
		return WorkflowPermissions{}, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return WorkflowPermissions{}, response.Meta, err
	}
	var value WorkflowPermissions
	decodeErr := decodeJSON(response.Body, &value)
	if decodeErr != nil || (value.Default != "read" && value.Default != "write") {
		return WorkflowPermissions{}, response.Meta, invalidGovernanceResponse(response, decodeErr)
	}
	return value, response.Meta, nil
}

func (client *Client) getRulesets(
	ctx context.Context,
	base string,
) ([]RulesetSummary, ResponseMeta, error) {
	query := url.Values{"per_page": {"100"}, "page": {"1"}}
	target, err := client.endpoint(base+"rulesets", query)
	if err != nil {
		return nil, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return nil, response.Meta, err
	}
	var rulesets []RulesetSummary
	if err := decodeJSON(response.Body, &rulesets); err != nil {
		return nil, response.Meta, invalidGovernanceResponse(response, err)
	}
	if len(rulesets) > maxRepositoryRulesets {
		return nil, response.Meta, fmt.Errorf(
			"GitHub repository ruleset count exceeds the %d-item bound", maxRepositoryRulesets,
		)
	}
	next, err := client.nextPage(response.Header, target)
	if err != nil {
		return nil, response.Meta, err
	}
	if next != nil {
		return nil, response.Meta, fmt.Errorf(
			"GitHub repository ruleset count exceeds the %d-item bound", maxRepositoryRulesets,
		)
	}
	seen := make(map[int64]struct{}, len(rulesets))
	for _, ruleset := range rulesets {
		if ruleset.ID <= 0 || !boundedProviderText(ruleset.Name, 256) ||
			!boundedProviderText(ruleset.Source, 256) ||
			(ruleset.Target != "branch" && ruleset.Target != "tag" && ruleset.Target != "push") ||
			(ruleset.SourceType != "Repository" && ruleset.SourceType != "Organization" &&
				ruleset.SourceType != "Enterprise") ||
			(ruleset.Enforcement != "active" && ruleset.Enforcement != "disabled" &&
				ruleset.Enforcement != "evaluate") {
			return nil, response.Meta, invalidGovernanceResponse(response, nil)
		}
		if _, duplicate := seen[ruleset.ID]; duplicate {
			return nil, response.Meta, invalidGovernanceResponse(response, nil)
		}
		seen[ruleset.ID] = struct{}{}
	}
	sort.Slice(rulesets, func(left, right int) bool { return rulesets[left].ID < rulesets[right].ID })
	return rulesets, response.Meta, nil
}

func boundedProviderText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func invalidGovernanceResponse(response getResult, cause error) error {
	return &APIError{
		Kind: ErrorResponse, StatusCode: response.StatusCode,
		RequestID: response.Meta.RequestID, Cause: cause,
	}
}
