// Package githubchange implements repository-bound GitHub change-set operations.
package githubchange

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitref"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

const (
	BranchAction       = "github-change-branch"
	ContentAction      = "github-change-content"
	DraftPRAction      = "github-change-draft-pr"
	maxChangeFiles     = 100
	maxContentBytes    = 1 << 20
	maxPullRequestBody = 64 << 10
)

type Scope struct {
	ReadInstallationID   string `json:"read_installation_id"`
	MutationCapabilityID string `json:"mutation_capability_id"`
	ProviderRepositoryID int64  `json:"provider_repository_id"`
	Owner                string `json:"owner"`
	Name                 string `json:"name"`
	ChangeSetDigest      string `json:"change_set_digest"`
	BaseBranch           string `json:"base_branch"`
	BaseSHA              string `json:"base_sha"`
	HeadBranch           string `json:"head_branch"`
}

type BranchChange struct {
	ExpectedState string `json:"expected_state"`
	ExpectedSHA   string `json:"expected_sha,omitempty"`
	TargetSHA     string `json:"target_sha"`
}

type ContentChange struct {
	Path          string `json:"path"`
	Message       string `json:"message"`
	ExpectedState string `json:"expected_state"`
	ExpectedSHA   string `json:"expected_sha,omitempty"`
	FinalStatus   string `json:"final_status"`
	ContentBase64 string `json:"content_base64"`
	ContentDigest string `json:"content_digest"`
}

type PlannedFile struct {
	Path          string `json:"path"`
	Status        string `json:"status"`
	ContentDigest string `json:"content_digest"`
}

type PullRequestChange struct {
	Title string        `json:"title"`
	Body  string        `json:"body"`
	Files []PlannedFile `json:"files"`
}

type OperationParameters struct {
	Scope       Scope              `json:"scope"`
	Branch      *BranchChange      `json:"branch,omitempty"`
	Content     *ContentChange     `json:"content,omitempty"`
	PullRequest *PullRequestChange `json:"pull_request,omitempty"`
}

func Parameters(value OperationParameters) map[string]any {
	return map[string]any{"github_change": value}
}

func StepParameters(step operations.Step) (OperationParameters, error) {
	if len(step.Parameters) != 1 {
		return OperationParameters{}, errors.New("GitHub change step must contain one parameter domain")
	}
	raw, found := step.Parameters["github_change"]
	if !found {
		return OperationParameters{}, errors.New("github_change parameters are missing")
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return OperationParameters{}, fmt.Errorf("encode GitHub change parameters: %w", err)
	}
	var value OperationParameters
	if err := json.Unmarshal(payload, &value); err != nil {
		return OperationParameters{}, fmt.Errorf("decode GitHub change parameters: %w", err)
	}
	if err := validateParameters(step.Action, value); err != nil {
		return OperationParameters{}, err
	}
	return value, nil
}

func ValidatePlan(plan operations.Plan) error {
	if len(plan.Steps) < 3 || len(plan.Steps) > maxChangeFiles+2 ||
		plan.Steps[0].Action != BranchAction || plan.Steps[len(plan.Steps)-1].Action != DraftPRAction {
		return errors.New("GitHub change plan must contain branch, content, and final draft-PR steps")
	}
	first, err := StepParameters(plan.Steps[0])
	if err != nil || first.Branch == nil ||
		(first.Branch.ExpectedState == "missing" && first.Branch.TargetSHA != first.Scope.BaseSHA) ||
		(first.Branch.ExpectedState == "present" && first.Branch.TargetSHA != first.Branch.ExpectedSHA) {
		return errors.New("GitHub change plan branch step is invalid")
	}
	contentFiles := make(map[string]PlannedFile, len(plan.Steps)-2)
	for index, step := range plan.Steps {
		parameters, parseErr := StepParameters(step)
		if parseErr != nil || !sameScope(first.Scope, parameters.Scope) ||
			step.RepositoryID != plan.Steps[0].RepositoryID {
			return errors.New("GitHub change plan steps have different immutable scopes")
		}
		switch {
		case index == 0:
			if step.Action != BranchAction {
				return errors.New("GitHub change branch step must be first")
			}
		case index == len(plan.Steps)-1:
			if step.Action != DraftPRAction || parameters.PullRequest == nil {
				return errors.New("GitHub change draft-PR step must be last")
			}
		case step.Action == ContentAction && parameters.Content != nil:
			if _, duplicate := contentFiles[parameters.Content.Path]; duplicate {
				return errors.New("GitHub change plan contains duplicate content paths")
			}
			contentFiles[parameters.Content.Path] = PlannedFile{
				Path: parameters.Content.Path, Status: parameters.Content.FinalStatus,
				ContentDigest: parameters.Content.ContentDigest,
			}
		default:
			return errors.New("GitHub change plan contains an unexpected action order")
		}
	}
	last, _ := StepParameters(plan.Steps[len(plan.Steps)-1])
	if !samePlannedFiles(contentFiles, last.PullRequest.Files) {
		return errors.New("GitHub draft-PR file contract differs from content steps")
	}
	return nil
}

