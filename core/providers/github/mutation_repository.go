package github

import (
	"context"
	"fmt"
	"net/http"
	"sort"
)

type RepositorySettingsUpdate struct {
	Name                     *string `json:"name,omitempty"`
	Archived                 *bool   `json:"archived,omitempty"`
	AllowMergeCommit         *bool   `json:"allow_merge_commit,omitempty"`
	AllowSquashMerge         *bool   `json:"allow_squash_merge,omitempty"`
	AllowRebaseMerge         *bool   `json:"allow_rebase_merge,omitempty"`
	AllowAutoMerge           *bool   `json:"allow_auto_merge,omitempty"`
	AllowUpdateBranch        *bool   `json:"allow_update_branch,omitempty"`
	DeleteBranchOnMerge      *bool   `json:"delete_branch_on_merge,omitempty"`
	MergeCommitTitle         *string `json:"merge_commit_title,omitempty"`
	MergeCommitMessage       *string `json:"merge_commit_message,omitempty"`
	SquashMergeCommitTitle   *string `json:"squash_merge_commit_title,omitempty"`
	SquashMergeCommitMessage *string `json:"squash_merge_commit_message,omitempty"`
}

type ActionsPermissionsUpdate struct {
	Enabled            bool    `json:"enabled"`
	AllowedActions     *string `json:"allowed_actions,omitempty"`
	SHAPinningRequired *bool   `json:"sha_pinning_required,omitempty"`
}

type WorkflowPermissionsUpdate struct {
	Default                     string `json:"default_workflow_permissions"`
	CanApprovePullRequestReview bool   `json:"can_approve_pull_request_reviews"`
}

func (mutator *RepositoryMutator) UpdateRepositorySettings(
	ctx context.Context,
	update RepositorySettingsUpdate,
) (Repository, MutationMeta, error) {
	if update.Name != nil || update.Archived != nil {
		return Repository{}, MutationMeta{}, fmt.Errorf(
			"repository lifecycle fields are outside the settings mutation",
		)
	}
	return mutator.updateRepository(ctx, MutationRepositorySettings, update)
}

func (mutator *RepositoryMutator) RenameRepository(
	ctx context.Context,
	name string,
) (Repository, MutationMeta, error) {
	return mutator.updateRepository(
		ctx, MutationRepositoryLifecycle, RepositorySettingsUpdate{Name: &name},
	)
}

func (mutator *RepositoryMutator) ArchiveRepository(
	ctx context.Context,
) (Repository, MutationMeta, error) {
	archived := true
	return mutator.updateRepository(
		ctx, MutationRepositoryLifecycle, RepositorySettingsUpdate{Archived: &archived},
	)
}

func (mutator *RepositoryMutator) updateRepository(
	ctx context.Context,
	operation string,
	update RepositorySettingsUpdate,
) (Repository, MutationMeta, error) {
	if err := validateRepositorySettingsUpdate(update); err != nil {
		return Repository{}, MutationMeta{}, err
	}
	target, err := mutator.endpoint("")
	if err != nil {
		return Repository{}, MutationMeta{}, err
	}
	response, meta, err := mutator.mutate(
		ctx, operation, http.MethodPatch, target, update,
	)
	if err != nil {
		return Repository{}, meta, err
	}
	var raw repositoryResponse
	if err := decodeJSON(response.Body, &raw); err != nil {
		return Repository{}, meta, invalidMutationResponse(response, err)
	}
	repository, err := normalizeRepository(raw)
	if err != nil || repository.ID != mutator.scope.RepositoryID {
		return Repository{}, meta, invalidMutationResponse(response, err)
	}
	return repository, meta, nil
}

func (mutator *RepositoryMutator) DeleteRepository(
	ctx context.Context,
) (MutationMeta, error) {
	target, err := mutator.endpoint("")
	if err != nil {
		return MutationMeta{}, err
	}
	response, meta, err := mutator.mutate(
		ctx, MutationRepositoryDelete, http.MethodDelete, target, nil,
	)
	if err != nil {
		return meta, err
	}
	if response.StatusCode != http.StatusNoContent || len(response.Body) != 0 {
		return meta, invalidMutationResponse(response, nil)
	}
	return meta, nil
}

func (mutator *RepositoryMutator) SetActionsPermissions(
	ctx context.Context,
	update ActionsPermissionsUpdate,
) (MutationMeta, error) {
	if update.AllowedActions != nil && *update.AllowedActions != "all" &&
		*update.AllowedActions != "local_only" && *update.AllowedActions != "selected" {
		return MutationMeta{}, fmt.Errorf("GitHub Actions allowed-actions value is invalid")
	}
	return mutator.putNoContent(
		ctx, MutationRepositorySettings, "actions/permissions", update,
	)
}

