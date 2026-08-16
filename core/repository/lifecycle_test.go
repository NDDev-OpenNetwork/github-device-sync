package repository

import (
	"reflect"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

func TestValidateRenamePreservesStableIdentityAndAliasHistory(t *testing.T) {
	current := lifecycleAnchor()
	candidate := current
	candidate.Provider.Name = "renamed"
	candidate.Provider.Aliases = []domain.GitHubAlias{{Owner: current.Provider.Owner, Name: current.Provider.Name}}
	transition, findings := ValidateTransition(RenameOperation, current, candidate)
	if len(findings) != 0 || transition.RepositoryID != current.Repository.ID || transition.TargetName != "renamed" {
		t.Fatalf("transition=%#v findings=%#v", transition, findings)
	}
	candidate.Repository.ID = "repo_01JEXAMPZ0000000000000000D"
	_, findings = ValidateTransition(RenameOperation, current, candidate)
	assertTransitionFinding(t, findings, "GDS_REPOSITORY_TRANSITION_IDENTITY_CHANGED")
}

func TestProviderTransitionParametersRoundTrip(t *testing.T) {
	current := lifecycleAnchor()
	candidate := current
	candidate.Provider.Name = "renamed"
	candidate.Provider.Aliases = []domain.GitHubAlias{{Owner: current.Provider.Owner, Name: current.Provider.Name}}
	transition, findings := ValidateTransition(RenameOperation, current, candidate)
	if len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	decoded, err := StepTransition(operations.Step{
		RepositoryID: transition.RepositoryID, Action: ProviderLifecycleAction,
		Parameters: Parameters(transition),
	})
	if err != nil || !reflect.DeepEqual(decoded, transition) {
		t.Fatalf("decoded=%#v transition=%#v err=%v", decoded, transition, err)
	}
}

func TestValidateTransferAllowsOnlyOwnerClassificationAndPolicyChange(t *testing.T) {
	current := lifecycleAnchor()
	candidate := current
	candidate.Provider.Owner = "example-org"
	candidate.Provider.Installation = "installation:github-organization"
	candidate.Provider.Aliases = []domain.GitHubAlias{{Owner: current.Provider.Owner, Name: current.Provider.Name}}
	candidate.Classification.Portfolios = []string{"portfolio:organization-projects"}
	candidate.Policy.Profiles = []string{"repository-default", "organization-default"}
	if _, findings := ValidateTransition(TransferOperation, current, candidate); len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	candidate.Git.DefaultBranch = "trunk"
	_, findings := ValidateTransition(TransferOperation, current, candidate)
	assertTransitionFinding(t, findings, "GDS_REPOSITORY_TRANSITION_SCOPE_EXCEEDED")
}

func TestValidateArchiveChangesOnlyLifecycle(t *testing.T) {
	current := lifecycleAnchor()
	candidate := current
	candidate.Repository.Lifecycle = "archived"
	if _, findings := ValidateTransition(ArchiveOperation, current, candidate); len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	candidate.Provider.Name = "renamed"
	_, findings := ValidateTransition(ArchiveOperation, current, candidate)
	assertTransitionFinding(t, findings, "GDS_REPOSITORY_ARCHIVE_DELTA_INVALID")
}

func TestValidateDeleteRequiresArchivedLifecycle(t *testing.T) {
	current := lifecycleAnchor()
	_, findings := ValidateDelete(current)
	assertTransitionFinding(t, findings, "GDS_REPOSITORY_DELETE_ARCHIVE_REQUIRED")
	current.Repository.Lifecycle = "archived"
	transition, findings := ValidateDelete(current)
	if len(findings) != 0 || transition.Operation != DeleteOperation || transition.TargetLifecycle != "tombstoned" {
		t.Fatalf("transition=%#v findings=%#v", transition, findings)
	}
}

func lifecycleAnchor() domain.RepositoryAnchor {
	return domain.RepositoryAnchor{
		SchemaVersion: 1,
		Repository: domain.RepositoryIdentity{
			ID: "repo_01JEXAMPZ0000000000000000C", Roles: []string{"project"}, Lifecycle: "active",
		},
		Provider: domain.GitHubLocator{
			Type: "github", Installation: "installation:github-personal", RepositoryID: 123,
			Owner: "example-user", Name: "example",
		},
		Classification: domain.RepositoryClassification{
			Portfolios: []string{"portfolio:personal-projects"}, VisibilityContract: "private",
			DataClassification: "private-development",
		},
		Policy: domain.RepositoryPolicy{Profiles: []string{"repository-default"}, RolloutRing: "standard"},
		Git: domain.GitPolicy{
			DefaultBranch: "main", Integration: "pull-request", BranchModel: "task-branches",
			HandoffPR: "preferred", Cleanup: "merged-only",
		},
		Agent: domain.AgentPolicy{
			ContextProfile: "project-default", GeneratedAgents: true,
			Serena: domain.SerenaPolicy{Enabled: true, ProvenanceRequired: true},
		},
		Release: domain.ReleasePolicy{Mode: "none"},
	}
}

func assertTransitionFinding(t *testing.T, findings []domain.Finding, code string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("missing %s in %#v", code, findings)
}
