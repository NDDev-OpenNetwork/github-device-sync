package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

const (
	RepositorySettingsAction         = "github-repository-settings"
	ActionsPermissionsAction         = "github-actions-permissions"
	SelectedActionsPermissionsAction = "github-selected-actions-permissions"
	WorkflowPermissionsAction        = "github-workflow-permissions"
	ImmutableReleasesAction          = "github-immutable-releases"
)

type GovernanceReader interface {
	GetRepositoryGovernance(context.Context, string, string) (githubprovider.GovernanceSnapshot, error)
}

type GovernanceWriter interface {
	Scope() githubprovider.RepositoryMutationScope
	UpdateRepositorySettings(context.Context, githubprovider.RepositorySettingsUpdate) (githubprovider.Repository, githubprovider.MutationMeta, error)
	SetActionsPermissions(context.Context, githubprovider.ActionsPermissionsUpdate) (githubprovider.MutationMeta, error)
	SetSelectedActionsPermissions(context.Context, githubprovider.SelectedActionsPermissions) (githubprovider.MutationMeta, error)
	SetWorkflowPermissions(context.Context, githubprovider.WorkflowPermissionsUpdate) (githubprovider.MutationMeta, error)
	SetImmutableReleases(context.Context, bool) (githubprovider.MutationMeta, error)
}

type MergeChange struct {
	Expected githubprovider.MergeSettings `json:"expected"`
	Desired  githubprovider.MergeSettings `json:"desired"`
}

type ActionsChange struct {
	Expected githubprovider.ActionsPermissions `json:"expected"`
	Desired  githubprovider.ActionsPermissions `json:"desired"`
}

type SelectedActionsChange struct {
	Expected githubprovider.SelectedActionsPermissions `json:"expected"`
	Desired  githubprovider.SelectedActionsPermissions `json:"desired"`
}

type WorkflowChange struct {
	Expected githubprovider.WorkflowPermissions `json:"expected"`
	Desired  githubprovider.WorkflowPermissions `json:"desired"`
}

type ImmutableReleasesChange struct {
	Expected githubprovider.ImmutableReleases `json:"expected"`
	Desired  githubprovider.ImmutableReleases `json:"desired"`
}

type OperationParameters struct {
	ReadInstallationID     string                   `json:"read_installation_id"`
	MutationCapabilityID   string                   `json:"mutation_capability_id"`
	ProviderRepositoryID   int64                    `json:"provider_repository_id"`
	Owner                  string                   `json:"owner"`
	Name                   string                   `json:"name"`
	PostState              string                   `json:"post_state"`
	ExpectedEvidenceDigest string                   `json:"expected_evidence_digest"`
	DesiredEvidenceDigest  string                   `json:"desired_evidence_digest,omitempty"`
	RepositorySettings     *MergeChange             `json:"repository_settings,omitempty"`
	ActionsPermissions     *ActionsChange           `json:"actions_permissions,omitempty"`
	SelectedActions        *SelectedActionsChange   `json:"selected_actions_permissions,omitempty"`
	WorkflowPermissions    *WorkflowChange          `json:"workflow_permissions,omitempty"`
	ImmutableReleases      *ImmutableReleasesChange `json:"immutable_releases,omitempty"`
}

type StepEvidence struct {
	State      StableSnapshot               `json:"state"`
	Digest     string                       `json:"digest"`
	Mutation   *githubprovider.MutationMeta `json:"mutation,omitempty"`
	Idempotent bool                         `json:"idempotent"`
}

type Handler struct {
	Reader GovernanceReader
	Writer GovernanceWriter
	Scope  githubprovider.RepositoryMutationScope
	Action string
}

func Parameters(value OperationParameters) map[string]any {
	return map[string]any{"github_governance": value}
}

func StepParameters(step operations.Step) (OperationParameters, error) {
	raw, found := step.Parameters["github_governance"]
	if !found {
		return OperationParameters{}, errors.New("github_governance parameters are missing")
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return OperationParameters{}, fmt.Errorf("encode GitHub governance parameters: %w", err)
	}
	var value OperationParameters
	if err := json.Unmarshal(payload, &value); err != nil {
		return OperationParameters{}, fmt.Errorf("decode GitHub governance parameters: %w", err)
	}
	if err := validateParameters(step.Action, value); err != nil {
		return OperationParameters{}, err
	}
	return value, nil
}

