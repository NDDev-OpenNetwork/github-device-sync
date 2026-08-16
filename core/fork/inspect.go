// Package fork inspects fork topology without fetching or mutating refs.
package fork

import (
	"context"
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitcontracts"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

type Inspector struct {
	Git *gitprovider.Runner
}

type Report struct {
	RepositoryID   string                    `json:"repository_id"`
	Policy         string                    `json:"policy"`
	SyncBranch     string                    `json:"sync_branch"`
	Status         gitprovider.Status        `json:"status"`
	Topology       gitprovider.Topology      `json:"topology"`
	Comparison     gitprovider.RefComparison `json:"comparison"`
	SafeActions    []string                  `json:"safe_actions"`
	BlockedActions []string                  `json:"blocked_actions"`
}

func (inspector Inspector) Inspect(
	ctx context.Context,
	path string,
	anchor domain.RepositoryAnchor,
) (Report, []domain.Finding) {
	report := Report{RepositoryID: anchor.Repository.ID}
	if anchor.Fork == nil {
		return report, []domain.Finding{{
			Code: "GDS_FORK_METADATA_MISSING", Severity: domain.SeverityHigh,
			Message:  "Repository anchor has no fork lifecycle metadata.",
			Evidence: map[string]any{"repository_id": anchor.Repository.ID},
		}}
	}
	report.Policy = anchor.Fork.Policy
	report.SyncBranch = anchor.Fork.SyncBranch
	status, err := inspector.Git.InspectStatus(ctx, path)
	if err != nil {
		return report, []domain.Finding{{
			Code: "GDS_FORK_STATUS_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf("Fork Git status is unavailable: %v", err),
		}}
	}
	report.Status = status
	topology, err := inspector.Git.InspectTopology(ctx, status.Repository.WorktreeRoot)
	if err != nil {
		return report, []domain.Finding{{
			Code: "GDS_FORK_TOPOLOGY_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf("Fork topology is unavailable: %v", err),
		}}
	}
	report.Topology = topology
	findings := gitcontracts.ValidateRemote(topology, "origin", gitcontracts.ExpectedRepository{
		Owner: anchor.Provider.Owner, Name: anchor.Provider.Name,
	})

	report.SafeActions = []string{"inspect"}
	report.BlockedActions = []string{"fetch", "integrate", "force-sync", "push"}
	if anchor.Fork.Policy == "detached" {
		return report, findings
	}
	findings = append(findings, gitcontracts.ValidateRemote(
		topology, "upstream", gitcontracts.ExpectedRepository{
			Owner: anchor.Fork.Upstream.Owner, Name: anchor.Fork.Upstream.Name,
		},
	)...)
	if anchor.Fork.Policy == "frozen" {
		return report, findings
	}
	comparison, err := inspector.Git.CompareCachedRemoteRefs(
		ctx, status.Repository.WorktreeRoot, "upstream", "origin", anchor.Fork.SyncBranch,
	)
	if err != nil {
		findings = append(findings, domain.Finding{
			Code: "GDS_FORK_COMPARISON_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf("Cached fork refs cannot be compared: %v", err),
		})
		return report, findings
	}
	report.Comparison = comparison
	if !comparison.Available {
		findings = append(findings, domain.Finding{
			Code: "GDS_FORK_REFS_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "Both cached origin and upstream tracking refs are required for comparison.",
			Evidence: map[string]any{
				"origin_oid": comparison.RightOID, "upstream_oid": comparison.LeftOID,
			},
		})
	} else {
		findings = append(findings, domain.Finding{
			Code: "GDS_FORK_REMOTE_FRESHNESS_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  "Fork comparison uses cached refs whose provider freshness is unknown.",
			Evidence: map[string]any{"freshness": comparison.Freshness},
		})
	}
	if anchor.Fork.Policy == "upstream-tracking" && comparison.RightOnly > 0 {
		findings = append(findings, domain.Finding{
			Code: "GDS_FORK_UNEXPECTED_COMMITS", Severity: domain.SeverityHigh,
			Message:  "An upstream-tracking fork has fork-only commits that must not be discarded automatically.",
			Evidence: map[string]any{"fork_only_commits": comparison.RightOnly},
		})
	}
	return report, findings
}
