package assurance

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
)

type fixtureRepository struct {
	Observed estate.ObservedRepository
	Provider githubprovider.Repository
}

type fixtureReader struct {
	inventory githubprovider.Inventory
	err       error
	calls     atomic.Int32
}

func (reader *fixtureReader) ListInstallationRepositories(
	context.Context,
	int,
) (githubprovider.Inventory, error) {
	reader.calls.Add(1)
	return reader.inventory, reader.err
}

type fixtureAudit struct{}

func (fixtureAudit) Record(
	_ context.Context,
	_ string,
	reconciliationID string,
	result reconciler.Result,
	_ time.Time,
) (string, string, error) {
	digest, err := canonicaljson.Digest(struct {
		ReconciliationID string            `json:"reconciliation_id"`
		Result           reconciler.Result `json:"result"`
	}{ReconciliationID: reconciliationID, Result: result})
	if err != nil {
		return "", "", err
	}
	return "audit_01J00000000000000000000000", digest, nil
}

func buildFixtureRepositories(options Options) []fixtureRepository {
	result := make([]fixtureRepository, 0, options.RepositoryCount)
	// Forks are restricted to owners that carry explicit fork selectors in the
	// canonical estate (personal and organization); the other organization
	// installations have no fork selector, so a fork owned there would compile
	// unassigned.
	forkOwners := []string{"example-user", "example-org"}
	sourceOwners := []string{"example-user", "example-org", "example-media", "NDDev-OpenNetwork"}
	for index := 0; index < options.RepositoryCount; index++ {
		providerID := int64(index + 1)
		fork := index < options.ForkCount
		owner := sourceOwners[index%len(sourceOwners)]
		if fork {
			owner = forkOwners[index%len(forkOwners)]
		}
		name := fmt.Sprintf("assurance-repository-%04d", index+1)
		archived := !fork && index%10 == 9
		visibility := "private"
		if index%5 == 4 {
			visibility = "public"
		}
		// The OpenNetwork owner contract is public-only.
		if owner == "NDDev-OpenNetwork" {
			visibility = "public"
		}
		provider := githubprovider.Repository{
			ID: providerID, NodeID: fmt.Sprintf("node-%d", providerID),
			Owner: owner, Name: name, FullName: owner + "/" + name,
			Private: visibility == "private", Visibility: visibility,
			Fork: fork, Archived: archived, DefaultBranch: "main",
			HTMLURL: "https://github.com/" + owner + "/" + name,
		}
		result = append(result, fixtureRepository{
			Observed: estate.ObservedRepository{
				ProviderID: providerID, Owner: owner, Name: name, Fork: fork,
				Archived: archived, Visibility: visibility, DefaultBranch: "main",
			},
			Provider: provider,
		})
	}
	return result
}

func fixtureInventories(
	repositories []fixtureRepository,
	observedAt time.Time,
) map[string]githubprovider.Inventory {
	permissions := githubprovider.PermissionEvidence{
		Expected:            map[string]string{"metadata": "read"},
		Effective:           map[string]string{"metadata": "read"},
		RepositorySelection: "all", Status: "verified-exact",
	}
	result := map[string]githubprovider.Inventory{
		"installation:github-personal": {
			InstallationID: "installation:github-personal", ObservedAt: observedAt,
			Permissions: permissions, RequestIDs: []string{"request-personal"},
		},
		"installation:github-organization": {
			InstallationID: "installation:github-organization", ObservedAt: observedAt,
			Permissions: permissions, RequestIDs: []string{"request-organization"},
		},
		"installation:github-example-media": {
			InstallationID: "installation:github-example-media", ObservedAt: observedAt,
			Permissions: permissions, RequestIDs: []string{"request-example-media"},
		},
		"installation:github-guild": {
			InstallationID: "installation:github-guild", ObservedAt: observedAt,
			Permissions: permissions, RequestIDs: []string{"request-guild"},
		},
		"installation:github-opennetwork": {
			InstallationID: "installation:github-opennetwork", ObservedAt: observedAt,
			Permissions: permissions, RequestIDs: []string{"request-opennetwork"},
		},
	}
	for _, repository := range repositories {
		installationID := "installation:github-personal"
		if repository.Provider.Owner == "example-org" {
			installationID = "installation:github-organization"
		} else if repository.Provider.Owner == "example-media" {
			installationID = "installation:github-example-media"
		} else if repository.Provider.Owner == "example-guild" {
			installationID = "installation:github-guild"
		} else if repository.Provider.Owner == "NDDev-OpenNetwork" {
			installationID = "installation:github-opennetwork"
		}
		inventory := result[installationID]
		inventory.Repositories = append(inventory.Repositories, repository.Provider)
		result[installationID] = inventory
	}
	for id, inventory := range result {
		inventory.TotalCount = len(inventory.Repositories)
		inventory.Pages = (inventory.TotalCount + 99) / 100
		result[id] = inventory
	}
	return result
}

