// Package governance compares typed GitHub observations with compiled policy.
package governance

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

// StableSnapshot contains only decision-relevant GitHub state. Volatile request,
// rate-limit, permission-evidence, and observation-time metadata are deliberately
// excluded so the digest can be used as an optimistic-concurrency precondition.
type StableSnapshot struct {
	InstallationID    string                                     `json:"installation_id"`
	Repository        StableRepository                           `json:"repository"`
	Actions           githubprovider.ActionsPermissions          `json:"actions"`
	SelectedActions   *githubprovider.SelectedActionsPermissions `json:"selected_actions,omitempty"`
	Workflow          githubprovider.WorkflowPermissions         `json:"workflow"`
	ImmutableReleases githubprovider.ImmutableReleases           `json:"immutable_releases"`
	Rulesets          []githubprovider.RulesetSummary            `json:"rulesets"`
}

type StableRepository struct {
	ID            int64                           `json:"id"`
	Owner         string                          `json:"owner"`
	Name          string                          `json:"name"`
	Private       bool                            `json:"private"`
	Visibility    string                          `json:"visibility"`
	Fork          bool                            `json:"fork"`
	Archived      bool                            `json:"archived"`
	Disabled      bool                            `json:"disabled"`
	DefaultBranch string                          `json:"default_branch"`
	Merge         githubprovider.MergeSettings    `json:"merge"`
	Security      githubprovider.SecuritySettings `json:"security"`
}

func Stabilize(snapshot githubprovider.GovernanceSnapshot) StableSnapshot {
	rulesets := append([]githubprovider.RulesetSummary(nil), snapshot.Rulesets...)
	sort.Slice(rulesets, func(left, right int) bool { return rulesets[left].ID < rulesets[right].ID })
	features := make(map[string]string, len(snapshot.Repository.Security.Features))
	for key, value := range snapshot.Repository.Security.Features {
		features[key] = value
	}
	var selected *githubprovider.SelectedActionsPermissions
	if snapshot.SelectedActions != nil {
		copy := *snapshot.SelectedActions
		copy.PatternsAllowed = append([]string(nil), snapshot.SelectedActions.PatternsAllowed...)
		sort.Strings(copy.PatternsAllowed)
		selected = &copy
	}
	return StableSnapshot{
		InstallationID: snapshot.InstallationID,
		Repository: StableRepository{
			ID: snapshot.Repository.ID, Owner: snapshot.Repository.Owner,
			Name: snapshot.Repository.Name, Private: snapshot.Repository.Private,
			Visibility: snapshot.Repository.Visibility, Fork: snapshot.Repository.Fork,
			Archived: snapshot.Repository.Archived, Disabled: snapshot.Repository.Disabled,
			DefaultBranch: snapshot.Repository.DefaultBranch, Merge: snapshot.Repository.Merge,
			Security: githubprovider.SecuritySettings{
				Available: snapshot.Repository.Security.Available, Features: features,
			},
		},
		Actions: snapshot.Actions, SelectedActions: selected,
		Workflow: snapshot.Workflow, ImmutableReleases: snapshot.ImmutableReleases,
		Rulesets: rulesets,
	}
}

func EvidenceDigest(snapshot githubprovider.GovernanceSnapshot) (string, error) {
	return canonicaljson.Digest(Stabilize(snapshot))
}

func StableEvidenceDigest(snapshot StableSnapshot) (string, error) {
	return canonicaljson.Digest(snapshot)
}

type FieldResult struct {
	Path       string `json:"path"`
	Management string `json:"management"`
	Status     string `json:"status"`
	Desired    any    `json:"desired,omitempty"`
	Observed   any    `json:"observed,omitempty"`
}

type Result struct {
	Status       string         `json:"status"`
	PolicyDigest string         `json:"policy_digest,omitempty"`
	Counts       map[string]int `json:"counts"`
	Fields       []FieldResult  `json:"fields"`
}

type observedField struct {
	path  string
	value any
}

