package governance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type governanceFixture struct {
	snapshot           githubprovider.GovernanceSnapshot
	writes             int
	pinSHAWhenEnabling bool
}

type governanceOperationObserver struct {
	fixture *governanceFixture
}

func (observer governanceOperationObserver) Observe(
	context.Context,
	string,
) (operations.Observation, error) {
	digest, err := EvidenceDigest(observer.fixture.snapshot)
	return operations.Observation{
		RepositoryID: governanceTestScope().RepositoryID,
		HeadOID:      strings.Repeat("a", 40), RemoteEvidenceDigest: digest,
		ManifestDigest: "sha256:" + strings.Repeat("b", 64),
		PolicyDigest:   "sha256:" + strings.Repeat("c", 64),
	}, err
}

func (fixture *governanceFixture) GetRepositoryGovernance(
	context.Context,
	string,
	string,
) (githubprovider.GovernanceSnapshot, error) {
	return fixture.snapshot, nil
}

func (fixture *governanceFixture) Scope() githubprovider.RepositoryMutationScope {
	return githubprovider.RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{githubprovider.MutationRepositorySettings},
	}
}

func (fixture *governanceFixture) UpdateRepositorySettings(
	_ context.Context,
	update githubprovider.RepositorySettingsUpdate,
) (githubprovider.Repository, githubprovider.MutationMeta, error) {
	fixture.writes++
	merge := &fixture.snapshot.Repository.Merge
	if update.AllowMergeCommit != nil {
		merge.AllowMergeCommit = *update.AllowMergeCommit
	}
	if update.AllowSquashMerge != nil {
		merge.AllowSquashMerge = *update.AllowSquashMerge
	}
	if update.AllowRebaseMerge != nil {
		merge.AllowRebaseMerge = *update.AllowRebaseMerge
	}
	if update.AllowAutoMerge != nil {
		merge.AllowAutoMerge = *update.AllowAutoMerge
	}
	if update.AllowUpdateBranch != nil {
		merge.AllowUpdateBranch = *update.AllowUpdateBranch
	}
	if update.DeleteBranchOnMerge != nil {
		merge.DeleteBranchOnMerge = *update.DeleteBranchOnMerge
	}
	if update.MergeCommitTitle != nil {
		merge.MergeCommitTitle = *update.MergeCommitTitle
	}
	if update.MergeCommitMessage != nil {
		merge.MergeCommitMessage = *update.MergeCommitMessage
	}
	if update.SquashMergeCommitTitle != nil {
		merge.SquashMergeTitle = *update.SquashMergeCommitTitle
	}
	if update.SquashMergeCommitMessage != nil {
		merge.SquashMergeMessage = *update.SquashMergeCommitMessage
	}
	return fixture.snapshot.Repository, fixture.meta(), nil
}

func (fixture *governanceFixture) SetActionsPermissions(
	_ context.Context,
	update githubprovider.ActionsPermissionsUpdate,
) (githubprovider.MutationMeta, error) {
	fixture.writes++
	fixture.snapshot.Actions.Enabled = update.Enabled
	if update.Enabled && update.AllowedActions == nil && fixture.snapshot.Actions.AllowedActions == "" {
		fixture.snapshot.Actions.AllowedActions = "all"
	}
	if update.Enabled && fixture.pinSHAWhenEnabling {
		fixture.snapshot.Actions.SHAPinningRequired = true
	}
	if update.AllowedActions != nil {
		fixture.snapshot.Actions.AllowedActions = *update.AllowedActions
	}
	if update.SHAPinningRequired != nil {
		fixture.snapshot.Actions.SHAPinningRequired = *update.SHAPinningRequired
	}
	return fixture.meta(), nil
}

func (fixture *governanceFixture) SetSelectedActionsPermissions(
	_ context.Context,
	update githubprovider.SelectedActionsPermissions,
) (githubprovider.MutationMeta, error) {
	fixture.writes++
	copy := update
	copy.PatternsAllowed = append([]string(nil), update.PatternsAllowed...)
	fixture.snapshot.SelectedActions = &copy
	return fixture.meta(), nil
}

func (fixture *governanceFixture) SetWorkflowPermissions(
	_ context.Context,
	update githubprovider.WorkflowPermissionsUpdate,
) (githubprovider.MutationMeta, error) {
	fixture.writes++
	fixture.snapshot.Workflow = githubprovider.WorkflowPermissions(update)
	return fixture.meta(), nil
}

func (fixture *governanceFixture) SetImmutableReleases(
	_ context.Context,
	enabled bool,
) (githubprovider.MutationMeta, error) {
	fixture.writes++
	fixture.snapshot.ImmutableReleases.Enabled = enabled
	return fixture.meta(), nil
}