func (handler *Handler) Apply(ctx context.Context, step operations.Step) (operations.ApplyEvidence, error) {
	parameters, err := StepParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if err := handler.validateBinding(step.Action, parameters); err != nil {
		return operations.ApplyEvidence{}, err
	}
	if handler.Writer == nil {
		return operations.ApplyEvidence{}, errors.New("GitHub governance mutation writer is unavailable")
	}
	before, beforeDigest, err := handler.observe(ctx, parameters)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	beforeEvidence := StepEvidence{State: Stabilize(before), Digest: beforeDigest}
	if acceptablePostState(step.Action, parameters, before, beforeDigest) {
		beforeEvidence.Idempotent = true
		return operations.ApplyEvidence{Before: beforeEvidence, After: beforeEvidence}, nil
	}
	if beforeDigest != parameters.ExpectedEvidenceDigest || !fieldMatchesExpected(step.Action, parameters, before) {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub governance state changed after planning; provider mutation was not attempted",
		)
	}
	meta, err := handler.mutate(ctx, step.Action, parameters)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	after, afterDigest, err := handler.observe(ctx, parameters)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, fmt.Errorf(
			"GitHub mutation returned but post-mutation observation failed: %w", err,
		)
	}
	afterEvidence := StepEvidence{State: Stabilize(after), Digest: afterDigest, Mutation: &meta}
	if !acceptablePostState(step.Action, parameters, after, afterDigest) {
		return operations.ApplyEvidence{Before: beforeEvidence, After: afterEvidence}, errors.New(
			"GitHub mutation completed but exact desired governance state was not observed",
		)
	}
	return operations.ApplyEvidence{Before: beforeEvidence, After: afterEvidence}, nil
}

func (handler *Handler) Verify(ctx context.Context, step operations.Step, recorded json.RawMessage) error {
	parameters, err := StepParameters(step)
	if err != nil {
		return err
	}
	if err := handler.validateBinding(step.Action, parameters); err != nil {
		return err
	}
	var evidence StepEvidence
	if err := json.Unmarshal(recorded, &evidence); err != nil {
		return fmt.Errorf("decode recorded GitHub governance evidence: %w", err)
	}
	if evidence.Digest == "" ||
		(parameters.PostState == "exact" && evidence.Digest != parameters.DesiredEvidenceDigest) {
		return errors.New("recorded GitHub governance evidence does not match the planned post-state")
	}
	recordedDigest, err := StableEvidenceDigest(evidence.State)
	if err != nil || recordedDigest != evidence.Digest {
		return errors.New("recorded GitHub governance state does not match its evidence digest")
	}
	current, _, err := handler.observe(ctx, parameters)
	if err != nil {
		return err
	}
	currentMatches := fieldMatchesDesired(step.Action, parameters, current)
	if parameters.PostState == "discover-selected-actions" {
		currentMatches = currentMatches && current.SelectedActions != nil
	} else if parameters.PostState == "discover-actions-permissions" {
		currentMatches = acceptablePostState(step.Action, parameters, current, "")
	}
	if !currentMatches {
		return errors.New("current GitHub governance state does not match verified operation evidence")
	}
	return nil
}

func (handler *Handler) observe(ctx context.Context, parameters OperationParameters) (githubprovider.GovernanceSnapshot, string, error) {
	snapshot, err := handler.Reader.GetRepositoryGovernance(ctx, parameters.Owner, parameters.Name)
	if err != nil {
		return githubprovider.GovernanceSnapshot{}, "", err
	}
	if snapshot.InstallationID != parameters.ReadInstallationID ||
		snapshot.Repository.ID != parameters.ProviderRepositoryID ||
		!strings.EqualFold(snapshot.Repository.Owner, parameters.Owner) ||
		!strings.EqualFold(snapshot.Repository.Name, parameters.Name) {
		return githubprovider.GovernanceSnapshot{}, "", errors.New("GitHub governance observation identity mismatch")
	}
	digest, err := EvidenceDigest(snapshot)
	return snapshot, digest, err
}

func (handler *Handler) validateBinding(action string, parameters OperationParameters) error {
	if handler == nil || handler.Reader == nil || handler.Action != action {
		return errors.New("GitHub governance handler binding is incomplete")
	}
	scope := handler.Scope
	if handler.Writer != nil {
		writerScope := handler.Writer.Scope()
		if scope.RepositoryID != 0 && !reflect.DeepEqual(scope, writerScope) {
			return errors.New("GitHub governance handler and writer scopes differ")
		}
		scope = writerScope
	}
	if scope.RepositoryID != parameters.ProviderRepositoryID ||
		!strings.EqualFold(scope.Owner, parameters.Owner) || !strings.EqualFold(scope.Name, parameters.Name) {
		return errors.New("GitHub mutation writer identity differs from the immutable plan")
	}
	return nil
}

