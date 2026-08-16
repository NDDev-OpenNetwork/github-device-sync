package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

type RemediationScope struct {
	RepositoryID         string
	ReadInstallationID   string
	MutationCapabilityID string
	ProviderRepositoryID int64
	Owner                string
	Name                 string
}

type Remediation struct {
	Steps                 []operations.Step `json:"steps"`
	InitialEvidenceDigest string            `json:"initial_evidence_digest"`
	FinalEvidenceDigest   string            `json:"final_evidence_digest,omitempty"`
	RequiresReplan        bool              `json:"requires_replan"`
}

func BuildRemediation(
	scope RemediationScope,
	snapshot githubprovider.GovernanceSnapshot,
	comparison Result,
) (Remediation, error) {
	if scope.RepositoryID == "" || scope.ReadInstallationID == "" ||
		scope.MutationCapabilityID == "" || scope.ProviderRepositoryID <= 0 ||
		scope.Owner == "" || scope.Name == "" {
		return Remediation{}, errors.New("GitHub governance remediation scope is incomplete")
	}
	if snapshot.InstallationID != scope.ReadInstallationID ||
		snapshot.Repository.ID != scope.ProviderRepositoryID ||
		!strings.EqualFold(snapshot.Repository.Owner, scope.Owner) ||
		!strings.EqualFold(snapshot.Repository.Name, scope.Name) {
		return Remediation{}, errors.New("GitHub governance remediation scope differs from observed identity")
	}
	if comparison.Status == "invalid-policy" || comparison.PolicyDigest == "" {
		return Remediation{}, errors.New("GitHub governance remediation requires one valid compiled policy")
	}
	initialDigest, err := EvidenceDigest(snapshot)
	if err != nil {
		return Remediation{}, err
	}
	result := Remediation{
		Steps: []operations.Step{}, InitialEvidenceDigest: initialDigest,
		FinalEvidenceDigest: initialDigest,
	}
	predicted := snapshot
	mergeDesired, mergeDrift, err := desiredMerge(snapshot.Repository.Merge, comparison)
	if err != nil {
		return Remediation{}, err
	}
	workflowDesired, workflowDrift, err := desiredWorkflow(snapshot.Workflow, comparison)
	if err != nil {
		return Remediation{}, err
	}
	actionsDesired, actionsDrift, err := desiredActions(snapshot.Actions, comparison)
	if err != nil {
		return Remediation{}, err
	}
	selectedDesired, selectedDrift, err := desiredSelected(snapshot.SelectedActions, comparison)
	if err != nil {
		return Remediation{}, err
	}
	immutableDesired, immutableDrift, err := desiredImmutableReleases(snapshot.ImmutableReleases, comparison)
	if err != nil {
		return Remediation{}, err
	}

	appendExact := func(action string, id string, parameters OperationParameters, update func(*githubprovider.GovernanceSnapshot)) error {
		expectedDigest, digestErr := EvidenceDigest(predicted)
		if digestErr != nil {
			return digestErr
		}
		parameters.ExpectedEvidenceDigest = expectedDigest
		parameters.PostState = "exact"
		update(&predicted)
		desiredDigest, digestErr := EvidenceDigest(predicted)
		if digestErr != nil {
			return digestErr
		}
		parameters.DesiredEvidenceDigest = desiredDigest
		result.Steps = append(result.Steps, remediationStep(scope.RepositoryID, id, action, parameters))
		result.FinalEvidenceDigest = desiredDigest
		return nil
	}
	base := func() OperationParameters {
		return OperationParameters{
			ReadInstallationID:   scope.ReadInstallationID,
			MutationCapabilityID: scope.MutationCapabilityID,
			ProviderRepositoryID: scope.ProviderRepositoryID,
			Owner:                scope.Owner, Name: scope.Name,
		}
	}
	if mergeDrift {
		parameters := base()
		parameters.RepositorySettings = &MergeChange{Expected: predicted.Repository.Merge, Desired: mergeDesired}
		if err := appendExact(RepositorySettingsAction, "set-github-repository-settings", parameters, func(value *githubprovider.GovernanceSnapshot) {
			value.Repository.Merge = mergeDesired
		}); err != nil {
			return Remediation{}, err
		}
	}
	if workflowDrift {
		parameters := base()
		parameters.WorkflowPermissions = &WorkflowChange{Expected: predicted.Workflow, Desired: workflowDesired}
		if err := appendExact(WorkflowPermissionsAction, "set-github-workflow-permissions", parameters, func(value *githubprovider.GovernanceSnapshot) {
			value.Workflow = workflowDesired
		}); err != nil {
			return Remediation{}, err
		}
	}
	if actionsDrift && !predicted.Actions.Enabled && actionsDesired.Enabled &&
		predicted.Actions.AllowedActions == "" && actionsDesired.AllowedActions == "" {
		parameters := base()
		parameters.ExpectedEvidenceDigest = result.FinalEvidenceDigest
		parameters.PostState = "discover-actions-permissions"
		parameters.ActionsPermissions = &ActionsChange{Expected: predicted.Actions, Desired: actionsDesired}
		result.Steps = append(result.Steps, remediationStep(
			scope.RepositoryID, "enable-github-actions", ActionsPermissionsAction, parameters,
		))
		result.FinalEvidenceDigest = ""
		result.RequiresReplan = true
		return result, nil
	}
	if actionsDrift && predicted.Actions.AllowedActions != "selected" && actionsDesired.AllowedActions == "selected" {
		parameters := base()
		parameters.ExpectedEvidenceDigest = result.FinalEvidenceDigest
		parameters.PostState = "discover-selected-actions"
		parameters.ActionsPermissions = &ActionsChange{Expected: predicted.Actions, Desired: actionsDesired}
		result.Steps = append(result.Steps, remediationStep(
			scope.RepositoryID, "select-github-actions-policy", ActionsPermissionsAction, parameters,
		))
		result.FinalEvidenceDigest = ""
		result.RequiresReplan = true
		return result, nil
	}
	if actionsDrift {
		parameters := base()
		parameters.ActionsPermissions = &ActionsChange{Expected: predicted.Actions, Desired: actionsDesired}
		if err := appendExact(ActionsPermissionsAction, "set-github-actions-permissions", parameters, func(value *githubprovider.GovernanceSnapshot) {
			value.Actions = actionsDesired
		}); err != nil {
			return Remediation{}, err
		}
	}
	if selectedDrift {
		if predicted.Actions.AllowedActions != "selected" || predicted.SelectedActions == nil || selectedDesired == nil {
			return Remediation{}, errors.New("selected Actions state is not observable; change allowed_actions and re-plan")
		}
		parameters := base()
		parameters.SelectedActions = &SelectedActionsChange{
			Expected: *predicted.SelectedActions, Desired: *selectedDesired,
		}
		if err := appendExact(SelectedActionsPermissionsAction, "set-github-selected-actions", parameters, func(value *githubprovider.GovernanceSnapshot) {
			copy := *selectedDesired
			copy.PatternsAllowed = append([]string(nil), selectedDesired.PatternsAllowed...)
			value.SelectedActions = &copy
		}); err != nil {
			return Remediation{}, err
		}
	}
	if immutableDrift {
		parameters := base()
		parameters.ImmutableReleases = &ImmutableReleasesChange{
			Expected: predicted.ImmutableReleases, Desired: immutableDesired,
		}
		if err := appendExact(ImmutableReleasesAction, "set-github-immutable-releases", parameters, func(value *githubprovider.GovernanceSnapshot) {
			value.ImmutableReleases = immutableDesired
		}); err != nil {
			return Remediation{}, err
		}
	}
	return result, nil
}

