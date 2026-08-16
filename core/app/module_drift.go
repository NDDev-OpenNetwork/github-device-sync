package app

import (
	"context"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
)

// ModuleDriftEdge describes one submodule whose recorded superproject gitlink
// differs from the materialized worktree checkout. It is the read-only
// observable the operator uses to decide whether to advance the gitlink through
// the gated gds module update-pin flow.
type ModuleDriftEdge struct {
	// GitmodulesName is the .gitmodules name of the drifted submodule.
	GitmodulesName string `json:"gitmodules_name"`
	// Path is the worktree-relative submodule path.
	Path string `json:"path"`
	// RecordedOID is the gitlink the superproject records in its index/tree.
	RecordedOID string `json:"recorded_oid"`
	// ActualOID is the commit the submodule worktree is currently checked out at.
	ActualOID string `json:"actual_oid"`
	// WorktreeState is the submodule worktree state ("uninitialized", "clean",
	// "dirty", ...), as reported by Git topology inspection.
	WorktreeState string `json:"worktree_state"`
}

// ModuleDriftReportData is the structured read-only output of a drift scan.
type ModuleDriftReportData struct {
	// RepositoryID is the canonical superproject repository identity.
	RepositoryID string `json:"repository_id"`
	// SubmoduleCount is the total number of typed submodules observed.
	SubmoduleCount int `json:"submodule_count"`
	// Drifted is the subset of submodules whose recorded gitlink diverges from
	// the checked-out commit.
	Drifted []ModuleDriftEdge `json:"drifted"`
}

// ModuleDriftReport scans the repository at path for submodule gitlink drift.
// It is strictly read-only: it never stages, commits, pushes, or advances a
// gitlink. Divergent edges are reported so the operator can drive the gated
// gds module update-pin flow deliberately. A successful scan with zero drift
// still returns exit success so the command composes with automation.
func (services *Services) ModuleDriftReport(ctx context.Context, path string) domain.Envelope {
	const command = "gds module drift-report"
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError(command, path, err)
	}
	anchor, findings := manifest.NewLoader(services.Schemas).LoadRepository(info.WorktreeRoot)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	topology, err := services.Git.InspectTopology(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError(command, path, err)
	}
	data := ModuleDriftReportData{RepositoryID: anchor.Repository.ID, SubmoduleCount: len(topology.Submodules)}
	for _, submodule := range topology.Submodules {
		if submodule.GitlinkOID == "" || submodule.GitlinkOID == submodule.CurrentOID {
			continue
		}
		data.Drifted = append(data.Drifted, ModuleDriftEdge{
			GitmodulesName: submodule.Name, Path: submodule.Path,
			RecordedOID: submodule.GitlinkOID, ActualOID: submodule.CurrentOID,
			WorktreeState: submodule.WorktreeState,
		})
	}
	classification := classifyDriftReport(len(data.Drifted))
	envelope := domain.NewEnvelope(command, classification, data)
	envelope.Scope["repository_id"] = anchor.Repository.ID
	envelope.Scope["drifted_submodules"] = len(data.Drifted)
	return envelope
}

// classifyDriftReport keeps a zero-drift scan as a clean success and marks any
// divergence as stale so it surfaces in reports without blocking automation.
func classifyDriftReport(drifted int) domain.ExitClass {
	if drifted == 0 {
		return domain.ExitSuccess
	}
	return domain.ExitStale
}
