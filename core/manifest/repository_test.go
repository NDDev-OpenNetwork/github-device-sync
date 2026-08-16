package manifest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestLoadRepositoryAllowsSchemaValidatedOptionalSections(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	anchorDirectory := filepath.Join(temporary, ".gds")
	if err := os.MkdirAll(anchorDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(anchorDirectory, "repository.yaml"), raw, 0o644,
	); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	anchor, findings := NewLoader(schemas).LoadRepository(temporary)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if anchor.Repository.ID != "repo_01JEXAMPZ0000000000000000C" {
		t.Fatalf("anchor = %#v", anchor)
	}
}

func TestLoadRepositoryAllowsPathLikeGitSubmoduleName(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "\nrelease:\n", `
relationships:
  - type: "git-submodule-consumer"
    target: "repo_01JEXAMPZ0000000000000000D"
    gitmodules_name: "modules/public-module"

release:
`, 1))
	temporary := t.TempDir()
	if err := os.MkdirAll(filepath.Join(temporary, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(temporary, ".gds", "repository.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	anchor, findings := NewLoader(schemas).LoadRepository(temporary)
	if len(findings) != 0 || len(anchor.Relationships) != 1 ||
		anchor.Relationships[0].GitmodulesName != "modules/public-module" {
		t.Fatalf("anchor=%#v findings=%#v", anchor, findings)
	}
}

func TestLoadRepositoryPreservesModuleForkAndAliasFacts(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-module-fork-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	anchorDirectory := filepath.Join(temporary, ".gds")
	if err := os.MkdirAll(anchorDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchorDirectory, "repository.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	anchor, findings := NewLoader(schemas).LoadRepository(temporary)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if anchor.Module == nil || anchor.Module.PinPolicy != "version-tag" ||
		anchor.Fork == nil || anchor.Fork.Policy != "maintained-patch" ||
		len(anchor.Provider.Aliases) != 1 || len(anchor.Relationships) != 2 {
		t.Fatalf("typed anchor lost validated facts: %#v", anchor)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}
