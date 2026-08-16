package workspace

import (
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func TestResolvePlacementUsesOnePortfolioAssignment(t *testing.T) {
	descriptor := testDevice()
	anchor := testWorkspaceAnchor()
	home := filepath.Join(string(filepath.Separator), "home", "owner")
	placement, findings := ResolvePlacement(descriptor, anchor, Environment{
		Home: home, XDGStateHome: filepath.Join(home, ".local", "state"),
	})
	if len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	if placement.TargetPath != filepath.Join(home, "Developer", "personal", "example") ||
		placement.StateRoot != filepath.Join(home, ".local", "state", "github-device-sync") ||
		placement.Mode != "active" {
		t.Fatalf("placement=%#v", placement)
	}
}

func TestResolvePlacementRejectsAmbiguousAssignments(t *testing.T) {
	descriptor := testDevice()
	descriptor.Materialization.Include = append(descriptor.Materialization.Include, MaterializationAssignment{
		Selector: "portfolio:public-modules", WorkspaceRoot: "personal", Mode: "reference",
	})
	anchor := testWorkspaceAnchor()
	anchor.Classification.Portfolios = append(anchor.Classification.Portfolios, "portfolio:public-modules")
	home := filepath.Join(string(filepath.Separator), "home", "owner")
	_, findings := ResolvePlacement(descriptor, anchor, Environment{
		Home: home, XDGStateHome: filepath.Join(home, ".local", "state"),
	})
	if len(findings) != 1 || findings[0].Code != "GDS_WORKSPACE_PLACEMENT_AMBIGUOUS" {
		t.Fatalf("findings=%#v", findings)
	}
}

func testDevice() DeviceDescriptor {
	return DeviceDescriptor{
		SchemaVersion:  1,
		Device:         DeviceIdentity{ID: "device_01JEXAMPZ00000000000000000", Name: "fixture"},
		WorkspaceRoots: map[string]string{"personal": "${HOME}/Developer/personal"},
		Materialization: MaterializationPolicy{
			DefaultMode: "absent",
			Include: []MaterializationAssignment{{
				Selector: "portfolio:personal-projects", WorkspaceRoot: "personal", Mode: "active",
			}},
		},
		State: DeviceStatePolicy{Path: "${XDG_STATE_HOME}/github-device-sync"},
	}
}

func testWorkspaceAnchor() domain.RepositoryAnchor {
	return domain.RepositoryAnchor{
		Repository: domain.RepositoryIdentity{ID: "repo_01JEXAMPZ0000000000000000C"},
		Provider:   domain.GitHubLocator{Name: "example"},
		Classification: domain.RepositoryClassification{
			Portfolios: []string{"portfolio:personal-projects"},
		},
	}
}
