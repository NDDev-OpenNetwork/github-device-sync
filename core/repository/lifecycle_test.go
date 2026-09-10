package repository

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
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

func TestDeletePlanCarriesTheAcceptedLossesThroughItsParameters(t *testing.T) {
	current := lifecycleAnchor()
	current.Repository.Lifecycle = "archived"
	transition, findings := ValidateDelete(current)
	if len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	// What the operator accepted losing is part of what the approver signs, so
	// it has to survive into the stored plan: the apply path rebuilds the
	// retirement evidence from it, and an empty set makes every accepted loss
	// read as blocking again.
	transition.AnalysisRoot = "/verified-estate"
	transition.PreservedIdentities = []string{"commits:unpushed", "ref:refs/tags/v0.6.10"}
	decoded, err := StepTransition(operations.Step{
		RepositoryID: transition.RepositoryID, Action: ProviderLifecycleAction,
		Parameters: Parameters(transition),
	})
	if err != nil || !reflect.DeepEqual(decoded, transition) {
		t.Fatalf("decoded=%#v transition=%#v err=%v", decoded, transition, err)
	}
}

func TestDeleteParametersRejectANonStringAcceptedLoss(t *testing.T) {
	current := lifecycleAnchor()
	current.Repository.Lifecycle = "archived"
	transition, findings := ValidateDelete(current)
	if len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	parameters := Parameters(transition)
	provider, ok := parameters["repository_provider"].(map[string]any)
	if !ok {
		t.Fatalf("parameters=%#v", parameters)
	}
	provider["preserved_identities"] = []any{"commits:unpushed", 7}
	if _, err := StepTransition(operations.Step{
		RepositoryID: transition.RepositoryID, Action: ProviderLifecycleAction,
		Parameters: parameters,
	}); err == nil {
		t.Fatal("a non-string accepted loss must not decode into a preservation declaration")
	}
}

// The plan schema refuses unknown keys under `repository_provider`, so a new
// transition field is only real once the schema admits it. A delete plan
// carrying accepted losses is exactly the shape that shipped broken: the Go
// round trip passed while `gds repository delete --plan` failed with
// GDS_PLAN_INVALID, because nothing validated the parameters against the
// schema that governs them.
func TestDeleteParametersValidateAgainstThePlanSchema(t *testing.T) {
	current := lifecycleAnchor()
	current.Repository.Lifecycle = "archived"
	transition, findings := ValidateDelete(current)
	if len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	transition.MutationCapabilityID = "mutation:github-personal"
	transition.ExpectedProviderDigest = "sha256:" + strings.Repeat("a", 64)
	transition.AnalysisRoot = "/verified-estate"
	transition.PreservedIdentities = []string{
		"commits:unpushed", "ref:refs/tags/v0.6.10", "review-threads",
		"worktree:/verified-estate/example", "branch:task/one",
		"pull-request:7", "issue:11",
	}
	plan, err := operations.NewPlan(
		"plan_01ABCDEFGHJKMNPQRSTVWXYZ01", time.Unix(1_800_000_000, 0).UTC(),
		time.Unix(1_800_000_900, 0).UTC(),
		operations.PlanInput{
			Operation: "delete-repository",
			Actor:     operations.Actor{Type: "agent-session", SessionID: "schema-test-session"},
			Preconditions: []operations.Precondition{{
				RepositoryID:   transition.RepositoryID,
				HeadOID:        strings.Repeat("b", 40),
				ManifestDigest: "sha256:" + strings.Repeat("c", 64),
				PolicyDigest:   "sha256:" + strings.Repeat("d", 64),
			}},
			Steps: []operations.Step{{
				StepID: "delete-provider-repository", RepositoryID: transition.RepositoryID,
				Action: ProviderLifecycleAction, RequiresApproval: true,
				Compensation: operations.Compensation{Mode: "manual"},
				Parameters:   Parameters(transition),
			}},
			ApprovalClass: "delete-github-repository",
		},
	)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatalf("NewSchemaSet: %v", err)
	}
	if planFindings := plan.Validate(schemas); len(planFindings) != 0 {
		t.Fatalf("plan findings = %#v", planFindings)
	}
}