func (mutator *RepositoryMutator) SetSelectedActionsPermissions(
	ctx context.Context,
	update SelectedActionsPermissions,
) (MutationMeta, error) {
	if err := validateSelectedActionsPermissions(update); err != nil {
		return MutationMeta{}, err
	}
	update.PatternsAllowed = append([]string(nil), update.PatternsAllowed...)
	sort.Strings(update.PatternsAllowed)
	return mutator.putNoContent(
		ctx, MutationRepositorySettings, "actions/permissions/selected-actions", update,
	)
}

func (mutator *RepositoryMutator) SetWorkflowPermissions(
	ctx context.Context,
	update WorkflowPermissionsUpdate,
) (MutationMeta, error) {
	if update.Default != "read" && update.Default != "write" {
		return MutationMeta{}, fmt.Errorf("GitHub workflow default permission is invalid")
	}
	return mutator.putNoContent(
		ctx, MutationRepositorySettings, "actions/permissions/workflow", update,
	)
}

func (mutator *RepositoryMutator) SetImmutableReleases(
	ctx context.Context,
	enabled bool,
) (MutationMeta, error) {
	method := http.MethodDelete
	if enabled {
		method = http.MethodPut
	}
	target, err := mutator.endpoint("immutable-releases")
	if err != nil {
		return MutationMeta{}, err
	}
	response, meta, err := mutator.mutate(
		ctx, MutationRepositorySettings, method, target, nil,
	)
	if err != nil {
		return meta, err
	}
	if response.StatusCode != http.StatusNoContent || len(response.Body) != 0 {
		return meta, invalidMutationResponse(response, nil)
	}
	return meta, nil
}

func (mutator *RepositoryMutator) putNoContent(
	ctx context.Context,
	operation string,
	suffix string,
	payload any,
) (MutationMeta, error) {
	target, err := mutator.endpoint(suffix)
	if err != nil {
		return MutationMeta{}, err
	}
	response, meta, err := mutator.mutate(ctx, operation, http.MethodPut, target, payload)
	if err != nil {
		return meta, err
	}
	if response.StatusCode != http.StatusNoContent || len(response.Body) != 0 {
		return meta, invalidMutationResponse(response, nil)
	}
	return meta, nil
}

func validateRepositorySettingsUpdate(update RepositorySettingsUpdate) error {
	values := []any{
		update.Name, update.Archived, update.AllowMergeCommit, update.AllowSquashMerge,
		update.AllowRebaseMerge, update.AllowAutoMerge, update.AllowUpdateBranch,
		update.DeleteBranchOnMerge, update.MergeCommitTitle, update.MergeCommitMessage,
		update.SquashMergeCommitTitle, update.SquashMergeCommitMessage,
	}
	set := 0
	for _, value := range values {
		if value != nil {
			set++
		}
	}
	if set == 0 {
		return fmt.Errorf("GitHub repository settings mutation is empty")
	}
	if update.Name != nil && !githubNamePattern.MatchString(*update.Name) {
		return fmt.Errorf("GitHub repository rename target is invalid")
	}
	if update.MergeCommitTitle != nil &&
		!validMergeSetting(*update.MergeCommitTitle, "PR_TITLE", "MERGE_MESSAGE") {
		return fmt.Errorf("GitHub merge commit title setting is invalid")
	}
	if update.MergeCommitMessage != nil &&
		!validMergeSetting(*update.MergeCommitMessage, "PR_TITLE", "PR_BODY", "BLANK") {
		return fmt.Errorf("GitHub merge commit message setting is invalid")
	}
	if update.SquashMergeCommitTitle != nil &&
		!validMergeSetting(*update.SquashMergeCommitTitle, "PR_TITLE", "COMMIT_OR_PR_TITLE") {
		return fmt.Errorf("GitHub squash title setting is invalid")
	}
	if update.SquashMergeCommitMessage != nil &&
		!validMergeSetting(*update.SquashMergeCommitMessage, "PR_BODY", "COMMIT_MESSAGES", "BLANK") {
		return fmt.Errorf("GitHub squash message setting is invalid")
	}
	return nil
}

func validateSelectedActionsPermissions(update SelectedActionsPermissions) error {
	if len(update.PatternsAllowed) > maxSelectedActionPatterns {
		return fmt.Errorf("GitHub selected action pattern count exceeds the safe bound")
	}
	seen := map[string]struct{}{}
	for _, pattern := range update.PatternsAllowed {
		if !boundedProviderText(pattern, 1024) {
			return fmt.Errorf("GitHub selected action pattern is invalid")
		}
		if _, duplicate := seen[pattern]; duplicate {
			return fmt.Errorf("GitHub selected action patterns contain a duplicate")
		}
		seen[pattern] = struct{}{}
	}
	return nil
}

func invalidMutationResponse(response getResult, cause error) error {
	return &APIError{
		Kind: ErrorResponse, StatusCode: response.StatusCode,
		RequestID: response.Meta.RequestID, Cause: cause,
	}
}