func Compare(
	policy compiler.CompiledPolicyDocument,
	snapshot githubprovider.GovernanceSnapshot,
) Result {
	observed := []observedField{
		{"github.actions.allowed_actions", snapshot.Actions.AllowedActions},
		{"github.actions.enabled", snapshot.Actions.Enabled},
		{"github.actions.sha_pinning_required", snapshot.Actions.SHAPinningRequired},
		{"github.actions.selected_actions", snapshot.SelectedActions},
		{"github.merge.allow_auto_merge", snapshot.Repository.Merge.AllowAutoMerge},
		{"github.merge.allow_merge_commit", snapshot.Repository.Merge.AllowMergeCommit},
		{"github.merge.allow_rebase_merge", snapshot.Repository.Merge.AllowRebaseMerge},
		{"github.merge.allow_squash_merge", snapshot.Repository.Merge.AllowSquashMerge},
		{"github.merge.allow_update_branch", snapshot.Repository.Merge.AllowUpdateBranch},
		{"github.merge.delete_branch_on_merge", snapshot.Repository.Merge.DeleteBranchOnMerge},
		{"github.merge.merge_commit_message", snapshot.Repository.Merge.MergeCommitMessage},
		{"github.merge.merge_commit_title", snapshot.Repository.Merge.MergeCommitTitle},
		{"github.merge.squash_merge_commit_message", snapshot.Repository.Merge.SquashMergeMessage},
		{"github.merge.squash_merge_commit_title", snapshot.Repository.Merge.SquashMergeTitle},
		{"github.releases.immutable", snapshot.ImmutableReleases.Enabled},
		{"github.rulesets", snapshot.Rulesets},
		{"github.security", snapshot.Repository.Security},
		{"github.workflow.can_approve_pull_request_reviews", snapshot.Workflow.CanApprovePullRequestReview},
		{"github.workflow.default_workflow_permissions", snapshot.Workflow.Default},
	}
	result := Result{
		Status: "not-declared", PolicyDigest: policy.CompiledPolicy.Digest,
		Counts: map[string]int{}, Fields: []FieldResult{},
	}
	for _, field := range observed {
		contract, found := lookupContract(policy.Effective, field.path)
		if !found {
			continue
		}
		management, _ := contract["management"].(string)
		entry := FieldResult{
			Path: field.path, Management: management, Observed: field.value,
		}
		switch management {
		case "managed":
			entry.Desired = contract["value"]
			if semanticallyEqual(entry.Desired, entry.Observed) {
				entry.Status = "compliant"
			} else {
				entry.Status = "drift"
			}
		case "observed":
			entry.Status = "observed"
		case "ignored":
			entry.Status = "ignored"
		default:
			entry.Status = "invalid-policy"
		}
		result.Counts[entry.Status]++
		result.Fields = append(result.Fields, entry)
	}
	sort.Slice(result.Fields, func(left, right int) bool {
		return result.Fields[left].Path < result.Fields[right].Path
	})
	switch {
	case result.Counts["invalid-policy"] != 0:
		result.Status = "invalid-policy"
	case result.Counts["drift"] != 0:
		result.Status = "drift"
	case result.Counts["compliant"] != 0:
		result.Status = "compliant"
	case result.Counts["observed"] != 0 || result.Counts["ignored"] != 0:
		result.Status = "observed-only"
	}
	return result
}

func semanticallyEqual(left any, right any) bool {
	normalize := func(value any) (any, bool) {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, false
		}
		var normalized any
		if err := json.Unmarshal(raw, &normalized); err != nil {
			return nil, false
		}
		return normalized, true
	}
	leftValue, leftOK := normalize(left)
	rightValue, rightOK := normalize(right)
	return leftOK && rightOK && reflect.DeepEqual(leftValue, rightValue)
}

func lookupContract(root map[string]any, path string) (map[string]any, bool) {
	current := root
	parts := strings.Split(path, ".")
	for index, part := range parts {
		value, found := current[part]
		if !found {
			return nil, false
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		if index == len(parts)-1 {
			return object, true
		}
		current = object
	}
	return nil, false
}