func remediationStep(repositoryID, stepID, action string, parameters OperationParameters) operations.Step {
	return operations.Step{
		StepID: stepID, RepositoryID: repositoryID, Action: action,
		RequiresApproval: true, Compensation: operations.Compensation{Mode: "manual"},
		Parameters: Parameters(parameters),
	}
}

func desiredMerge(current githubprovider.MergeSettings, comparison Result) (githubprovider.MergeSettings, bool, error) {
	desired := current
	drift := false
	for _, field := range comparison.Fields {
		if field.Management != "managed" || !strings.HasPrefix(field.Path, "github.merge.") {
			continue
		}
		if field.Status == "drift" {
			drift = true
		}
		switch field.Path {
		case "github.merge.allow_merge_commit":
			value, ok := field.Desired.(bool)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.AllowMergeCommit = value
		case "github.merge.allow_squash_merge":
			value, ok := field.Desired.(bool)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.AllowSquashMerge = value
		case "github.merge.allow_rebase_merge":
			value, ok := field.Desired.(bool)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.AllowRebaseMerge = value
		case "github.merge.allow_auto_merge":
			value, ok := field.Desired.(bool)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.AllowAutoMerge = value
		case "github.merge.allow_update_branch":
			value, ok := field.Desired.(bool)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.AllowUpdateBranch = value
		case "github.merge.delete_branch_on_merge":
			value, ok := field.Desired.(bool)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.DeleteBranchOnMerge = value
		case "github.merge.merge_commit_title":
			value, ok := field.Desired.(string)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.MergeCommitTitle = value
		case "github.merge.merge_commit_message":
			value, ok := field.Desired.(string)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.MergeCommitMessage = value
		case "github.merge.squash_merge_commit_title":
			value, ok := field.Desired.(string)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.SquashMergeTitle = value
		case "github.merge.squash_merge_commit_message":
			value, ok := field.Desired.(string)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.SquashMergeMessage = value
		}
	}
	return desired, drift, nil
}

