package inventory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/discovery"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestCompileLocalInventory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repository := filepath.Join(root, "project")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", "-q", repository)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}

	runner, err := gitprovider.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	localDiscovery := discovery.NewLocal(runner, manifest.NewLoader(schemas))
	fixed := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	compiler := NewCompiler(localDiscovery, runner, func() time.Time { return fixed })
	result, err := compiler.Compile(context.Background(), root, discovery.Options{
		MaxDepth: 4, MaxRepositories: 10, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if result.ObservedAt != "2026-07-11T05:00:00Z" {
		t.Fatalf("observed_at = %q", result.ObservedAt)
	}
	if result.Summary.Total != 1 || result.Summary.Unanchored != 1 {
		t.Fatalf("summary = %#v", result.Summary)
	}
	if result.Repositories[0].Status == nil ||
		result.Repositories[0].Status.Classification != "unborn" {
		t.Fatalf("status = %#v", result.Repositories[0].Status)
	}
}

// TestArchivedRepositoriesAreExcludedButCounted pins both halves of the
// default. An archived repository is read-only at the provider and observe-only
// in the estate, so listing it beside live work is noise that grows with every
// project ever retired. Hiding it silently would be worse than listing it, so
// the summary always reports how many were left out.
func TestArchivedRepositoriesAreExcludedButCounted(t *testing.T) {
	live := Repository{}
	live.Path = "/estate/live"
	live.Lifecycle = "active"
	retired := Repository{}
	retired.Path = "/estate/retired"
	retired.Lifecycle = "archived"

	withArchived := summarize([]Repository{live, retired}, 0)
	if withArchived.Total != 2 || withArchived.ExcludedArchived != 0 {
		t.Fatalf("include-archived summary = %#v", withArchived)
	}

	def := summarize([]Repository{live}, 1)
	if def.Total != 1 {
		t.Fatalf("default listed %d repositories, want 1", def.Total)
	}
	if def.ExcludedArchived != 1 {
		t.Fatal("the default hid an archived repository without reporting it")
	}
}
