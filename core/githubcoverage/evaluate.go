// Package githubcoverage classifies GitHub App inventories against local GDS
// identities by immutable provider repository ID. It does not invent a second
// inspector: gds github inventory remains one installation, gds reconcile
// remains the App union, and this evaluator is the ID-stable coverage view.
package githubcoverage

import (
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
)

const (
	StatusMigrated      = "migrated"
	StatusPartial       = "partial"
	StatusDenied        = "denied"
	StatusUnknown       = "unknown"
	StatusNotApplicable = "not-applicable"
)

type RepositoryCoverage struct {
	ProviderID      int64    `json:"provider_id"`
	Owner           string   `json:"owner"`
	Name            string   `json:"name"`
	InstallationID  string   `json:"installation_id,omitempty"`
	GDSRepositoryID string   `json:"gds_repository_id,omitempty"`
	Status          string   `json:"status"`
	Reasons         []string `json:"reasons,omitempty"`
}

type InstallationCoverage struct {
	InstallationID  string `json:"installation_id"`
	Status          string `json:"status"`
	RepositoryCount int    `json:"repository_count"`
}

type Report struct {
	LocalIdentitiesCollected bool                   `json:"local_identities_collected"`
	Installations            []InstallationCoverage `json:"installations"`
	Repositories             []RepositoryCoverage   `json:"repositories"`
	Counts                   map[string]int         `json:"counts"`
}

func Evaluate(
	config estate.Config,
	result reconciler.Result,
	identities []estate.IdentityRepository,
	localCollected bool,
) Report {
	report := Report{
		LocalIdentitiesCollected: localCollected,
		Counts:                   map[string]int{},
	}
	installationStatus := map[string]string{}
	for _, installation := range result.Installations {
		status := classifyInstallation(result, installation)
		installationStatus[installation.InstallationID] = status
		report.Installations = append(report.Installations, InstallationCoverage{
			InstallationID:  installation.InstallationID,
			Status:          status,
			RepositoryCount: installation.RepositoryCount,
		})
	}
	for _, installationID := range config.Root.Installations {
		if _, seen := installationStatus[installationID]; seen {
			continue
		}
		installationStatus[installationID] = StatusUnknown
		report.Installations = append(report.Installations, InstallationCoverage{
			InstallationID: installationID, Status: StatusUnknown,
		})
	}
	ownerInstallation := map[string]string{}
	for _, owner := range config.Owners {
		ownerInstallation[strings.ToLower(owner.Owner.ProviderLogin)] = owner.Owner.Installation
	}

	observed := map[int64]estate.Assignment{}
	for _, assignment := range result.Inventory.Repositories {
		observed[assignment.ProviderID] = assignment
	}
	local := map[int64]estate.IdentityRepository{}
	for _, identity := range identities {
		if identity.ProviderID <= 0 {
			continue
		}
		local[identity.ProviderID] = identity
	}

	seen := map[int64]struct{}{}
	for id, assignment := range observed {
		seen[id] = struct{}{}
		identity, hasLocal := local[id]
		report.Repositories = append(report.Repositories, classifyObserved(
			assignment, identity, hasLocal, localCollected, installationStatus,
		))
	}
	for id, identity := range local {
		if _, already := seen[id]; already {
			continue
		}
		installationID := ownerInstallation[strings.ToLower(identity.Owner)]
		report.Repositories = append(report.Repositories, classifyLocalOnly(
			identity, installationID, installationStatus[installationID],
		))
	}

	sort.Slice(report.Installations, func(left, right int) bool {
		return report.Installations[left].InstallationID < report.Installations[right].InstallationID
	})
	sort.Slice(report.Repositories, func(left, right int) bool {
		return report.Repositories[left].ProviderID < report.Repositories[right].ProviderID
	})
	for _, repository := range report.Repositories {
		report.Counts[repository.Status]++
	}
	return report
}

func classifyInstallation(result reconciler.Result, installation reconciler.InstallationResult) string {
	for _, finding := range result.Findings {
		if finding.Evidence["installation"] != installation.InstallationID {
			continue
		}
		switch finding.Code {
		case "GDS_RECONCILE_PERMISSION_CONTRACT_MISMATCH":
			return StatusDenied
		case "GDS_RECONCILE_INSTALLATION_NOT_PROVEN":
			return StatusUnknown
		}
	}
	switch installation.Status {
	case "observed", "observed-unpersisted":
		return "observed"
	case "identity-mismatch":
		return StatusUnknown
	case "not-proven":
		return StatusUnknown
	default:
		if installation.Status == "" {
			return StatusUnknown
		}
		return installation.Status
	}
}

func classifyObserved(
	assignment estate.Assignment,
	identity estate.IdentityRepository,
	hasLocal bool,
	localCollected bool,
	installationStatus map[string]string,
) RepositoryCoverage {
	coverage := RepositoryCoverage{
		ProviderID:     assignment.ProviderID,
		Owner:          assignment.Owner,
		Name:           assignment.Name,
		InstallationID: assignment.InstallationID,
	}
	if hasLocal {
		coverage.GDSRepositoryID = identity.ID
		if !strings.EqualFold(identity.Owner, assignment.Owner) ||
			!strings.EqualFold(identity.Name, assignment.Name) {
			coverage.Reasons = append(coverage.Reasons, "locator_changed")
		}
	}
	if assignment.Archived || strings.EqualFold(identity.Lifecycle, "archived") {
		coverage.Status = StatusNotApplicable
		coverage.Reasons = append(coverage.Reasons, "archived")
		return coverage
	}
	if hasLocal {
		coverage.Status = StatusMigrated
		return coverage
	}
	coverage.Status = StatusPartial
	if !localCollected {
		coverage.Reasons = append(coverage.Reasons, "local_identity_not_collected")
	} else {
		coverage.Reasons = append(coverage.Reasons, "gds_identity_missing")
	}
	if status := installationStatus[assignment.InstallationID]; status == StatusDenied {
		coverage.Status = StatusDenied
	}
	return coverage
}

func classifyLocalOnly(
	identity estate.IdentityRepository,
	installationID string,
	installationStatus string,
) RepositoryCoverage {
	coverage := RepositoryCoverage{
		ProviderID:      identity.ProviderID,
		Owner:           identity.Owner,
		Name:            identity.Name,
		InstallationID:  installationID,
		GDSRepositoryID: identity.ID,
	}
	if strings.EqualFold(identity.Lifecycle, "archived") {
		coverage.Status = StatusNotApplicable
		coverage.Reasons = []string{"archived"}
		return coverage
	}
	switch installationStatus {
	case StatusDenied:
		coverage.Status = StatusDenied
		coverage.Reasons = []string{"installation_denied"}
	case "", StatusUnknown:
		coverage.Status = StatusUnknown
		coverage.Reasons = []string{"installation_not_proven"}
	default:
		coverage.Status = StatusPartial
		coverage.Reasons = []string{"app_inventory_missing"}
	}
	return coverage
}
