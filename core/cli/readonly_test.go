package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestReadOnlyCommandsPreserveRepositoryState(t *testing.T) {
	root := newReadOnlyFixture(t)
	commands := []struct {
		name string
		args []string
	}{
		{name: "context", args: []string{"--json", "--cwd", root, "context"}},
		{name: "status", args: []string{"--json", "--cwd", root, "status"}},
		{
			name: "discover",
			args: []string{
				"--json", "--cwd", root, "discover", "--root", root,
				"--max-depth", "2", "--max-repositories", "8", "--concurrency", "2",
			},
		},
		{
			name: "inventory",
			args: []string{
				"--json", "--cwd", root, "inventory", "--root", root,
				"--max-depth", "2", "--max-repositories", "8", "--concurrency", "2",
			},
		},
		{name: "validate", args: []string{"--json", "--cwd", root, "validate"}},
		{
			name: "validate-repository",
			args: []string{"--json", "--cwd", root, "validate", "repository"},
		},
		{
			name: "validate-schemas",
			args: []string{"--json", "--cwd", root, "validate", "schemas"},
		},
		{name: "doctor", args: []string{"--json", "--cwd", root, "doctor"}},
		{
			name: "compile-policy",
			args: []string{"--json", "--cwd", root, "compile", "policy"},
		},
		{
			name: "generate-repository",
			args: []string{"--json", "--cwd", root, "generate", "repository"},
		},
		{
			name: "generate-repository-check",
			args: []string{"--json", "--cwd", root, "generate", "repository", "--check"},
		},
		{
			name: "validate-projections",
			args: []string{"--json", "--cwd", root, "validate", "projections"},
		},
	}

	baseline := snapshotTree(t, root)
	for _, test := range commands {
		t.Run(test.name, func(t *testing.T) {
			exitCode, envelope, stderr := executeJSON(t, test.args...)
			if exitCode == 14 {
				t.Fatalf("internal error: stderr = %q, envelope = %#v", stderr, envelope)
			}
			if envelope.Mutation.Attempted || envelope.Mutation.Completed {
				t.Fatalf("read-only command reported mutation: %#v", envelope.Mutation)
			}
			assertEnvelopeSchema(t, envelope)
			after := snapshotTree(t, root)
			if !maps.Equal(baseline, after) {
				t.Fatalf(
					"read-only command changed repository state:\n%s",
					strings.Join(snapshotDelta(baseline, after), "\n"),
				)
			}
		})
	}
}

func newReadOnlyFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runFixtureGit(t, root, "init", "-b", "main")
	runFixtureGit(t, root, "config", "user.name", "GDS Test")
	runFixtureGit(t, root, "config", "user.email", "gds-test@example.invalid")

	if err := os.MkdirAll(filepath.Join(root, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gds", "repository.yaml"), anchor, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, root, "add", "--all")
	runFixtureGit(t, root, "commit", "-m", "test: initialize fixture")
	return root
}

func runFixtureGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	arguments = append([]string{
		"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
	}, arguments...)
	command := exec.CommandContext(context.Background(), "git", arguments...)
	command.Dir = root
	command.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) && strings.HasSuffix(path, ".lock") {
				return nil
			}
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(relative), ".git/") && strings.HasSuffix(relative, ".lock") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && strings.HasSuffix(path, ".lock") {
				return nil
			}
			return err
		}
		value := info.Mode().String()
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += ":" + target
		case info.Mode().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(":%x", sha256.Sum256(content))
		}
		snapshot[filepath.ToSlash(relative)] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func snapshotDelta(before, after map[string]string) []string {
	keys := map[string]struct{}{}
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	changes := []string{}
	for _, key := range ordered {
		if before[key] != after[key] {
			changes = append(changes, fmt.Sprintf("%s: %q -> %q", key, before[key], after[key]))
		}
	}
	return changes
}
