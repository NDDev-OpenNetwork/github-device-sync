package githubcoverage

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
)

func TestEvaluateMigratesByImmutableProviderIDAcrossRename(t *testing.T) {
	t.Parallel()
	report := Evaluate(coverageConfig(), reconciler.Result{
		Installations: []reconciler.InstallationResult{{
			InstallationID: "installation:github-personal", RepositoryCount: 1, Status: "observed",
		}},
		Inventory: estate.CompiledInventory{Repositories: []estate.Assignment{{
			ProviderID: 42, Owner: "example-user", Name: "renamed",
			InstallationID: "installation:github-personal",
		}}},
	}, []estate.IdentityRepository{{
		ID: "repo_01TEST", ProviderID: 42, Owner: "example-user", Name: "original",
	}}, true)
	if report.Counts[StatusMigrated] != 1 || len(report.Repositories) != 1 {
		t.Fatalf("report=%#v", report)
	}
	if report.Repositories[0].Status != StatusMigrated ||
		!containsReason(report.Repositories[0], "locator_changed") {
		t.Fatalf("repository=%#v", report.Repositories[0])
	}
}

func TestEvaluateMarksArchivedNotApplicable(t *testing.T) {
	t.Parallel()
	report := Evaluate(coverageConfig(), reconciler.Result{
		Installations: []reconciler.InstallationResult{{
			InstallationID: "installation:github-personal", RepositoryCount: 1, Status: "observed",
		}},
		Inventory: estate.CompiledInventory{Repositories: []estate.Assignment{{
			ProviderID: 7, Owner: "example-user", Name: "old", Archived: true,
			InstallationID: "installation:github-personal",
		}}},
	}, []estate.IdentityRepository{{
		ID: "repo_archived", ProviderID: 7, Owner: "example-user", Name: "old", Lifecycle: "archived",
	}}, true)
	if report.Counts[StatusNotApplicable] != 1 || report.Counts[StatusMigrated] != 0 {
		t.Fatalf("report=%#v", report)
	}
}

func TestEvaluateClassifiesMissingInstallationAsUnknown(t *testing.T) {
	t.Parallel()
	report := Evaluate(coverageConfig(), reconciler.Result{
		Installations: []reconciler.InstallationResult{{
			InstallationID: "installation:github-personal", Status: "not-proven",
		}},
		Findings: []domain.Finding{{
			Code:     "GDS_RECONCILE_INSTALLATION_NOT_PROVEN",
			Evidence: map[string]any{"installation": "installation:github-personal"},
		}},
	}, []estate.IdentityRepository{{
		ID: "repo_local", ProviderID: 99, Owner: "example-user", Name: "private",
	}}, true)
	if report.Counts[StatusUnknown] != 1 || report.Installations[0].Status != StatusUnknown {
		t.Fatalf("report=%#v", report)
	}
}

func TestEvaluateClassifiesPermissionMismatchAsDenied(t *testing.T) {
	t.Parallel()
	report := Evaluate(coverageConfig(), reconciler.Result{
		Installations: []reconciler.InstallationResult{{
			InstallationID: "installation:github-personal", Status: "not-proven",
		}},
		Findings: []domain.Finding{{
			Code:     "GDS_RECONCILE_PERMISSION_CONTRACT_MISMATCH",
			Evidence: map[string]any{"installation": "installation:github-personal"},
		}},
	}, []estate.IdentityRepository{{
		ID: "repo_local", ProviderID: 99, Owner: "example-user", Name: "private",
	}}, true)
	if report.Counts[StatusDenied] != 1 {
		t.Fatalf("report=%#v", report)
	}
}

func TestEvaluateAppOnlyWithoutLocalIdentitiesIsPartial(t *testing.T) {
	t.Parallel()
	report := Evaluate(coverageConfig(), reconciler.Result{
		Installations: []reconciler.InstallationResult{{
			InstallationID: "installation:github-personal", RepositoryCount: 1, Status: "observed",
		}},
		Inventory: estate.CompiledInventory{Repositories: []estate.Assignment{{
			ProviderID: 5, Owner: "example-user", Name: "example",
			InstallationID: "installation:github-personal",
		}}},
	}, nil, false)
	if report.Counts[StatusPartial] != 1 ||
		!containsReason(report.Repositories[0], "local_identity_not_collected") {
		t.Fatalf("report=%#v", report)
	}
}

func TestEvaluateLocalOnlyWhenAppObservedIsPartial(t *testing.T) {
	t.Parallel()
	report := Evaluate(coverageConfig(), reconciler.Result{
		Installations: []reconciler.InstallationResult{{
			InstallationID: "installation:github-personal", RepositoryCount: 0, Status: "observed",
		}},
	}, []estate.IdentityRepository{{
		ID: "repo_local", ProviderID: 11, Owner: "example-user", Name: "missing-from-app",
	}}, true)
	if report.Counts[StatusPartial] != 1 ||
		!containsReason(report.Repositories[0], "app_inventory_missing") {
		t.Fatalf("report=%#v", report)
	}
}

func coverageConfig() estate.Config {
	return estate.Config{
		Root: estate.Root{Installations: []string{"installation:github-personal"}},
		Owners: []estate.Owner{{
			Owner: estate.OwnerIdentity{
				Installation: "installation:github-personal", ProviderLogin: "example-user",
			},
		}},
	}
}

func containsReason(coverage RepositoryCoverage, reason string) bool {
	for _, value := range coverage.Reasons {
		if value == reason {
			return true
		}
	}
	return false
}
