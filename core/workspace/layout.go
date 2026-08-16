package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

const (
	LayoutKindStandalone = "standalone-checkout"
	LayoutKindEmbedded   = "embedded-submodule"

	LayoutStatusPlaced   = "placed"
	LayoutStatusEmbedded = "embedded"
	LayoutStatusDrift    = "drift"
	LayoutStatusInvalid  = "invalid"
)

// LayoutRepository is one observed Git boundary with its repository-owned
// anchor and Git-reported superproject relationship.
type LayoutRepository struct {
	Path             string
	SuperprojectRoot string
	Anchor           domain.RepositoryAnchor
}

// LayoutEntry explains how one observed Git boundary is placed on a device.
// Standalone repositories carry a device placement. Embedded repositories
// inherit their physical locator from an exact typed superproject relation.
type LayoutEntry struct {
	RepositoryID     string     `json:"repository_id"`
	Path             string     `json:"path"`
	Kind             string     `json:"kind"`
	Status           string     `json:"status"`
	SuperprojectRoot string     `json:"superproject_root,omitempty"`
	SuperprojectID   string     `json:"superproject_id,omitempty"`
	GitmodulesName   string     `json:"gitmodules_name,omitempty"`
	ExpectedPath     string     `json:"expected_path"`
	StandaloneTarget *Placement `json:"standalone_placement,omitempty"`
}

type LayoutReport struct {
	DeviceID   string        `json:"device_id"`
	Entries    []LayoutEntry `json:"entries"`
	Standalone int           `json:"standalone"`
	Embedded   int           `json:"embedded"`
	Compliant  int           `json:"compliant"`
	Drifted    int           `json:"drifted"`
	Invalid    int           `json:"invalid"`
}

