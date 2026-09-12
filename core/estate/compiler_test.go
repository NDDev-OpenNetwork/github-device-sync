package estate

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestLoadCanonicalControlledMutationEstate(t *testing.T) {
	t.Parallel()
	config := loadCanonical(t)
	if config.Root.Rollout.MutationMode != "pull-request" ||
		config.Root.Discovery.DefaultManagementMode != "observe-only" ||
		len(config.Installations) != 5 || len(config.Mutations) != 4 ||
		len(config.Owners) != 5 || len(config.Selectors) != 7 {
		t.Fatalf("config = %#v", config)
	}
}

func TestCompileTwoThousandRepositoriesAndForksDeterministically(t *testing.T) {
	t.Parallel()
	config := loadCanonical(t)
	repositories := make([]ObservedRepository, 0, 2000)
	for index := 1; index <= 2000; index++ {
		owner := "example-user"
		if index%2 == 0 {
			owner = "example-org"
		}
		repositories = append(repositories, ObservedRepository{
			ProviderID: int64(3000 - index), Owner: owner,
			Name: "repository", Fork: index <= 1000, Visibility: "private",
			DefaultBranch: "main",
		})
	}
	compiled, findings := Compile(config, repositories)
	if len(findings) != 0 || len(compiled.Repositories) != 2000 {
		t.Fatalf("repositories=%d findings=%#v", len(compiled.Repositories), findings)
	}
	managed := 0
	for index, assignment := range compiled.Repositories {
		if assignment.ProviderID != int64(index+1000) ||
			assignment.MatchedSelector == "" || assignment.IdentityState != "not-observed" {
			t.Fatalf("assignment[%d] = %#v", index, assignment)
		}
		if assignment.MatchedSelector == "organization-sources" {
			if assignment.ManagementMode != "managed" {
				t.Fatalf("managed assignment[%d] = %#v", index, assignment)
			}
			managed++
		} else if assignment.ManagementMode != "observe-only" {
			t.Fatalf("observe-only assignment[%d] = %#v", index, assignment)
		}
		// Half the observations are forks. None of them is classified as
		// one: a repository belongs to the account that holds it, so a fork
		// lands in exactly the selector its non-fork sibling would.
		if assignment.Owner == "example-user" && assignment.MatchedSelector != "personal-sources" {
			t.Fatalf("personal assignment[%d] = %#v", index, assignment)
		}
		if assignment.Owner == "example-org" && assignment.MatchedSelector != "organization-sources" {
			t.Fatalf("organization assignment[%d] = %#v", index, assignment)
		}
	}
	// Every organization repository is managed now, forks included. That is
	// the consequence of dropping fork classification: a managed account
	// manages everything it holds, and a fork stops being a way to sit
	// outside that. Half of these observations are forks.
	if managed != 1000 {
		t.Fatalf("managed assignments = %d, want 1000", managed)
	}

	for left, right := 0, len(repositories)-1; left < right; left, right = left+1, right-1 {
		repositories[left], repositories[right] = repositories[right], repositories[left]
	}
	second, secondFindings := Compile(config, repositories)
	firstJSON, _ := json.Marshal(compiled)
	secondJSON, _ := json.Marshal(second)
	if len(secondFindings) != 0 || string(firstJSON) != string(secondJSON) {
		t.Fatal("compiled inventory is not deterministic")
	}
}

func TestCompileRejectsSelectorConflictAndUnknownOwner(t *testing.T) {
	t.Parallel()
	config := loadCanonical(t)
	conflict := organizationSourcesSelector(t, config)
	conflict.Selector.ID = "conflicting-selector"
	config.Selectors = append(config.Selectors, conflict)
	_, findings := Compile(config, []ObservedRepository{{
		ProviderID: 1, Owner: "example-org", Name: "repository",
		Fork: true, Visibility: "private", DefaultBranch: "main",
	}})
	if !estateHasFinding(findings, "GDS_ESTATE_SELECTOR_CONFLICT") {
		t.Fatalf("selector conflict findings = %#v", findings)
	}
	_, findings = Compile(loadCanonical(t), []ObservedRepository{{
		ProviderID: 2, Owner: "unknown-owner", Name: "repository",
		Visibility: "private", DefaultBranch: "main",
	}})
	if !estateHasFinding(findings, "GDS_ESTATE_OWNER_NOT_PROVEN") {
		t.Fatalf("unknown owner findings = %#v", findings)
	}
}

