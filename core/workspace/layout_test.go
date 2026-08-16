package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func TestAuditLayoutSeparatesStandaloneAndEmbeddedPlacement(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Developer", "personal"), 0o755); err != nil {
		t.Fatal(err)
	}
	descriptor := testDevice()
	parent := testWorkspaceAnchor()
	parent.Repository.ID = "repo_01JEXAMPZ0000000000000000D"
	parent.Repository.Roles = []string{"project", "superproject"}
	parent.Provider.Name = "consumer"
	module := testWorkspaceAnchor()
	module.Repository.ID = "repo_01JEXAMPZ0000000000000000E"
	module.Repository.Roles = []string{"module"}
	module.Provider.Name = "shared-module"
	parent.Relationships = []domain.Relationship{{
		Type: "git-submodule-consumer", Target: module.Repository.ID,
		GitmodulesName: "modules/shared-module",
	}}
	parentPath := filepath.Join(home, "Developer", "personal", "consumer")
	modulePath := filepath.Join(parentPath, "modules", "shared-module")

	report, findings := AuditLayout(descriptor, Environment{
		Home: home, XDGStateHome: filepath.Join(home, ".local", "state"),
	}, []LayoutRepository{
		{Path: parentPath, Anchor: parent},
		{Path: modulePath, SuperprojectRoot: parentPath, Anchor: module},
	})
	if len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	if report.Standalone != 1 || report.Embedded != 1 || report.Compliant != 2 ||
		report.Drifted != 0 || report.Invalid != 0 {
		t.Fatalf("report=%#v", report)
	}
	if report.Entries[1].Kind != LayoutKindEmbedded ||
		report.Entries[1].ExpectedPath != modulePath ||
		report.Entries[1].StandaloneTarget != nil {
		t.Fatalf("embedded=%#v", report.Entries[1])
	}
}

func TestAuditLayoutReportsStandaloneAndEmbeddedDrift(t *testing.T) {
	descriptor := testDevice()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Developer", "personal"), 0o755); err != nil {
		t.Fatal(err)
	}
	parent := testWorkspaceAnchor()
	parent.Repository.ID = "repo_01JEXAMPZ0000000000000000D"
	parent.Repository.Roles = []string{"project", "superproject"}
	parent.Provider.Name = "consumer"
	module := testWorkspaceAnchor()
	module.Repository.ID = "repo_01JEXAMPZ0000000000000000E"
	module.Repository.Roles = []string{"module"}
	parent.Relationships = []domain.Relationship{{
		Type: "git-submodule-consumer", Target: module.Repository.ID,
		GitmodulesName: "modules/shared-module",
	}}
	parentPath := "/wrong/consumer"
	report, findings := AuditLayout(descriptor, Environment{
		Home: home, XDGStateHome: filepath.Join(home, ".local", "state"),
	}, []LayoutRepository{
		{Path: parentPath, Anchor: parent},
		{Path: filepath.Join(parentPath, "wrong-module"), SuperprojectRoot: parentPath, Anchor: module},
	})
	if report.Drifted != 2 || len(findings) != 2 ||
		findings[0].Code != "GDS_WORKSPACE_PLACEMENT_DRIFT" ||
		findings[1].Code != "GDS_WORKSPACE_EMBEDDED_PATH_DRIFT" {
		t.Fatalf("report=%#v findings=%#v", report, findings)
	}
}