func (handler *Handler) mutate(ctx context.Context, action string, parameters OperationParameters) (githubprovider.MutationMeta, error) {
	switch action {
	case RepositorySettingsAction:
		expected := parameters.RepositorySettings.Expected
		desired := parameters.RepositorySettings.Desired
		update := githubprovider.RepositorySettingsUpdate{}
		if expected.AllowMergeCommit != desired.AllowMergeCommit {
			update.AllowMergeCommit = &desired.AllowMergeCommit
		}
		if expected.AllowSquashMerge != desired.AllowSquashMerge {
			update.AllowSquashMerge = &desired.AllowSquashMerge
		}
		if expected.AllowRebaseMerge != desired.AllowRebaseMerge {
			update.AllowRebaseMerge = &desired.AllowRebaseMerge
		}
		if expected.AllowAutoMerge != desired.AllowAutoMerge {
			update.AllowAutoMerge = &desired.AllowAutoMerge
		}
		if expected.AllowUpdateBranch != desired.AllowUpdateBranch {
			update.AllowUpdateBranch = &desired.AllowUpdateBranch
		}
		if expected.DeleteBranchOnMerge != desired.DeleteBranchOnMerge {
			update.DeleteBranchOnMerge = &desired.DeleteBranchOnMerge
		}
		if expected.MergeCommitTitle != desired.MergeCommitTitle {
			update.MergeCommitTitle = &desired.MergeCommitTitle
		}
		if expected.MergeCommitMessage != desired.MergeCommitMessage {
			update.MergeCommitMessage = &desired.MergeCommitMessage
		}
		if expected.SquashMergeTitle != desired.SquashMergeTitle {
			update.SquashMergeCommitTitle = &desired.SquashMergeTitle
		}
		if expected.SquashMergeMessage != desired.SquashMergeMessage {
			update.SquashMergeCommitMessage = &desired.SquashMergeMessage
		}
		_, meta, err := handler.Writer.UpdateRepositorySettings(ctx, update)
		return meta, err
	case ActionsPermissionsAction:
		expected := parameters.ActionsPermissions.Expected
		desired := parameters.ActionsPermissions.Desired
		update := githubprovider.ActionsPermissionsUpdate{Enabled: desired.Enabled}
		if expected.AllowedActions != desired.AllowedActions && desired.AllowedActions != "" {
			update.AllowedActions = &desired.AllowedActions
		}
		if expected.SHAPinningRequired != desired.SHAPinningRequired {
			update.SHAPinningRequired = &desired.SHAPinningRequired
		}
		return handler.Writer.SetActionsPermissions(ctx, update)
	case SelectedActionsPermissionsAction:
		return handler.Writer.SetSelectedActionsPermissions(ctx, parameters.SelectedActions.Desired)
	case WorkflowPermissionsAction:
		desired := parameters.WorkflowPermissions.Desired
		return handler.Writer.SetWorkflowPermissions(ctx, githubprovider.WorkflowPermissionsUpdate(desired))
	case ImmutableReleasesAction:
		return handler.Writer.SetImmutableReleases(ctx, parameters.ImmutableReleases.Desired.Enabled)
	default:
		return githubprovider.MutationMeta{}, fmt.Errorf("unsupported GitHub governance action %q", action)
	}
}

func fieldMatchesExpected(action string, parameters OperationParameters, snapshot githubprovider.GovernanceSnapshot) bool {
	switch action {
	case RepositorySettingsAction:
		return reflect.DeepEqual(parameters.RepositorySettings.Expected, snapshot.Repository.Merge)
	case ActionsPermissionsAction:
		return reflect.DeepEqual(parameters.ActionsPermissions.Expected, snapshot.Actions)
	case SelectedActionsPermissionsAction:
		return snapshot.SelectedActions != nil && reflect.DeepEqual(parameters.SelectedActions.Expected, *snapshot.SelectedActions)
	case WorkflowPermissionsAction:
		return reflect.DeepEqual(parameters.WorkflowPermissions.Expected, snapshot.Workflow)
	case ImmutableReleasesAction:
		return reflect.DeepEqual(parameters.ImmutableReleases.Expected, snapshot.ImmutableReleases)
	default:
		return false
	}
}