func (fixture *governanceFixture) meta() githubprovider.MutationMeta {
	return githubprovider.MutationMeta{RepositoryID: 42, StatusCode: 204, RequestID: "request"}
}

func TestGovernanceHandlersRecheckEveryStepAndVerifyRecordedEvidence(t *testing.T) {
	fixture := &governanceFixture{snapshot: governanceTestSnapshot()}
	steps := []struct {
		action string
		change func(OperationParameters, githubprovider.GovernanceSnapshot) OperationParameters
	}{
		{RepositorySettingsAction, func(parameters OperationParameters, before githubprovider.GovernanceSnapshot) OperationParameters {
			desired := before.Repository.Merge
			desired.AllowMergeCommit = false
			desired.AllowSquashMerge = true
			desired.AllowUpdateBranch = true
			parameters.RepositorySettings = &MergeChange{Expected: before.Repository.Merge, Desired: desired}
			return parameters
		}},
		{ActionsPermissionsAction, func(parameters OperationParameters, before githubprovider.GovernanceSnapshot) OperationParameters {
			desired := githubprovider.ActionsPermissions{Enabled: true, AllowedActions: "selected", SHAPinningRequired: true}
			parameters.ActionsPermissions = &ActionsChange{Expected: before.Actions, Desired: desired}
			return parameters
		}},
		{SelectedActionsPermissionsAction, func(parameters OperationParameters, before githubprovider.GovernanceSnapshot) OperationParameters {
			desired := githubprovider.SelectedActionsPermissions{
				GitHubOwnedAllowed: true, VerifiedAllowed: false,
				PatternsAllowed: []string{"example-org/ci-workflows/.github/workflows/*@*"},
			}
			parameters.SelectedActions = &SelectedActionsChange{Expected: *before.SelectedActions, Desired: desired}
			return parameters
		}},
		{WorkflowPermissionsAction, func(parameters OperationParameters, before githubprovider.GovernanceSnapshot) OperationParameters {
			desired := githubprovider.WorkflowPermissions{Default: "read", CanApprovePullRequestReview: false}
			parameters.WorkflowPermissions = &WorkflowChange{Expected: before.Workflow, Desired: desired}
			return parameters
		}},
		{ImmutableReleasesAction, func(parameters OperationParameters, before githubprovider.GovernanceSnapshot) OperationParameters {
			desired := before.ImmutableReleases
			desired.Enabled = true
			parameters.ImmutableReleases = &ImmutableReleasesChange{Expected: before.ImmutableReleases, Desired: desired}
			return parameters
		}},
	}
	for index, test := range steps {
		before := fixture.snapshot
		parameters := test.change(baseGovernanceParameters(t, before), before)
		desired := before
		applyDesired(test.action, parameters, &desired)
		parameters.DesiredEvidenceDigest = mustGovernanceDigest(t, desired)
		step := operations.Step{Action: test.action, Parameters: Parameters(parameters)}
		handler := &Handler{Reader: fixture, Writer: fixture, Action: test.action}
		evidence, err := handler.Apply(context.Background(), step)
		if err != nil {
			t.Fatalf("step %d apply: %v", index, err)
		}
		raw, err := json.Marshal(evidence.After)
		if err != nil {
			t.Fatal(err)
		}
		if err := handler.Verify(context.Background(), step, raw); err != nil {
			t.Fatalf("step %d verify: %v", index, err)
		}
		writes := fixture.writes
		idempotent, err := handler.Apply(context.Background(), step)
		if err != nil || fixture.writes != writes {
			t.Fatalf("step %d idempotent replay: evidence=%#v err=%v writes=%d", index, idempotent, err, fixture.writes)
		}
	}
	if fixture.writes != 5 {
		t.Fatalf("writes=%d", fixture.writes)
	}
}

func TestGovernanceHandlerBlocksUnrelatedStateDriftBeforeMutation(t *testing.T) {
	fixture := &governanceFixture{snapshot: governanceTestSnapshot()}
	parameters := baseGovernanceParameters(t, fixture.snapshot)
	desiredActions := githubprovider.ActionsPermissions{Enabled: true, AllowedActions: "selected", SHAPinningRequired: true}
	parameters.ActionsPermissions = &ActionsChange{Expected: fixture.snapshot.Actions, Desired: desiredActions}
	desired := fixture.snapshot
	desired.Actions = desiredActions
	parameters.DesiredEvidenceDigest = mustGovernanceDigest(t, desired)
	fixture.snapshot.Workflow.Default = "read"
	handler := &Handler{Reader: fixture, Writer: fixture, Action: ActionsPermissionsAction}
	_, err := handler.Apply(context.Background(), operations.Step{
		Action: ActionsPermissionsAction, Parameters: Parameters(parameters),
	})
	if err == nil || fixture.writes != 0 {
		t.Fatalf("err=%v writes=%d", err, fixture.writes)
	}
}

