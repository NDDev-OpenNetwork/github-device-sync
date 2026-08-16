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

func TestPrepareClaudeRuntimeFixtureIsCleanAndExact(t *testing.T) {
	root := repositoryRoot(t)
	evidence := t.TempDir()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := PrepareClaudeRuntimeFixture(context.Background(), root, evidence, "core", schemas)
	if err != nil {
		t.Fatalf("prepare fixture: %v", err)
	}
	if fixture.Root != filepath.Join(evidence, "fixture") || fixture.CandidateDigest == "" ||
		fixture.SkillRoot == "" || len(fixture.IncludedSkills) != 5 ||
		len(fixture.ImplicitSkills) != 3 || len(fixture.ExplicitOnlySkills) != 2 {
		t.Fatalf("unexpected fixture: %#v", fixture)
	}
	for _, path := range []string{
		filepath.Join(".claude", "CLAUDE.md"), ".gds/repository.yaml", ".gds/bundle.lock.yaml",
		".gds/compiled-policy.json", filepath.Join("nested", ".claude", "CLAUDE.md"),
		filepath.Join(fixture.SkillRoot, "gds-orient", "SKILL.md"),
	} {
		info, err := os.Lstat(filepath.Join(fixture.Root, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("fixture path %s info=%v err=%v", path, info, err)
		}
	}
	command := exec.Command("git", "status", "--porcelain=v2")
	command.Dir = fixture.Root
	output, err := command.Output()
	if err != nil || len(output) != 0 {
		t.Fatalf("fixture Git state output=%q err=%v", output, err)
	}
}

func TestPrepareClaudeRuntimeEnvironmentIsIsolated(t *testing.T) {
	credential := filepath.Join(t.TempDir(), ".credentials.json")
	if err := os.WriteFile(credential, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment, err := prepareClaudeRuntimeEnvironment(repositoryRoot(t), credential)
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
		if variable == "CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude") {
			found = true
		}
	}
	if !found {
		t.Fatalf("CLAUDE_CONFIG_DIR is not isolated: %#v", environment.Variables)
	}
	// The credential must be a private copy, never a symlink back to the real one.
	credentialCopy := filepath.Join(home, ".claude", ".credentials.json")
	info, err := os.Lstat(credentialCopy)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o600 {
		t.Fatalf("isolated credential copy info=%v err=%v", info, err)
	}
	settingsInfo, err := os.Lstat(filepath.Join(home, ".claude", "settings.json"))
	if err != nil || !settingsInfo.Mode().IsRegular() || settingsInfo.Mode().Perm() != 0o600 {
		t.Fatalf("isolated settings info=%v err=%v", settingsInfo, err)
	}
}

func TestWriteClaudeHookSettingsMatchesLifecycleContract(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeClaudeHookSettings(home, "/abs/gds_hook.py"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"SessionStart"`, `"PreToolUse"`, `"Stop"`,
		"/abs/gds_hook.py session-start", "/abs/gds_hook.py pre-tool-use", "/abs/gds_hook.py stop",
		`"matcher": "Bash"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("settings.json missing %q: %s", want, raw)
		}
	}
}