func fieldMatchesDesired(action string, parameters OperationParameters, snapshot githubprovider.GovernanceSnapshot) bool {
	switch action {
	case RepositorySettingsAction:
		return reflect.DeepEqual(parameters.RepositorySettings.Desired, snapshot.Repository.Merge)
	case ActionsPermissionsAction:
		return reflect.DeepEqual(parameters.ActionsPermissions.Desired, snapshot.Actions)
	case SelectedActionsPermissionsAction:
		return snapshot.SelectedActions != nil && reflect.DeepEqual(parameters.SelectedActions.Desired, *snapshot.SelectedActions)
	case WorkflowPermissionsAction:
		return reflect.DeepEqual(parameters.WorkflowPermissions.Desired, snapshot.Workflow)
	case ImmutableReleasesAction:
		return reflect.DeepEqual(parameters.ImmutableReleases.Desired, snapshot.ImmutableReleases)
	default:
		return false
	}
}

func validateParameters(action string, parameters OperationParameters) error {
	if parameters.ReadInstallationID == "" || parameters.MutationCapabilityID == "" ||
		parameters.ProviderRepositoryID <= 0 || parameters.Owner == "" || parameters.Name == "" ||
		parameters.ExpectedEvidenceDigest == "" {
		return errors.New("GitHub governance parameters are incomplete")
	}
	count := 0
	for _, present := range []bool{
		parameters.RepositorySettings != nil, parameters.ActionsPermissions != nil,
		parameters.SelectedActions != nil, parameters.WorkflowPermissions != nil,
		parameters.ImmutableReleases != nil,
	} {
		if present {
			count++
		}
	}
	if count != 1 {
		return errors.New("GitHub governance step must contain exactly one typed state change")
	}
	valid := (action == RepositorySettingsAction && parameters.RepositorySettings != nil) ||
		(action == ActionsPermissionsAction && parameters.ActionsPermissions != nil) ||
		(action == SelectedActionsPermissionsAction && parameters.SelectedActions != nil) ||
		(action == WorkflowPermissionsAction && parameters.WorkflowPermissions != nil) ||
		(action == ImmutableReleasesAction && parameters.ImmutableReleases != nil)
	if !valid {
		return errors.New("GitHub governance action and typed state change differ")
	}
	switch parameters.PostState {
	case "exact":
		if parameters.DesiredEvidenceDigest == "" {
			return errors.New("exact GitHub governance post-state requires a desired evidence digest")
		}
		if parameters.ActionsPermissions != nil && parameters.ActionsPermissions.Desired.Enabled &&
			parameters.ActionsPermissions.Desired.AllowedActions == "" {
			return errors.New("exact enabled Actions post-state requires an observable allowed-actions policy")
		}
	case "discover-selected-actions":
		if action != ActionsPermissionsAction || parameters.DesiredEvidenceDigest != "" ||
			parameters.ActionsPermissions.Expected.AllowedActions == "selected" ||
			parameters.ActionsPermissions.Desired.AllowedActions != "selected" {
			return errors.New("selected-actions discovery is only valid for the policy transition to selected")
		}
	case "discover-actions-permissions":
		if action != ActionsPermissionsAction || parameters.ActionsPermissions == nil ||
			parameters.DesiredEvidenceDigest != "" {
			return errors.New("Actions-permissions discovery is only valid when enabling an unobservable policy")
		}
		expected, desired := parameters.ActionsPermissions.Expected, parameters.ActionsPermissions.Desired
		if expected.Enabled || !desired.Enabled || expected.AllowedActions != "" ||
			desired.AllowedActions != "" || expected.SHAPinningRequired != desired.SHAPinningRequired {
			return errors.New("Actions-permissions discovery is only valid when enabling an unobservable policy")
		}
	default:
		return errors.New("GitHub governance post-state mode is invalid")
	}
	return nil
}

func acceptablePostState(
	action string,
	parameters OperationParameters,
	snapshot githubprovider.GovernanceSnapshot,
	digest string,
) bool {
	if parameters.PostState == "discover-actions-permissions" {
		desired := parameters.ActionsPermissions.Desired
		allowedKnown := snapshot.Actions.AllowedActions == "all" ||
			snapshot.Actions.AllowedActions == "local_only" ||
			snapshot.Actions.AllowedActions == "selected"
		return snapshot.Actions.Enabled == desired.Enabled && allowedKnown
	}
	if !fieldMatchesDesired(action, parameters, snapshot) {
		return false
	}
	if parameters.PostState == "exact" {
		return digest == parameters.DesiredEvidenceDigest
	}
	return parameters.PostState == "discover-selected-actions" && snapshot.SelectedActions != nil
}