func TestCompileRejectsMoreThanTwoThousandRepositories(t *testing.T) {
	t.Parallel()
	_, findings := Compile(loadCanonical(t), make([]ObservedRepository, 2001))
	if !estateHasFinding(findings, "GDS_ESTATE_REPOSITORY_LIMIT_EXCEEDED") {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestCompileRoutesServerRepositoriesByNamePrefix(t *testing.T) {
	t.Parallel()
	compiled, findings := Compile(loadCanonical(t), []ObservedRepository{
		{
			ProviderID: 10, Owner: "example-org", Name: "server-example-alliance",
			Fork: false, Visibility: "private", DefaultBranch: "main",
		},
		{
			ProviderID: 11, Owner: "example-org", Name: "nddev-web",
			Fork: false, Visibility: "private", DefaultBranch: "main",
		},
		{
			ProviderID: 12, Owner: "example-user", Name: "server-home-lab",
			Fork: false, Visibility: "private", DefaultBranch: "main",
		},
		{
			ProviderID: 13, Owner: "example-org", Name: "server-upstream-fork",
			Fork: true, Visibility: "private", DefaultBranch: "main",
		},
	})
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	byID := map[int64]Assignment{}
	for _, assignment := range compiled.Repositories {
		byID[assignment.ProviderID] = assignment
	}
	if got := byID[10]; got.MatchedSelector != "organization-servers" ||
		len(got.Portfolios) != 1 || got.Portfolios[0] != "portfolio:servers" {
		t.Fatalf("organization server repository = %#v", got)
	}
	if got := byID[11]; got.MatchedSelector != "organization-sources" ||
		!containsString(got.Portfolios, "portfolio:organization-projects") {
		t.Fatalf("organization non-server repository = %#v", got)
	}
	if got := byID[12]; got.MatchedSelector != "personal-servers" ||
		len(got.Portfolios) != 1 || got.Portfolios[0] != "portfolio:servers" {
		t.Fatalf("personal server repository = %#v", got)
	}
	// The name prefix decides, and being a fork no longer overrides it.
	if got := byID[13]; got.MatchedSelector != "organization-servers" ||
		!containsString(got.Portfolios, "portfolio:servers") {
		t.Fatalf("server-named organization fork repository = %#v", got)
	}
}

func loadCanonical(t *testing.T) Config {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	config, findings := Load(estateRepositoryRoot(t), schemas)
	if len(findings) != 0 {
		t.Fatalf("load findings = %#v", findings)
	}
	return config
}

func estateRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func estateHasFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestCompilePreservesArchivedObservation(t *testing.T) {
	t.Parallel()
	config := loadCanonical(t)
	compiled, findings := Compile(config, []ObservedRepository{{
		ProviderID: 42, Owner: "example-user", Name: "retired",
		Archived: true, Visibility: "private", DefaultBranch: "main",
	}})
	if len(findings) != 0 || len(compiled.Repositories) != 1 || !compiled.Repositories[0].Archived {
		t.Fatalf("compiled=%#v findings=%#v", compiled, findings)
	}
}

func organizationSourcesSelector(t *testing.T, config Config) Selector {
	t.Helper()
	for _, selector := range config.Selectors {
		if selector.Selector.ID == "organization-sources" {
			return selector
		}
	}
	t.Fatalf("organization-sources selector not found in %#v", config.Selectors)
	return Selector{}
}