// AuditLayout compares observed Git boundaries with device placement intent.
// It deliberately does not inspect gitlink OIDs: dependency pin drift belongs
// to the Git-topology validator, while this function proves physical layout.
func AuditLayout(
	descriptor DeviceDescriptor,
	environment Environment,
	repositories []LayoutRepository,
) (LayoutReport, []domain.Finding) {
	report := LayoutReport{DeviceID: descriptor.Device.ID, Entries: []LayoutEntry{}}
	findings := []domain.Finding{}
	byPath := make(map[string]LayoutRepository, len(repositories))
	anchors := make([]domain.RepositoryAnchor, 0, len(repositories))
	for _, repository := range repositories {
		byPath[filepath.Clean(repository.Path)] = repository
		anchors = append(anchors, repository.Anchor)
	}

	for _, repository := range repositories {
		entry := LayoutEntry{
			RepositoryID: repository.Anchor.Repository.ID,
			Path:         filepath.Clean(repository.Path),
		}
		if repository.SuperprojectRoot == "" {
			report.Standalone++
			entry.Kind = LayoutKindStandalone
			// ADR 0027: a consumed submodule has no standalone checkout, so an
			// observed one is a defect regardless of what a selector would place.
			if finding := EmbeddedOnlyFinding(
				entry.RepositoryID, EmbeddedConsumers(anchors, entry.RepositoryID),
			); finding != nil {
				entry.Status = LayoutStatusInvalid
				report.Invalid++
				findings = append(findings, *finding)
				report.Entries = append(report.Entries, entry)
				continue
			}
			placement, placementFindings := ResolvePlacement(
				descriptor, repository.Anchor, environment,
			)
			if len(placementFindings) != 0 {
				entry.Status = LayoutStatusInvalid
				report.Invalid++
				findings = append(findings, placementFindings...)
				report.Entries = append(report.Entries, entry)
				continue
			}
			entry.StandaloneTarget = &placement
			entry.ExpectedPath = placement.TargetPath
			switch {
			case !realDirectory(placement.WorkspaceRoot):
				entry.Status = LayoutStatusInvalid
				report.Invalid++
				findings = append(findings, layoutFinding(
					"GDS_WORKSPACE_ROOT_NOT_READY",
					"Selected workspace root must be an existing non-symlink directory.",
					entry,
				))
			case placement.Mode == "absent":
				entry.Status = LayoutStatusDrift
				report.Drifted++
				findings = append(findings, layoutFinding(
					"GDS_WORKSPACE_UNEXPECTED_CHECKOUT",
					"An observed standalone checkout is selected as absent by the device policy.",
					entry,
				))
			case !sameResolvedPath(entry.Path, placement.TargetPath):
				entry.Status = LayoutStatusDrift
				report.Drifted++
				findings = append(findings, layoutFinding(
					"GDS_WORKSPACE_PLACEMENT_DRIFT",
					"Standalone checkout path differs from the selected device placement.",
					entry,
				))
			default:
				entry.Status = LayoutStatusPlaced
				report.Compliant++
			}
			report.Entries = append(report.Entries, entry)
			continue
		}

		report.Embedded++
		entry.Kind = LayoutKindEmbedded
		entry.SuperprojectRoot = filepath.Clean(repository.SuperprojectRoot)
		parent, found := byPath[entry.SuperprojectRoot]
		if !found {
			entry.Status = LayoutStatusInvalid
			report.Invalid++
			findings = append(findings, layoutFinding(
				"GDS_WORKSPACE_SUPERPROJECT_NOT_DISCOVERED",
				"Embedded repository superproject is outside the audited repository set.",
				entry,
			))
			report.Entries = append(report.Entries, entry)
			continue
		}
		entry.SuperprojectID = parent.Anchor.Repository.ID
		if !contains(parent.Anchor.Repository.Roles, "superproject") ||
			!contains(repository.Anchor.Repository.Roles, "module") {
			entry.Status = LayoutStatusInvalid
			report.Invalid++
			findings = append(findings, layoutFinding(
				"GDS_WORKSPACE_EMBEDDED_ROLE_INVALID",
				"Embedded placement requires a superproject consumer and a module repository.",
				entry,
			))
			report.Entries = append(report.Entries, entry)
			continue
		}
		relations := matchingSubmoduleRelations(parent.Anchor, repository.Anchor.Repository.ID)
		if len(relations) != 1 || relations[0].GitmodulesName == "" {
			entry.Status = LayoutStatusInvalid
			report.Invalid++
			findings = append(findings, layoutFinding(
				"GDS_WORKSPACE_EMBEDDED_RELATION_INVALID",
				"Embedded repository must have exactly one typed superproject git-submodule relationship.",
				entry,
			))
			report.Entries = append(report.Entries, entry)
			continue
		}
		entry.GitmodulesName = relations[0].GitmodulesName
		entry.ExpectedPath = filepath.Clean(filepath.Join(entry.SuperprojectRoot, entry.GitmodulesName))
		if entry.ExpectedPath == entry.SuperprojectRoot ||
			!pathWithin(entry.SuperprojectRoot, entry.ExpectedPath) {
			entry.Status = LayoutStatusInvalid
			report.Invalid++
			findings = append(findings, layoutFinding(
				"GDS_WORKSPACE_EMBEDDED_PATH_UNSAFE",
				"Typed submodule relationship escapes or aliases the superproject root.",
				entry,
			))
		} else if !sameResolvedPath(entry.Path, entry.ExpectedPath) {
			entry.Status = LayoutStatusDrift
			report.Drifted++
			findings = append(findings, layoutFinding(
				"GDS_WORKSPACE_EMBEDDED_PATH_DRIFT",
				"Embedded checkout path differs from its typed superproject relationship.",
				entry,
			))
		} else {
			entry.Status = LayoutStatusEmbedded
			report.Compliant++
		}
		report.Entries = append(report.Entries, entry)
	}

	sort.Slice(report.Entries, func(left, right int) bool {
		return report.Entries[left].Path < report.Entries[right].Path
	})
	return report, findings
}

func matchingSubmoduleRelations(
	anchor domain.RepositoryAnchor,
	target string,
) []domain.Relationship {
	relations := []domain.Relationship{}
	for _, relationship := range anchor.Relationships {
		if relationship.Type == "git-submodule-consumer" && relationship.Target == target {
			relations = append(relations, relationship)
		}
	}
	return relations
}

func pathWithin(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != "." && relative != ".." &&
		!filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func sameResolvedPath(left string, right string) bool {
	resolvedLeft, leftErr := filepath.EvalSymlinks(left)
	resolvedRight, rightErr := filepath.EvalSymlinks(right)
	if leftErr == nil && rightErr == nil {
		return filepath.Clean(resolvedLeft) == filepath.Clean(resolvedRight)
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func realDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func layoutFinding(code string, message string, entry LayoutEntry) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: map[string]any{
			"repository_id": entry.RepositoryID,
			"path":          entry.Path,
			"expected_path": entry.ExpectedPath,
			"kind":          entry.Kind,
		},
	}
}