func desiredActions(current githubprovider.ActionsPermissions, comparison Result) (githubprovider.ActionsPermissions, bool, error) {
	desired := current
	drift := false
	for _, field := range comparison.Fields {
		if field.Management != "managed" || !strings.HasPrefix(field.Path, "github.actions.") || field.Path == "github.actions.selected_actions" {
			continue
		}
		if field.Status == "drift" {
			drift = true
		}
		switch field.Path {
		case "github.actions.enabled":
			value, ok := field.Desired.(bool)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.Enabled = value
		case "github.actions.allowed_actions":
			value, ok := field.Desired.(string)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.AllowedActions = value
		case "github.actions.sha_pinning_required":
			value, ok := field.Desired.(bool)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.SHAPinningRequired = value
		}
	}
	return desired, drift, nil
}

func desiredWorkflow(current githubprovider.WorkflowPermissions, comparison Result) (githubprovider.WorkflowPermissions, bool, error) {
	desired := current
	drift := false
	for _, field := range comparison.Fields {
		if field.Management != "managed" || !strings.HasPrefix(field.Path, "github.workflow.") {
			continue
		}
		if field.Status == "drift" {
			drift = true
		}
		switch field.Path {
		case "github.workflow.default_workflow_permissions":
			value, ok := field.Desired.(string)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.Default = value
		case "github.workflow.can_approve_pull_request_reviews":
			value, ok := field.Desired.(bool)
			if !ok {
				return desired, false, invalidDesired(field.Path)
			}
			desired.CanApprovePullRequestReview = value
		}
	}
	return desired, drift, nil
}

func desiredSelected(current *githubprovider.SelectedActionsPermissions, comparison Result) (*githubprovider.SelectedActionsPermissions, bool, error) {
	var desired *githubprovider.SelectedActionsPermissions
	if current != nil {
		copy := *current
		copy.PatternsAllowed = append([]string(nil), current.PatternsAllowed...)
		desired = &copy
	}
	drift := false
	for _, field := range comparison.Fields {
		if field.Path != "github.actions.selected_actions" || field.Management != "managed" {
			continue
		}
		if field.Status == "drift" {
			drift = true
		}
		raw, err := json.Marshal(field.Desired)
		if err != nil {
			return nil, false, invalidDesired(field.Path)
		}
		var value githubprovider.SelectedActionsPermissions
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, false, invalidDesired(field.Path)
		}
		sort.Strings(value.PatternsAllowed)
		desired = &value
	}
	return desired, drift, nil
}

func desiredImmutableReleases(
	current githubprovider.ImmutableReleases,
	comparison Result,
) (githubprovider.ImmutableReleases, bool, error) {
	desired := current
	drift := false
	for _, field := range comparison.Fields {
		if field.Path != "github.releases.immutable" || field.Management != "managed" {
			continue
		}
		value, ok := field.Desired.(bool)
		if !ok {
			return desired, false, invalidDesired(field.Path)
		}
		if !value && current.EnforcedByOwner {
			return desired, false, errors.New("immutable releases are enforced by the owner and cannot be disabled at repository scope")
		}
		desired.Enabled = value
		if field.Status == "drift" {
			drift = true
		}
	}
	return desired, drift, nil
}

func invalidDesired(path string) error {
	return fmt.Errorf("compiled GitHub governance value at %q has an invalid type", path)
}
