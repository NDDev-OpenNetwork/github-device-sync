package discovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestDiscoverFindsNestedGitBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(first, "nested", "second")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("git", "init", "-q", path)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v\n%s", path, err, output)
		}
	}

	discovery := newTestDiscovery(t)
	result, err := discovery.Discover(context.Background(), root, Options{
		MaxDepth: 8, MaxRepositories: 10, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Boundaries) != 2 {
		t.Fatalf("boundaries = %#v", result.Boundaries)
	}
	for _, boundary := range result.Boundaries {
		if boundary.AnchorState != "missing" {
			t.Fatalf("anchor state = %q", boundary.AnchorState)
		}
	}
}

func TestDiscoverCurrentRepositoryAnchor(t *testing.T) {
	t.Parallel()
	discovery := newTestDiscovery(t)
	result, err := discovery.Discover(
		context.Background(), repositoryRoot(t), Options{MaxDepth: 1},
	)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(result.Boundaries) == 0 {
		t.Fatal("no boundaries found")
	}
	if result.Boundaries[0].RepositoryID != "repo_01JEXAMPZ0000000000000000A" {
		t.Fatalf("boundary = %#v", result.Boundaries[0])
	}
}

func TestDiscoverRejectsIdentityClaimedByDistinctGitStores(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	anchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(path, ".gds"), 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("git", "init", "-q", path)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v\n%s", path, err, output)
		}
		if err := os.WriteFile(filepath.Join(path, ".gds", "repository.yaml"), anchor, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := newTestDiscovery(t).Discover(
		context.Background(), root, Options{MaxDepth: 4, MaxRepositories: 10, Concurrency: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range result.Findings {
		if finding.Code == "GDS_CONTEXT_IDENTITY_CONFLICT" {
			return
		}
	}
	t.Fatalf("identity conflict not found: %#v", result.Findings)
}

func newTestDiscovery(t *testing.T) *Local {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := gitprovider.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	return NewLocal(runner, manifest.NewLoader(schemas))
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func TestDiscoverAcceptsIdentityPinnedBySuperprojectAndCheckedOutStandalone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	anchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	writeAnchoredRepository := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(path, ".gds"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"init", "-q", path},
			{"-C", path, "config", "user.email", "test@example.com"},
			{"-C", path, "config", "user.name", "test"},
		} {
			if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, output)
			}
		}
		if err := os.WriteFile(filepath.Join(path, ".gds", "repository.yaml"), anchor, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The pinned copy: one repository committed, then added as a submodule of a
	// superproject. Its checkout under modules/ is the copy the superproject
	// pins. The source lives outside the scanned root so that only the pinned
	// checkout and the standalone one are discovered.
	pinned := filepath.Join(t.TempDir(), "pinned-source")
	writeAnchoredRepository(pinned)
	for _, args := range [][]string{
		{"-C", pinned, "add", "-A"},
		{"-C", pinned, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "anchor"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	superproject := filepath.Join(root, "superproject")
	if output, err := exec.Command("git", "init", "-q", superproject).CombinedOutput(); err != nil {
		t.Fatalf("git init superproject: %v\n%s", err, output)
	}
	add := exec.Command(
		"git", "-C", superproject, "-c", "protocol.file.allow=always",
		"submodule", "add", "-q", pinned, "modules/pinned",
	)
	if output, err := add.CombinedOutput(); err != nil {
		t.Skipf("submodule add unavailable in this environment: %v\n%s", err, output)
	}

	// The standalone copy: the same identity, materialized as an ordinary
	// workspace checkout with no superproject deciding its commit.
	writeAnchoredRepository(filepath.Join(root, "standalone"))

	result, err := newTestDiscovery(t).Discover(
		context.Background(), root, Options{MaxDepth: 5, MaxRepositories: 20, Concurrency: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range result.Findings {
		if finding.Code == "GDS_CONTEXT_IDENTITY_CONFLICT" {
			t.Fatalf("a pinned submodule must not compete with a standalone checkout: %#v", finding)
		}
	}
}
