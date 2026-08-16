package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestPrepareZcodeRuntimeFixtureIsCleanAndExact(t *testing.T) {
	root := repositoryRoot(t)
	evidence := t.TempDir()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := PrepareZcodeRuntimeFixture(context.Background(), root, evidence, "core", schemas)
	if err != nil {
		t.Fatalf("prepare fixture: %v", err)
	}
	if fixture.Root != filepath.Join(evidence, "fixture") || fixture.CandidateDigest == "" ||
		fixture.SkillRoot == "" || len(fixture.IncludedSkills) == 0 ||
		len(fixture.IncludedSkills) != len(fixture.ImplicitSkills)+len(fixture.ExplicitOnlySkills) {
		t.Fatalf("unexpected fixture: %#v", fixture)
	}
	if !strings.Contains(filepath.ToSlash(fixture.SkillRoot), ".zcode") {
		t.Fatalf("skill root is not the zcode local skill root: %q", fixture.SkillRoot)
	}
	for _, path := range []string{
		"AGENTS.md", ".gds/repository.yaml", ".gds/bundle.lock.yaml",
		".gds/compiled-policy.json", filepath.Join("nested", "AGENTS.md"),
		filepath.Join(filepath.FromSlash(fixture.SkillRoot), "gds-orient", "SKILL.md"),
	} {
		info, err := os.Lstat(filepath.Join(fixture.Root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("fixture path %s err=%v", path, err)
		}
		// The skill tree is materialized via local symlinks (skill_strategy:
		// local-symlink); the workspace anchors must be real regular files.
		if !strings.Contains(filepath.ToSlash(path), fixture.SkillRoot) &&
			(!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			t.Fatalf("fixture anchor %s is not one regular file: info=%v", path, info)
		}
	}
	command := exec.Command("git", "status", "--porcelain=v2")
	command.Dir = fixture.Root
	output, err := command.Output()
	if err != nil || len(output) != 0 {
		t.Fatalf("fixture Git state output=%q err=%v", output, err)
	}
}

func TestPrepareZcodeRuntimeEnvironmentIsolatesZcodeHome(t *testing.T) {
	environment, err := PrepareZcodeRuntimeEnvironment(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	home := environment.Home
	defer func() {
		if err := environment.Cleanup(); err != nil {
			t.Errorf("cleanup environment: %v", err)
		}
	}()
	if home == "" || home == os.Getenv("HOME") {
		t.Fatalf("runtime home is not isolated: %q", home)
	}
	found := false
	for _, variable := range environment.Variables {
		if variable == "ZCODE_HOME="+filepath.Join(home, ".zcode") {
			found = true
		}
		// zcode is unauthenticated here: no credential must be exported.
		if strings.HasPrefix(variable, "ZCODE_API_KEY=") || strings.HasPrefix(variable, "ZAI_API_KEY=") {
			t.Fatalf("isolated environment leaked a credential: %q", variable)
		}
	}
	if !found {
		t.Fatalf("ZCODE_HOME is not isolated: %#v", environment.Variables)
	}
	info, err := os.Lstat(filepath.Join(home, ".zcode"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("isolated ZCODE_HOME directory info=%v err=%v", info, err)
	}
}
