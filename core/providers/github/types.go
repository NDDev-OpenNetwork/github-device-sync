// Package github provides a bounded read-only GitHub App installation client.
package github

import (
	"context"
	"time"
)

const (
	APIVersion       = "2026-03-10"
	DefaultBaseURL   = "https://api.github.com/"
	DefaultUserAgent = "github-device-sync/dev"
	DefaultBodyLimit = int64(8 << 20)
)

type InstallationToken struct {
	Value               string
	ExpiresAt           time.Time
	Permissions         map[string]string
	RepositorySelection string
}

type PermissionContract struct {
	Permissions         map[string]string `json:"permissions"`
	RepositorySelection string            `json:"repository_selection"`
	Mode                string            `json:"permission_mode,omitempty"`
}

type PermissionEvidence struct {
	Expected            map[string]string `json:"expected"`
	Effective           map[string]string `json:"effective"`
	RepositorySelection string            `json:"repository_selection"`
	Status              string            `json:"status"`
}

type TokenSource interface {
	Token(context.Context, string) (InstallationToken, error)
}

type Repository struct {
	ID            int64              `json:"id"`
	NodeID        string             `json:"node_id"`
	Owner         string             `json:"owner"`
	Name          string             `json:"name"`
	FullName      string             `json:"full_name"`
	Private       bool               `json:"private"`
	Visibility    string             `json:"visibility"`
	Fork          bool               `json:"fork"`
	Archived      bool               `json:"archived"`
	Disabled      bool               `json:"disabled"`
	DefaultBranch string             `json:"default_branch"`
	HTMLURL       string             `json:"html_url"`
	Parent        *RepositoryLocator `json:"parent,omitempty"`
	Merge         MergeSettings      `json:"merge"`
	Security      SecuritySettings   `json:"security"`
}

type MergeSettings struct {
	AllowMergeCommit    bool   `json:"allow_merge_commit"`
	AllowSquashMerge    bool   `json:"allow_squash_merge"`
	AllowRebaseMerge    bool   `json:"allow_rebase_merge"`
	AllowAutoMerge      bool   `json:"allow_auto_merge"`
	AllowUpdateBranch   bool   `json:"allow_update_branch"`
	DeleteBranchOnMerge bool   `json:"delete_branch_on_merge"`
	MergeCommitTitle    string `json:"merge_commit_title,omitempty"`
	MergeCommitMessage  string `json:"merge_commit_message,omitempty"`
	SquashMergeTitle    string `json:"squash_merge_commit_title,omitempty"`
	SquashMergeMessage  string `json:"squash_merge_commit_message,omitempty"`
}

type SecuritySettings struct {
	Available bool              `json:"available"`
	Features  map[string]string `json:"features"`
}

type RepositoryLocator struct {
	ID       int64  `json:"id"`
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

type Inventory struct {
	InstallationID string             `json:"installation_id"`
	Repositories   []Repository       `json:"repositories"`
	TotalCount     int                `json:"total_count"`
	Pages          int                `json:"pages"`
	ObservedAt     time.Time          `json:"observed_at"`
	Rate           Rate               `json:"rate"`
	RequestIDs     []string           `json:"request_ids"`
	Permissions    PermissionEvidence `json:"permissions"`
}

type ActionsPermissions struct {
	Enabled            bool   `json:"enabled"`
	AllowedActions     string `json:"allowed_actions"`
	SHAPinningRequired bool   `json:"sha_pinning_required"`
}

type SelectedActionsPermissions struct {
	GitHubOwnedAllowed bool     `json:"github_owned_allowed"`
	VerifiedAllowed    bool     `json:"verified_allowed"`
	PatternsAllowed    []string `json:"patterns_allowed"`
}

type WorkflowPermissions struct {
	Default                     string `json:"default_workflow_permissions"`
	CanApprovePullRequestReview bool   `json:"can_approve_pull_request_reviews"`
}

type ImmutableReleases struct {
	Enabled         bool `json:"enabled"`
	EnforcedByOwner bool `json:"enforced_by_owner"`
}

type RulesetSummary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	SourceType  string `json:"source_type"`
	Source      string `json:"source"`
	Enforcement string `json:"enforcement"`
}

type GovernanceSnapshot struct {
	InstallationID    string                      `json:"installation_id"`
	Repository        Repository                  `json:"repository"`
	Actions           ActionsPermissions          `json:"actions"`
	SelectedActions   *SelectedActionsPermissions `json:"selected_actions,omitempty"`
	Workflow          WorkflowPermissions         `json:"workflow"`
	ImmutableReleases ImmutableReleases           `json:"immutable_releases"`
	Rulesets          []RulesetSummary            `json:"rulesets"`
	Permissions       PermissionEvidence          `json:"permissions"`
	ObservedAt        time.Time                   `json:"observed_at"`
	Rate              Rate                        `json:"rate"`
	RequestIDs        []string                    `json:"request_ids"`
}

type Rate struct {
	Known     bool      `json:"known"`
	Limit     int       `json:"limit,omitempty"`
	Remaining int       `json:"remaining,omitempty"`
	Used      int       `json:"used,omitempty"`
	ResetAt   time.Time `json:"reset_at,omitempty"`
}

type ResponseMeta struct {
	RequestID  string
	ETag       string
	Rate       Rate
	RetryAfter time.Duration
}