func syntheticAnchor(
	repository fixtureRepository,
	assignment estate.Assignment,
	index int,
) domain.RepositoryAnchor {
	lifecycles := []string{"active", "maintenance", "frozen", "archived"}
	classification := "private"
	if repository.Observed.Visibility == "public" {
		classification = "public"
	}
	anchor := domain.RepositoryAnchor{
		SchemaVersion: domain.SchemaVersion,
		Repository: domain.RepositoryIdentity{
			ID: repositoryID(index + 1), DisplayName: repository.Observed.Name,
			Roles: []string{"project"}, Lifecycle: lifecycles[index%len(lifecycles)],
		},
		Provider: domain.GitHubLocator{
			Type: "github", Installation: assignment.InstallationID,
			RepositoryID: repository.Observed.ProviderID, Owner: repository.Observed.Owner,
			Name: repository.Observed.Name,
		},
		Classification: domain.RepositoryClassification{
			Portfolios:         append([]string(nil), assignment.Portfolios...),
			VisibilityContract: repository.Observed.Visibility,
			DataClassification: classification,
		},
		Policy: domain.RepositoryPolicy{
			Profiles:    append([]string(nil), assignment.PolicyProfiles...),
			RolloutRing: assignment.RolloutRing,
		},
		Git: domain.GitPolicy{
			DefaultBranch: "main", Integration: "pull-request",
			BranchModel: "task-branches", HandoffPR: "preferred", Cleanup: "merged-only",
		},
		Agent: domain.AgentPolicy{
			ContextProfile: "project-default", GeneratedAgents: true,
			Serena: domain.SerenaPolicy{Enabled: true, ProvenanceRequired: true},
		},
		Release: domain.ReleasePolicy{Mode: "none"},
	}
	if repository.Observed.Fork {
		anchor.Fork = &domain.ForkPolicy{
			Upstream: domain.ForkUpstream{
				Provider: "github", RepositoryID: 100000 + repository.Observed.ProviderID,
				Owner: "upstream-owner", Name: "upstream-" + repository.Observed.Name,
			},
			Policy: "upstream-tracking", SyncBranch: "main",
			PreserveForkCommits: true, AllowForceSync: false,
		}
	}
	return anchor
}

func sharedModuleAnchor(index int) domain.RepositoryAnchor {
	name := fmt.Sprintf("shared-module-%02d", index+1)
	return domain.RepositoryAnchor{
		SchemaVersion: domain.SchemaVersion,
		Repository: domain.RepositoryIdentity{
			ID: repositoryID(DefaultRepositoryCount + index + 1), Roles: []string{"module"},
			Lifecycle: "active",
		},
		Provider: domain.GitHubLocator{
			Type: "github", Installation: "installation:github-organization",
			RepositoryID: int64(50000 + index), Owner: "example-org", Name: name,
		},
	}
}

func moduleTopology(module domain.RepositoryAnchor) gitprovider.Topology {
	name := module.Provider.Name
	return gitprovider.Topology{Submodules: []gitprovider.Submodule{{
		Name: name, Path: "modules/" + name,
		URL:          "https://github.com/" + module.Provider.Owner + "/" + name + ".git",
		GitlinkOID:   "0123456789abcdef0123456789abcdef01234567",
		GitlinkStage: 0, WorktreeState: "at-gitlink",
	}}}
}

func repositoryID(index int) string {
	return fmt.Sprintf("repo_01J%023d", index)
}