func TestGovernanceHandlerDiscoversProviderDefaultsWhenEnablingActions(t *testing.T) {
	fixture := &governanceFixture{
		snapshot:           governanceTestSnapshot(),
		pinSHAWhenEnabling: true,
	}
	fixture.snapshot.Actions = githubprovider.ActionsPermissions{}
	parameters := baseGovernanceParameters(t, fixture.snapshot)
	parameters.PostState = "discover-actions-permissions"
	parameters.ActionsPermissions = &ActionsChange{
		Expected: fixture.snapshot.Actions,
		Desired:  githubprovider.ActionsPermissions{Enabled: true},
	}
	step := operations.Step{Action: ActionsPermissionsAction, Parameters: Parameters(parameters)}
	handler := &Handler{Reader: fixture, Writer: fixture, Action: ActionsPermissionsAction}
	evidence, err := handler.Apply(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	if !fixture.snapshot.Actions.Enabled || fixture.snapshot.Actions.AllowedActions != "all" ||
		!fixture.snapshot.Actions.SHAPinningRequired {
		t.Fatalf("snapshot=%#v", fixture.snapshot.Actions)
	}
	raw, err := json.Marshal(evidence.After)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Verify(context.Background(), step, raw); err != nil {
		t.Fatal(err)
	}
}

func TestBuildRemediationCreatesDigestChainedAtomicSteps(t *testing.T) {
	snapshot := governanceTestSnapshot()
	snapshot.Actions = githubprovider.ActionsPermissions{
		Enabled: true, AllowedActions: "selected", SHAPinningRequired: false,
	}
	snapshot.SelectedActions = &githubprovider.SelectedActionsPermissions{
		GitHubOwnedAllowed: false, VerifiedAllowed: true,
		PatternsAllowed: []string{"example/*@*"},
	}
	comparison := Result{Status: "drift", PolicyDigest: "sha256:policy", Fields: []FieldResult{
		{Path: "github.merge.allow_squash_merge", Management: "managed", Status: "drift", Desired: true},
		{Path: "github.actions.sha_pinning_required", Management: "managed", Status: "drift", Desired: true},
		{Path: "github.actions.selected_actions", Management: "managed", Status: "drift", Desired: map[string]any{
			"github_owned_allowed": true, "verified_allowed": false,
			"patterns_allowed": []any{"example-org/ci-workflows/.github/workflows/*@*"},
		}},
		{Path: "github.workflow.default_workflow_permissions", Management: "managed", Status: "drift", Desired: "read"},
		{Path: "github.releases.immutable", Management: "managed", Status: "drift", Desired: true},
	}}
	remediation, err := BuildRemediation(governanceTestScope(), snapshot, comparison)
	if err != nil {
		t.Fatal(err)
	}
	expectedActions := []string{
		RepositorySettingsAction, WorkflowPermissionsAction,
		ActionsPermissionsAction, SelectedActionsPermissionsAction, ImmutableReleasesAction,
	}
	if remediation.RequiresReplan || len(remediation.Steps) != len(expectedActions) ||
		remediation.FinalEvidenceDigest == "" {
		t.Fatalf("remediation=%#v", remediation)
	}
	previous := remediation.InitialEvidenceDigest
	for index, step := range remediation.Steps {
		if step.Action != expectedActions[index] {
			t.Fatalf("step %d action=%s", index, step.Action)
		}
		parameters, err := StepParameters(step)
		if err != nil {
			t.Fatal(err)
		}
		if parameters.PostState != "exact" || parameters.ExpectedEvidenceDigest != previous ||
			parameters.DesiredEvidenceDigest == "" {
			t.Fatalf("step %d parameters=%#v", index, parameters)
		}
		previous = parameters.DesiredEvidenceDigest
	}
	if previous != remediation.FinalEvidenceDigest {
		t.Fatalf("final=%s previous=%s", remediation.FinalEvidenceDigest, previous)
	}
}

func TestBuildRemediationUsesDiscoveryBarrierBeforeSelectedActions(t *testing.T) {
	snapshot := governanceTestSnapshot()
	snapshot.SelectedActions = nil
	comparison := Result{Status: "drift", PolicyDigest: "sha256:policy", Fields: []FieldResult{
		{Path: "github.actions.allowed_actions", Management: "managed", Status: "drift", Desired: "selected"},
		{Path: "github.actions.sha_pinning_required", Management: "managed", Status: "drift", Desired: true},
		{Path: "github.actions.selected_actions", Management: "managed", Status: "drift", Desired: map[string]any{
			"github_owned_allowed": true, "verified_allowed": false,
			"patterns_allowed": []any{"example-org/ci-workflows/.github/workflows/*@*"},
		}},
	}}
	remediation, err := BuildRemediation(governanceTestScope(), snapshot, comparison)
	if err != nil {
		t.Fatal(err)
	}
	if !remediation.RequiresReplan || remediation.FinalEvidenceDigest != "" || len(remediation.Steps) != 1 {
		t.Fatalf("remediation=%#v", remediation)
	}
	parameters, err := StepParameters(remediation.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if remediation.Steps[0].Action != ActionsPermissionsAction ||
		parameters.PostState != "discover-selected-actions" || parameters.DesiredEvidenceDigest != "" {
		t.Fatalf("step=%#v parameters=%#v", remediation.Steps[0], parameters)
	}
}

func TestBuildRemediationUsesDiscoveryBarrierWhenEnablingActions(t *testing.T) {
	snapshot := governanceTestSnapshot()
	snapshot.Actions = githubprovider.ActionsPermissions{}
	comparison := Result{Status: "drift", PolicyDigest: "sha256:policy", Fields: []FieldResult{{
		Path: "github.actions.enabled", Management: "managed", Status: "drift", Desired: true,
	}}}
	remediation, err := BuildRemediation(governanceTestScope(), snapshot, comparison)
	if err != nil {
		t.Fatal(err)
	}
	if !remediation.RequiresReplan || remediation.FinalEvidenceDigest != "" || len(remediation.Steps) != 1 {
		t.Fatalf("remediation=%#v", remediation)
	}
	parameters, err := StepParameters(remediation.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if remediation.Steps[0].Action != ActionsPermissionsAction ||
		parameters.PostState != "discover-actions-permissions" ||
		parameters.ActionsPermissions.Desired.AllowedActions != "" {
		t.Fatalf("step=%#v parameters=%#v", remediation.Steps[0], parameters)
	}
}

func TestBuildRemediationCannotWeakenOwnerImmutableReleaseEnforcement(t *testing.T) {
	snapshot := governanceTestSnapshot()
	snapshot.ImmutableReleases = githubprovider.ImmutableReleases{
		Enabled: true, EnforcedByOwner: true,
	}
	comparison := Result{Status: "drift", PolicyDigest: "sha256:policy", Fields: []FieldResult{{
		Path: "github.releases.immutable", Management: "managed", Status: "drift", Desired: false,
	}}}
	_, err := BuildRemediation(governanceTestScope(), snapshot, comparison)
	if err == nil || !strings.Contains(err.Error(), "enforced by the owner") {
		t.Fatalf("error=%v", err)
	}
}

func TestGovernanceRemediationRunsThroughPlanApplyVerifyEngine(t *testing.T) {
	fixture := &governanceFixture{snapshot: governanceTestSnapshot()}
	fixture.snapshot.Actions = githubprovider.ActionsPermissions{
		Enabled: true, AllowedActions: "selected", SHAPinningRequired: false,
	}
	fixture.snapshot.SelectedActions = &githubprovider.SelectedActionsPermissions{
		PatternsAllowed: []string{"example/*@*"},
	}
	comparison := Result{Status: "drift", PolicyDigest: "sha256:" + strings.Repeat("c", 64), Fields: []FieldResult{
		{Path: "github.merge.allow_squash_merge", Management: "managed", Status: "drift", Desired: true},
		{Path: "github.actions.sha_pinning_required", Management: "managed", Status: "drift", Desired: true},
		{Path: "github.actions.selected_actions", Management: "managed", Status: "drift", Desired: map[string]any{
			"github_owned_allowed": true, "verified_allowed": false,
			"patterns_allowed": []any{"example-org/ci-workflows/.github/workflows/*@*"},
		}},
		{Path: "github.workflow.default_workflow_permissions", Management: "managed", Status: "drift", Desired: "read"},
		{Path: "github.releases.immutable", Management: "managed", Status: "drift", Desired: true},
	}}
	remediation, err := BuildRemediation(governanceTestScope(), fixture.snapshot, comparison)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := operations.NewPlan(planID, now, now.Add(15*time.Minute), operations.PlanInput{
		Operation: "reconcile-github-governance",
		Actor:     operations.Actor{Type: "agent-session", SessionID: "test-session"},
		Preconditions: []operations.Precondition{{
			RepositoryID: governanceTestScope().RepositoryID,
			HeadOID:      strings.Repeat("a", 40), RemoteEvidenceDigest: remediation.InitialEvidenceDigest,
			ManifestDigest: "sha256:" + strings.Repeat("b", 64),
			PolicyDigest:   "sha256:" + strings.Repeat("c", 64),
		}},
		Steps: remediation.Steps, ApprovalClass: "github-governance-write",
	})
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	if findings := plan.Validate(schemas); len(findings) != 0 {
		t.Fatalf("plan findings=%#v", findings)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handlers := map[string]operations.ActionHandler{}
	for _, step := range plan.Steps {
		handlers[step.Action] = &Handler{Reader: fixture, Writer: fixture, Action: step.Action}
	}
	engine := operations.NewDefaultEngine(
		store, schemas, governanceOperationObserver{fixture: fixture}, handlers,
		"device_01JEXAMPZ00000000000000000", "test-session",
	)
	engine.Now = func() time.Time { return now }
	// Keep this test scoped to governance handler ordering. Production engines
	// and dedicated operation tests require signed approval plus enablement.
	engine.RequireSignedApprovals = false
	if err := engine.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	applied, err := engine.Apply(context.Background(), plan.PlanID, "owner:test-approval")
	if err != nil || applied.Status != "succeeded" || fixture.writes != 5 {
		t.Fatalf("applied=%#v err=%v writes=%d", applied, err, fixture.writes)
	}
	verified, err := engine.Verify(context.Background(), applied.OperationID)
	if err != nil || verified.Status != "verified" || verified.Steps != 5 {
		t.Fatalf("verified=%#v err=%v", verified, err)
	}
}

func governanceTestSnapshot() githubprovider.GovernanceSnapshot {
	return githubprovider.GovernanceSnapshot{
		InstallationID: "installation:github-personal",
		Repository: githubprovider.Repository{
			ID: 42, Owner: "example", Name: "repository", FullName: "example/repository",
			Private: true, Visibility: "private", DefaultBranch: "main",
			Merge: githubprovider.MergeSettings{
				AllowMergeCommit: true, AllowSquashMerge: false,
				MergeCommitTitle: "PR_TITLE", MergeCommitMessage: "PR_BODY",
				SquashMergeTitle: "PR_TITLE", SquashMergeMessage: "PR_BODY",
			},
			Security: githubprovider.SecuritySettings{Available: true, Features: map[string]string{"secret_scanning": "enabled"}},
		},
		Actions:         githubprovider.ActionsPermissions{Enabled: true, AllowedActions: "all"},
		SelectedActions: &githubprovider.SelectedActionsPermissions{},
		Workflow:        githubprovider.WorkflowPermissions{Default: "write", CanApprovePullRequestReview: true},
		Rulesets:        []githubprovider.RulesetSummary{},
	}
}

func governanceTestScope() RemediationScope {
	return RemediationScope{
		RepositoryID:         "repo_01JEXAMPZ0000000000000000C",
		ReadInstallationID:   "installation:github-personal",
		MutationCapabilityID: "mutation:github-personal",
		ProviderRepositoryID: 42, Owner: "example", Name: "repository",
	}
}

func baseGovernanceParameters(t *testing.T, snapshot githubprovider.GovernanceSnapshot) OperationParameters {
	t.Helper()
	return OperationParameters{
		ReadInstallationID:   snapshot.InstallationID,
		MutationCapabilityID: "mutation:github-personal",
		ProviderRepositoryID: snapshot.Repository.ID,
		Owner:                snapshot.Repository.Owner, Name: snapshot.Repository.Name,
		PostState:              "exact",
		ExpectedEvidenceDigest: mustGovernanceDigest(t, snapshot),
	}
}

func mustGovernanceDigest(t *testing.T, snapshot githubprovider.GovernanceSnapshot) string {
	t.Helper()
	digest, err := EvidenceDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func applyDesired(action string, parameters OperationParameters, snapshot *githubprovider.GovernanceSnapshot) {
	switch action {
	case RepositorySettingsAction:
		snapshot.Repository.Merge = parameters.RepositorySettings.Desired
	case ActionsPermissionsAction:
		snapshot.Actions = parameters.ActionsPermissions.Desired
	case SelectedActionsPermissionsAction:
		value := parameters.SelectedActions.Desired
		snapshot.SelectedActions = &value
	case WorkflowPermissionsAction:
		snapshot.Workflow = parameters.WorkflowPermissions.Desired
	case ImmutableReleasesAction:
		snapshot.ImmutableReleases = parameters.ImmutableReleases.Desired
	}
}
