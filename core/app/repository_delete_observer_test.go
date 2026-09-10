package app

import (
	"testing"

	repositoryworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/repository"
)

// The delete precondition observer is built twice — once when the plan is
// stored and once when it is applied — and the two sites drifted apart, which
// made every approved deletion fail as GDS_STALE_PLAN. Planning and apply must
// derive the same observer from the same stored transition.
func TestDeleteObserverCarriesTheApprovedRetirementDeclaration(t *testing.T) {
	services := &Services{}
	transition := repositoryworkflow.ProviderTransition{
		Operation: repositoryworkflow.DeleteOperation, RepositoryID: "repo_01ABCDEFGHJKMNPQRSTVWXYZ01",
		ProviderRepositoryID: 42, CurrentInstallation: "installation:github-personal",
		CurrentOwner: "example-owner", CurrentName: "example", CurrentLifecycle: "archived",
		TargetInstallation: "installation:github-personal",
		TargetOwner:        "example-owner", TargetName: "example", TargetLifecycle: "tombstoned",
		AnalysisRoot:        "/verified-estate",
		PreservedIdentities: []string{"commits:unpushed", "ref:refs/tags/v0.6.10"},
	}
	options := RepositoryDeleteOptions{MaxDepth: 8, MaxRepositories: 2000, Concurrency: 4}

	observer := services.newRepositoryDeleteObserver("/verified-estate/example", transition, options, nil)

	if len(observer.preserve) != len(transition.PreservedIdentities) {
		t.Fatalf("preserve=%#v transition=%#v", observer.preserve, transition.PreservedIdentities)
	}
	for index, identity := range transition.PreservedIdentities {
		if observer.preserve[index] != identity {
			t.Fatalf("preserve[%d]=%q expected %q", index, observer.preserve[index], identity)
		}
	}
	if observer.inventoryRoot != transition.AnalysisRoot {
		t.Fatalf("inventoryRoot=%q expected %q", observer.inventoryRoot, transition.AnalysisRoot)
	}
}
