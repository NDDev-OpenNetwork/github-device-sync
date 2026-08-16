// Package module validates module and git-submodule relationships.
package module

import (
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitcontracts"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

type Report struct {
	RepositoryID  string               `json:"repository_id"`
	Topology      gitprovider.Topology `json:"topology"`
	Relationships []RelationshipStatus `json:"relationships"`
}

type RelationshipStatus struct {
	Target               string `json:"target"`
	GitmodulesName       string `json:"gitmodules_name"`
	Path                 string `json:"path,omitempty"`
	GitlinkOID           string `json:"gitlink_oid,omitempty"`
	WorktreeState        string `json:"worktree_state,omitempty"`
	IdentityVerification string `json:"identity_verification"`
}

func Inspect(anchor domain.RepositoryAnchor, topology gitprovider.Topology) (Report, []domain.Finding) {
	report := Report{RepositoryID: anchor.Repository.ID, Topology: topology}
	findings := gitcontracts.ValidateRemote(topology, "origin", gitcontracts.ExpectedRepository{
		Owner: anchor.Provider.Owner, Name: anchor.Provider.Name,
	})
	byName := map[string]gitprovider.Submodule{}
	for _, submodule := range topology.Submodules {
		if submodule.Name != "" {
			byName[submodule.Name] = submodule
		}
		if submodule.URLRedacted {
			findings = append(findings, domain.Finding{
				Code: "GDS_GITLINK_URL_CREDENTIALS_PRESENT", Severity: domain.SeverityCritical,
				Message:  fmt.Sprintf("Submodule %q stores credentials or query material in .gitmodules.", submodule.Name),
				Evidence: map[string]any{"name": submodule.Name, "path": submodule.Path},
			})
		}
		if submodule.Name == "" {
			findings = append(findings, domain.Finding{
				Code: "GDS_GITLINK_CONFIG_MISSING", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Gitlink path %q has no .gitmodules entry.", submodule.Path),
				Evidence: map[string]any{"path": submodule.Path, "gitlink_oid": submodule.GitlinkOID},
			})
		}
		if submodule.Name != "" && submodule.GitlinkOID == "" {
			findings = append(findings, domain.Finding{
				Code: "GDS_GITLINK_MISSING", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Configured submodule %q has no index gitlink.", submodule.Name),
				Evidence: map[string]any{"name": submodule.Name, "path": submodule.Path},
			})
		}
		if submodule.GitlinkStage != 0 {
			findings = append(findings, domain.Finding{
				Code: "GDS_GITLINK_CONFLICT", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Gitlink path %q is at conflict stage %d.", submodule.Path, submodule.GitlinkStage),
				Evidence: map[string]any{"path": submodule.Path, "stage": submodule.GitlinkStage},
			})
		}
		switch submodule.WorktreeState {
		case "off-gitlink":
			findings = append(findings, domain.Finding{
				Code: "GDS_GITLINK_WORKTREE_DRIFT", Severity: domain.SeverityMedium,
				Message: fmt.Sprintf("Submodule worktree %q is not at the indexed gitlink.", submodule.Path),
				Evidence: map[string]any{
					"path": submodule.Path, "gitlink_oid": submodule.GitlinkOID,
					"current_oid": submodule.CurrentOID,
				},
			})
		case "unsafe":
			findings = append(findings, domain.Finding{
				Code: "GDS_GITLINK_WORKTREE_UNSAFE", Severity: domain.SeverityCritical,
				Message:  fmt.Sprintf("Submodule path %q is not a real directory.", submodule.Path),
				Evidence: map[string]any{"path": submodule.Path},
			})
		}
	}

	declared := map[string]struct{}{}
	for _, relationship := range anchor.Relationships {
		if relationship.Type != "git-submodule-consumer" {
			continue
		}
		declared[relationship.GitmodulesName] = struct{}{}
		status := RelationshipStatus{
			Target: relationship.Target, GitmodulesName: relationship.GitmodulesName,
			IdentityVerification: "not-proven-without-estate-index",
		}
		if submodule, found := byName[relationship.GitmodulesName]; found {
			status.Path = submodule.Path
			status.GitlinkOID = submodule.GitlinkOID
			status.WorktreeState = submodule.WorktreeState
		} else {
			findings = append(findings, domain.Finding{
				Code: "GDS_GITLINK_RELATIONSHIP_UNRESOLVED", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf("Relationship %q has no matching .gitmodules entry.", relationship.GitmodulesName),
				Evidence: map[string]any{
					"gitmodules_name": relationship.GitmodulesName, "target": relationship.Target,
				},
			})
		}
		report.Relationships = append(report.Relationships, status)
	}
	for _, submodule := range topology.Submodules {
		if submodule.Name == "" {
			continue
		}
		if _, found := declared[submodule.Name]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_GITLINK_RELATIONSHIP_MISSING", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Submodule %q is not declared by a typed repository relationship.", submodule.Name),
				Evidence: map[string]any{"name": submodule.Name, "path": submodule.Path},
			})
		}
	}
	return report, findings
}