func DecodeContent(change ContentChange) ([]byte, error) {
	content, err := base64.StdEncoding.DecodeString(change.ContentBase64)
	if err != nil || len(content) > maxContentBytes || base64.StdEncoding.EncodeToString(content) != change.ContentBase64 {
		return nil, errors.New("GitHub change content encoding is invalid")
	}
	if DigestContent(content) != change.ContentDigest {
		return nil, errors.New("GitHub change content digest is invalid")
	}
	return content, nil
}

func validateParameters(action string, value OperationParameters) error {
	if value.Scope.ReadInstallationID == "" || value.Scope.MutationCapabilityID == "" ||
		value.Scope.ProviderRepositoryID <= 0 || value.Scope.Owner == "" || value.Scope.Name == "" ||
		!digest(value.Scope.ChangeSetDigest) || !branchName(value.Scope.BaseBranch) ||
		!gitOID(value.Scope.BaseSHA) || !branchName(value.Scope.HeadBranch) ||
		value.Scope.BaseBranch == value.Scope.HeadBranch {
		return errors.New("GitHub change scope is invalid")
	}
	count := 0
	for _, present := range []bool{value.Branch != nil, value.Content != nil, value.PullRequest != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return errors.New("GitHub change step must contain exactly one typed change")
	}
	switch action {
	case BranchAction:
		return validateBranch(value.Branch)
	case ContentAction:
		return validateContent(value.Content)
	case DraftPRAction:
		return validatePullRequest(value.PullRequest)
	default:
		return fmt.Errorf("unsupported GitHub change action %q", action)
	}
}

func validateBranch(change *BranchChange) error {
	if change == nil || !gitOID(change.TargetSHA) {
		return errors.New("GitHub branch change is invalid")
	}
	switch change.ExpectedState {
	case "missing":
		if change.ExpectedSHA != "" {
			return errors.New("missing GitHub branch cannot have an expected SHA")
		}
	case "present":
		if !gitOID(change.ExpectedSHA) {
			return errors.New("present GitHub branch requires an expected SHA")
		}
	default:
		return errors.New("GitHub branch expected state is invalid")
	}
	return nil
}

func validateContent(change *ContentChange) error {
	if change == nil || !safeRelativePath(change.Path) || !singleLine(change.Message, 512) ||
		(change.FinalStatus != "added" && change.FinalStatus != "modified") ||
		!digest(change.ContentDigest) {
		return errors.New("GitHub content change is invalid")
	}
	switch change.ExpectedState {
	case "missing":
		if change.ExpectedSHA != "" {
			return errors.New("missing GitHub content cannot have an expected SHA")
		}
	case "regular":
		if !gitOID(change.ExpectedSHA) {
			return errors.New("regular GitHub content requires an expected SHA")
		}
	default:
		return errors.New("GitHub content expected state is invalid")
	}
	_, err := DecodeContent(*change)
	return err
}

func validatePullRequest(change *PullRequestChange) error {
	if change == nil || !singleLine(change.Title, 256) || len(change.Body) > maxPullRequestBody ||
		strings.ContainsRune(change.Body, 0) || len(change.Files) == 0 || len(change.Files) > maxChangeFiles {
		return errors.New("GitHub draft pull-request change is invalid")
	}
	seen := map[string]struct{}{}
	for _, file := range change.Files {
		if !safeRelativePath(file.Path) || (file.Status != "added" && file.Status != "modified") ||
			!digest(file.ContentDigest) {
			return errors.New("GitHub draft pull-request file is invalid")
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return errors.New("GitHub draft pull-request files contain duplicates")
		}
		seen[file.Path] = struct{}{}
	}
	return nil
}

func sameScope(left Scope, right Scope) bool {
	return left == right
}

func samePlannedFiles(expected map[string]PlannedFile, actual []PlannedFile) bool {
	if len(expected) != len(actual) {
		return false
	}
	for _, file := range actual {
		if value, found := expected[file.Path]; !found || value != file {
			return false
		}
	}
	return true
}

func SortedFiles(values []PlannedFile) []PlannedFile {
	result := append([]PlannedFile(nil), values...)
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	return result
}

func branchName(value string) bool {
	return gitref.ValidBranchName(value)
}

func safeRelativePath(value string) bool {
	if value == "" || len(value) > 4096 || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			return false
		}
	}
	return true
}

func singleLine(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}

func gitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func digest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && len(value) == len("sha256:")+64 && gitOID(strings.TrimPrefix(value, "sha256:"))
}
