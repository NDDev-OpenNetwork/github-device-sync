package github

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitref"
)

var (
	githubOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	githubNamePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

type repositoryResponse struct {
	ID                       int64  `json:"id"`
	NodeID                   string `json:"node_id"`
	Name                     string `json:"name"`
	FullName                 string `json:"full_name"`
	Private                  bool   `json:"private"`
	Visibility               string `json:"visibility"`
	Fork                     bool   `json:"fork"`
	Archived                 bool   `json:"archived"`
	Disabled                 bool   `json:"disabled"`
	DefaultBranch            string `json:"default_branch"`
	HTMLURL                  string `json:"html_url"`
	AllowMergeCommit         bool   `json:"allow_merge_commit"`
	AllowSquashMerge         bool   `json:"allow_squash_merge"`
	AllowRebaseMerge         bool   `json:"allow_rebase_merge"`
	AllowAutoMerge           bool   `json:"allow_auto_merge"`
	AllowUpdateBranch        bool   `json:"allow_update_branch"`
	DeleteBranchOnMerge      bool   `json:"delete_branch_on_merge"`
	MergeCommitTitle         string `json:"merge_commit_title"`
	MergeCommitMessage       string `json:"merge_commit_message"`
	SquashMergeCommitTitle   string `json:"squash_merge_commit_title"`
	SquashMergeCommitMessage string `json:"squash_merge_commit_message"`
	SecurityAndAnalysis      map[string]struct {
		Status string `json:"status"`
	} `json:"security_and_analysis"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
	Parent *struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"parent"`
}

type repositoryPage struct {
	TotalCount   int                  `json:"total_count"`
	Repositories []repositoryResponse `json:"repositories"`
}

func (client *Client) ListInstallationRepositories(
	ctx context.Context,
	maxRepositories int,
) (Inventory, error) {
	if maxRepositories < 1 || maxRepositories > 2000 {
		return Inventory{}, fmt.Errorf("repository limit must be between 1 and 2000")
	}
	strategy := client.inventoryStrategy()
	query := url.Values{"per_page": {"100"}, "page": {"1"}}
	for key, values := range strategy.baseQuery {
		query[key] = values
	}
	next, err := client.endpoint(strategy.firstPath, query)
	if err != nil {
		return Inventory{}, err
	}
	inventory := Inventory{InstallationID: client.installationID, ObservedAt: client.now().UTC()}
	permissions, err := client.permissionEvidence(ctx)
	if err != nil {
		return Inventory{}, err
	}
	inventory.Permissions = permissions
	seen := map[int64]struct{}{}
	for next != nil {
		if inventory.Pages >= 20 {
			return Inventory{}, fmt.Errorf("GitHub pagination exceeded the 20-page estate bound")
		}
		response, err := client.get(ctx, next, "")
		if err != nil {
			return Inventory{}, err
		}
		page, err := strategy.decode(response.Body)
		if err != nil {
			return Inventory{}, &APIError{
				Kind: ErrorResponse, StatusCode: response.StatusCode,
				RequestID: response.Meta.RequestID, Cause: err,
			}
		}
		if strategy.tracksTotal {
			if inventory.Pages > 0 && inventory.TotalCount != page.TotalCount {
				return Inventory{}, fmt.Errorf(
					"GitHub inventory total changed from %d to %d during pagination",
					inventory.TotalCount, page.TotalCount,
				)
			}
			inventory.TotalCount = page.TotalCount
			if inventory.TotalCount > maxRepositories {
				return Inventory{}, fmt.Errorf(
					"GitHub reports %d repositories, above the configured %d-repository bound",
					inventory.TotalCount, maxRepositories,
				)
			}
		}
		inventory.Pages++
		inventory.Rate = response.Meta.Rate
		if response.Meta.RequestID != "" {
			inventory.RequestIDs = append(inventory.RequestIDs, response.Meta.RequestID)
		}
		for _, raw := range page.Repositories {
			repository, err := normalizeRepository(raw)
			if err != nil {
				return Inventory{}, &APIError{
					Kind: ErrorResponse, StatusCode: response.StatusCode,
					RequestID: response.Meta.RequestID, Cause: err,
				}
			}
			if _, duplicate := seen[repository.ID]; duplicate {
				return Inventory{}, fmt.Errorf("GitHub inventory repeated repository id %d", repository.ID)
			}
			seen[repository.ID] = struct{}{}
			inventory.Repositories = append(inventory.Repositories, repository)
			if len(inventory.Repositories) > maxRepositories {
				return Inventory{}, fmt.Errorf(
					"GitHub inventory exceeded the configured %d-repository bound", maxRepositories,
				)
			}
		}
		next, err = client.nextPage(response.Header, next)
		if err != nil {
			return Inventory{}, err
		}
	}
	if strategy.tracksTotal {
		if inventory.TotalCount != len(inventory.Repositories) {
			return Inventory{}, fmt.Errorf(
				"GitHub pagination returned %d of %d repositories",
				len(inventory.Repositories), inventory.TotalCount,
			)
		}
	} else {
		// The account list endpoints do not report a total_count; the observed
		// length is the authoritative count for those strategies.
		inventory.TotalCount = len(inventory.Repositories)
	}
	sort.Slice(inventory.Repositories, func(left, right int) bool {
		return inventory.Repositories[left].ID < inventory.Repositories[right].ID
	})
	return inventory, nil
}

// inventoryStrategy selects the inventory endpoint and response decoder for the
// client's credential model. The GitHub App installation token lists
// repositories at /installation/repositories with a {total_count, repositories}
// envelope. A personal access token (gh CLI) cannot use that endpoint and is
// instead enumerated through the account list endpoints, which return a bare
// repository array with no total_count.
type inventoryStrategy struct {
	firstPath   string
	tracksTotal bool
	// baseQuery adds fixed query parameters to the first inventory request
	// (e.g. type=owner for the user list endpoint). Pagination links echo them
	// back and are forwarded after validation.
	baseQuery url.Values
	decode    func([]byte) (repositoryPage, error)
}

func (client *Client) inventoryStrategy() inventoryStrategy {
	switch client.inventoryAccount.Type {
	case "organization":
		return inventoryStrategy{
			firstPath:   "orgs/" + url.PathEscape(client.inventoryAccount.Login) + "/repos",
			tracksTotal: false,
			decode:      decodeAccountRepositoryPage,
		}
	case "user":
		return inventoryStrategy{
			firstPath:   "user/repos",
			tracksTotal: false,
			// A PAT can see repositories it collaborates on; an estate user
			// installation observes only the repositories that account owns.
			baseQuery: url.Values{"type": {"owner"}},
			decode:    decodeAccountRepositoryPage,
		}
	default:
		return inventoryStrategy{
			firstPath:   "installation/repositories",
			tracksTotal: true,
			decode:      decodeInstallationRepositoryPage,
		}
	}
}

func decodeInstallationRepositoryPage(body []byte) (repositoryPage, error) {
	var page repositoryPage
	if err := decodeJSON(body, &page); err != nil {
		return repositoryPage{}, err
	}
	return page, nil
}

func decodeAccountRepositoryPage(body []byte) (repositoryPage, error) {
	var raw []repositoryResponse
	if err := decodeJSON(body, &raw); err != nil {
		return repositoryPage{}, err
	}
	return repositoryPage{TotalCount: len(raw), Repositories: raw}, nil
}

func (client *Client) GetRepository(
	ctx context.Context,
	owner string,
	name string,
	ifNoneMatch string,
) (Repository, ResponseMeta, bool, error) {
	if !safePathSegment(owner) || !safePathSegment(name) {
		return Repository{}, ResponseMeta{}, false, fmt.Errorf("invalid GitHub owner or repository name")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil,
	)
	if err != nil {
		return Repository{}, ResponseMeta{}, false, err
	}
	response, err := client.get(ctx, target, ifNoneMatch)
	if err != nil {
		return Repository{}, response.Meta, false, err
	}
	if response.StatusCode == 304 {
		return Repository{}, response.Meta, true, nil
	}
	var raw repositoryResponse
	if err := decodeJSON(response.Body, &raw); err != nil {
		return Repository{}, response.Meta, false, &APIError{
			Kind: ErrorResponse, StatusCode: response.StatusCode,
			RequestID: response.Meta.RequestID, Cause: err,
		}
	}
	repository, err := normalizeRepository(raw)
	return repository, response.Meta, false, err
}

func (client *Client) nextPage(header map[string][]string, current *url.URL) (*url.URL, error) {
	for _, value := range header["Link"] {
		for _, entry := range strings.Split(value, ",") {
			parts := strings.Split(strings.TrimSpace(entry), ";")
			if len(parts) < 2 || strings.TrimSpace(parts[1]) != `rel="next"` {
				continue
			}
			raw := strings.TrimSpace(parts[0])
			if len(raw) < 2 || raw[0] != '<' || raw[len(raw)-1] != '>' {
				return nil, fmt.Errorf("invalid GitHub Link header")
			}
			next, err := url.Parse(raw[1 : len(raw)-1])
			if err != nil || client.validateTarget(next) != nil || next.Path != current.Path {
				return nil, fmt.Errorf("GitHub pagination link escaped the expected endpoint")
			}
			query := next.Query()
			if query.Get("per_page") != "100" || query.Get("page") == "" {
				return nil, fmt.Errorf("GitHub pagination link has unexpected query parameters")
			}
			page, err := strconv.Atoi(query.Get("page"))
			if err != nil || page < 2 || page > 20 {
				return nil, fmt.Errorf("GitHub pagination page is outside the bounded range")
			}
			// The account list endpoints may echo back additional query
			// parameters (type, affiliation, sort) that the first request did
			// not carry. They are safe to forward because the target origin and
			// path are already validated above; only per_page and page are
			// load-bearing for the bounded pagination contract.
			return next, nil
		}
	}
	return nil, nil
}

func normalizeRepository(raw repositoryResponse) (Repository, error) {
	if raw.ID <= 0 || raw.NodeID == "" || !githubOwnerPattern.MatchString(raw.Owner.Login) ||
		!githubNamePattern.MatchString(raw.Name) || raw.FullName == "" ||
		!safeBranchName(raw.DefaultBranch) || raw.HTMLURL == "" {
		return Repository{}, fmt.Errorf("GitHub repository response is missing required identity fields")
	}
	expectedFullName := raw.Owner.Login + "/" + raw.Name
	if !strings.EqualFold(raw.FullName, expectedFullName) {
		return Repository{}, fmt.Errorf("GitHub repository full_name does not match owner and name")
	}
	visibility := raw.Visibility
	if visibility == "" {
		if raw.Private {
			visibility = "private"
		} else {
			visibility = "public"
		}
	}
	if visibility != "public" && visibility != "private" && visibility != "internal" {
		return Repository{}, fmt.Errorf("GitHub repository has unsupported visibility %q", visibility)
	}
	if (visibility == "public") == raw.Private {
		return Repository{}, fmt.Errorf("GitHub repository visibility and private flag conflict")
	}
	if !validMergeSetting(raw.MergeCommitTitle, "PR_TITLE", "MERGE_MESSAGE") ||
		!validMergeSetting(raw.MergeCommitMessage, "PR_TITLE", "PR_BODY", "BLANK") ||
		!validMergeSetting(raw.SquashMergeCommitTitle, "PR_TITLE", "COMMIT_OR_PR_TITLE") ||
		!validMergeSetting(raw.SquashMergeCommitMessage, "PR_BODY", "COMMIT_MESSAGES", "BLANK") {
		return Repository{}, fmt.Errorf("GitHub repository merge settings are invalid")
	}
	security, err := normalizeSecuritySettings(raw.SecurityAndAnalysis)
	if err != nil {
		return Repository{}, err
	}
	htmlURL, err := url.Parse(raw.HTMLURL)
	if err != nil || htmlURL.Scheme != "https" || !strings.EqualFold(htmlURL.Host, "github.com") ||
		htmlURL.User != nil || htmlURL.RawQuery != "" || htmlURL.Fragment != "" ||
		!strings.EqualFold(strings.Trim(htmlURL.Path, "/"), expectedFullName) {
		return Repository{}, fmt.Errorf("GitHub repository html_url does not match repository identity")
	}
	repository := Repository{
		ID: raw.ID, NodeID: raw.NodeID, Owner: raw.Owner.Login, Name: raw.Name,
		FullName: raw.FullName, Private: raw.Private, Visibility: visibility,
		Fork: raw.Fork, Archived: raw.Archived, Disabled: raw.Disabled,
		DefaultBranch: raw.DefaultBranch, HTMLURL: raw.HTMLURL,
		Merge: MergeSettings{
			AllowMergeCommit:    raw.AllowMergeCommit,
			AllowSquashMerge:    raw.AllowSquashMerge,
			AllowRebaseMerge:    raw.AllowRebaseMerge,
			AllowAutoMerge:      raw.AllowAutoMerge,
			AllowUpdateBranch:   raw.AllowUpdateBranch,
			DeleteBranchOnMerge: raw.DeleteBranchOnMerge,
			MergeCommitTitle:    raw.MergeCommitTitle,
			MergeCommitMessage:  raw.MergeCommitMessage,
			SquashMergeTitle:    raw.SquashMergeCommitTitle,
			SquashMergeMessage:  raw.SquashMergeCommitMessage,
		},
		Security: SecuritySettings{
			Available: raw.SecurityAndAnalysis != nil,
			Features:  security,
		},
	}
	if raw.Parent != nil {
		if raw.Parent.ID <= 0 || !githubOwnerPattern.MatchString(raw.Parent.Owner.Login) ||
			!githubNamePattern.MatchString(raw.Parent.Name) ||
			!strings.EqualFold(raw.Parent.FullName, raw.Parent.Owner.Login+"/"+raw.Parent.Name) {
			return Repository{}, fmt.Errorf("GitHub fork parent identity is incomplete or inconsistent")
		}
		repository.Parent = &RepositoryLocator{
			ID: raw.Parent.ID, Owner: raw.Parent.Owner.Login,
			Name: raw.Parent.Name, FullName: raw.Parent.FullName,
		}
	}
	return repository, nil
}

func normalizeSecuritySettings(source map[string]struct {
	Status string `json:"status"`
}) (map[string]string, error) {
	if len(source) > 32 {
		return nil, fmt.Errorf("GitHub repository security settings exceed the 32-feature bound")
	}
	result := make(map[string]string, len(source))
	for name, feature := range source {
		if !validPermissionName(name) ||
			(feature.Status != "enabled" && feature.Status != "disabled") {
			return nil, fmt.Errorf("GitHub repository security settings are invalid")
		}
		result[name] = feature.Status
	}
	return result, nil
}

func validMergeSetting(value string, allowed ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func safePathSegment(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, "/\\\x00\r\n")
}

func safeBranchName(value string) bool {
	return gitref.ValidBranchName(value)
}
