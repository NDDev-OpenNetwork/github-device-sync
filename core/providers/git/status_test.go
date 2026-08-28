package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePorcelainV2(t *testing.T) {
	t.Parallel()
	raw := []byte(
		"# branch.oid 0123456789abcdef0123456789abcdef01234567\x00" +
			"# branch.head task/example\x00" +
			"# branch.upstream origin/task/example\x00" +
			"# branch.ab +2 -1\x00" +
			"1 M. N... 100644 100644 100644 a b file.txt\x00" +
			"1 .M S.M. 160000 160000 160000 a b module\x00" +
			"? new.txt\x00",
	)
	status, err := parsePorcelainV2(raw)
	if err != nil {
		t.Fatalf("parsePorcelainV2() error = %v", err)
	}
	if status.Head.Mode != "branch" || status.Branch.Name != "task/example" {
		t.Fatalf("unexpected head/branch: %#v %#v", status.Head, status.Branch)
	}
	if status.Branch.Ahead != 2 || status.Branch.Behind != 1 || !status.Branch.Diverged {
		t.Fatalf("unexpected ahead/behind: %#v", status.Branch)
	}
	if status.Changes.Staged != 1 || status.Changes.Unstaged != 1 ||
		status.Changes.Untracked != 1 || status.Changes.SubmoduleChanges != 1 {
		t.Fatalf("unexpected changes: %#v", status.Changes)
	}
}

func TestReadOnlyRunnerRejectsMutation(t *testing.T) {
	t.Parallel()
	runner := NewRunnerForPath("git", 1024)
	if _, err := runner.Run(context.Background(), t.TempDir(), "fetch", "origin"); err == nil {
		t.Fatal("Runner.Run(fetch) error = nil, want read-only rejection")
	}
	if _, err := runner.Run(context.Background(), t.TempDir(), "diff", "--output=result"); err == nil {
		t.Fatal("Runner.Run(diff --output) error = nil, want read-only rejection")
	}
}

func TestCommittedSourceOIDRequiresCommittedBoundedInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	directory := createCommittedRepository(t)
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	initial, err := runner.HeadOID(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := runner.CommittedSourceOID(
		context.Background(), directory, []string{"tracked.txt"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != initial {
		t.Fatalf("source oid = %q, want %q", resolved, initial)
	}

	writeFile(t, directory, "tracked.txt", "changed\n")
	if _, err := runner.CommittedSourceOID(
		context.Background(), directory, []string{"tracked.txt"},
	); err == nil {
		t.Fatal("CommittedSourceOID() accepted an uncommitted canonical source")
	}

	runGit(t, directory, "checkout", "--", "tracked.txt")
	writeFile(t, directory, "unrelated.txt", "local\n")
	resolved, err = runner.CommittedSourceOID(
		context.Background(), directory, []string{"tracked.txt"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != initial {
		t.Fatalf("source oid after unrelated change = %q, want %q", resolved, initial)
	}
	runGit(t, directory, "add", "unrelated.txt")
	runGit(t, directory, "commit", "-qm", "generated projection")
	resolved, err = runner.CommittedSourceOID(
		context.Background(), directory, []string{"tracked.txt"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != initial {
		t.Fatalf("source oid after generated-only commit = %q, want %q", resolved, initial)
	}
}

func TestCommittedSourceOIDRejectsUnsafePaths(t *testing.T) {
	runner := NewRunnerForPath("git", 1024)
	for _, path := range []string{"", ".", "../outside", "/absolute", ":(glob)**"} {
		if _, err := runner.CommittedSourceOID(
			context.Background(), t.TempDir(), []string{path},
		); err == nil {
			t.Fatalf("CommittedSourceOID(%q) error = nil", path)
		}
	}
}

func TestCommittedSourceOIDCollapsesEquivalentSyntheticMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	directory := createCommittedRepository(t)
	runGit(t, directory, "checkout", "-qb", "feature")
	writeFile(t, directory, "tracked.txt", "feature\n")
	runGit(t, directory, "add", "tracked.txt")
	runGit(t, directory, "commit", "-qm", "feature source")
	featureOID := strings.TrimSpace(runGitOutput(t, directory, "rev-parse", "HEAD"))

	runGit(t, directory, "checkout", "-q", "main")
	writeFile(t, directory, "unrelated.txt", "main\n")
	runGit(t, directory, "add", "unrelated.txt")
	runGit(t, directory, "commit", "-qm", "main only")
	runGit(t, directory, "merge", "--no-ff", "-qm", "synthetic merge", "feature")

	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := runner.CommittedSourceOID(
		context.Background(), directory, []string{"tracked.txt"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != featureOID {
		t.Fatalf("source oid through equivalent merge = %q, want %q", resolved, featureOID)
	}
}

func TestCommittedSourceOIDRetainsMergeThatChangesSourceBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	directory := createCommittedRepository(t)
	runGit(t, directory, "checkout", "-qb", "feature")
	writeFile(t, directory, "tracked.txt", "feature\n")
	runGit(t, directory, "add", "tracked.txt")
	runGit(t, directory, "commit", "-qm", "feature source")

	runGit(t, directory, "checkout", "-q", "main")
	writeFile(t, directory, "tracked.txt", "main\n")
	runGit(t, directory, "add", "tracked.txt")
	runGit(t, directory, "commit", "-qm", "main source")
	merge := exec.Command("git", "merge", "--no-ff", "--no-commit", "feature")
	merge.Dir = directory
	if err := merge.Run(); err == nil {
		t.Fatal("conflicting merge unexpectedly succeeded")
	}
	writeFile(t, directory, "tracked.txt", "resolved\n")
	runGit(t, directory, "add", "tracked.txt")
	runGit(t, directory, "commit", "-qm", "resolved source merge")
	mergeOID := strings.TrimSpace(runGitOutput(t, directory, "rev-parse", "HEAD"))

	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := runner.CommittedSourceOID(
		context.Background(), directory, []string{"tracked.txt"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != mergeOID {
		t.Fatalf("source oid after source-changing merge = %q, want %q", resolved, mergeOID)
	}
}

func TestInspectStatusRealRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	directory := t.TempDir()
	runGit(t, directory, "init", "-q")
	runGit(t, directory, "config", "user.name", "GDS Test")
	runGit(t, directory, "config", "user.email", "gds@example.invalid")
	if err := os.WriteFile(filepath.Join(directory, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "add", "tracked.txt")
	runGit(t, directory, "commit", "-qm", "initial")
	if err := os.WriteFile(filepath.Join(directory, "tracked.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	status, err := runner.InspectStatus(context.Background(), directory)
	if err != nil {
		t.Fatalf("InspectStatus() error = %v", err)
	}
	if status.Classification != "dirty" {
		t.Fatalf("classification = %q, want dirty", status.Classification)
	}
	if status.Changes.Unstaged != 1 || status.Changes.Untracked != 1 {
		t.Fatalf("changes = %#v", status.Changes)
	}
	realDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Worktrees) != 1 || status.Worktrees[0].Path != realDirectory {
		t.Fatalf("worktrees = %#v", status.Worktrees)
	}
}

func TestRepositoryInfoHandlesQuotedPath(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	directory := filepath.Join(t.TempDir(), "repository\nwith-newline")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "init", "-q")
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	info, err := runner.RepositoryInfo(context.Background(), directory)
	if err != nil {
		t.Fatalf("RepositoryInfo() error = %v", err)
	}
	expected, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.WorktreeRoot != expected {
		t.Fatalf("worktree root = %q, want %q", info.WorktreeRoot, expected)
	}
}

func TestInspectStatusDetachedAndMultipleWorktrees(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	directory := createCommittedRepository(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, directory, "worktree", "add", "-qb", "task/example", linked)
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	status, err := runner.InspectStatus(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Worktrees) != 2 {
		t.Fatalf("worktrees = %#v", status.Worktrees)
	}
	runGit(t, linked, "checkout", "--detach", "-q")
	detached, err := runner.InspectStatus(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if detached.Head.Mode != "detached" || detached.Classification != "detached" {
		t.Fatalf("detached status = %#v", detached)
	}
	paths := map[string]bool{}
	for _, worktree := range detached.Worktrees {
		paths[worktree.Path] = true
	}
	if len(paths) != 2 || !paths[filepath.Clean(directory)] || !paths[filepath.Clean(linked)] {
		t.Fatalf("linked worktree paths = %#v, want %q and %q", paths, directory, linked)
	}
}

func TestInspectStatusCachedAheadBehindAndDiverged(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	runGit(t, root, "init", "--bare", "-q", origin)
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-qb", "main")
	configureIdentity(t, seed)
	writeFile(t, seed, "file.txt", "base\n")
	runGit(t, seed, "add", "file.txt")
	runGit(t, seed, "commit", "-qm", "base")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "-qu", "origin", "main")
	runGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")

	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	runGit(t, root, "clone", "-q", origin, first)
	runGit(t, root, "clone", "-q", origin, second)
	configureIdentity(t, first)
	configureIdentity(t, second)
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, first, "ahead.txt", "ahead\n")
	runGit(t, first, "add", "ahead.txt")
	runGit(t, first, "commit", "-qm", "ahead")
	ahead, err := runner.InspectStatus(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if ahead.Classification != "ahead-cached" || ahead.Branch.Ahead != 1 {
		t.Fatalf("ahead status = %#v", ahead)
	}

	writeFile(t, second, "remote.txt", "remote\n")
	runGit(t, second, "add", "remote.txt")
	runGit(t, second, "commit", "-qm", "remote")
	runGit(t, second, "push", "-q", "origin", "main")
	runGit(t, first, "fetch", "-q", "origin")
	diverged, err := runner.InspectStatus(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if diverged.Classification != "diverged-cached" || !diverged.Branch.Diverged {
		t.Fatalf("diverged status = %#v", diverged)
	}

	runGit(t, first, "reset", "--hard", "-q", "origin/main")
	writeFile(t, second, "remote-2.txt", "remote-2\n")
	runGit(t, second, "add", "remote-2.txt")
	runGit(t, second, "commit", "-qm", "remote 2")
	runGit(t, second, "push", "-q", "origin", "main")
	runGit(t, first, "fetch", "-q", "origin")
	behind, err := runner.InspectStatus(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if behind.Classification != "behind-cached" || behind.Branch.Behind != 1 {
		t.Fatalf("behind status = %#v", behind)
	}
}

func TestInspectStatusConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	directory := createCommittedRepository(t)
	runGit(t, directory, "checkout", "-qb", "side")
	writeFile(t, directory, "tracked.txt", "side\n")
	runGit(t, directory, "commit", "-qam", "side")
	runGit(t, directory, "checkout", "-q", "main")
	writeFile(t, directory, "tracked.txt", "main\n")
	runGit(t, directory, "commit", "-qam", "main")
	command := exec.Command("git", "merge", "side")
	command.Dir = directory
	if err := command.Run(); err == nil {
		t.Fatal("git merge unexpectedly succeeded")
	}
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	status, err := runner.InspectStatus(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if status.Classification != "conflicted" || status.Changes.Conflicted == 0 {
		t.Fatalf("conflict status = %#v", status)
	}
}

func TestParseSubmodules(t *testing.T) {
	t.Parallel()
	state := parseSubmodules([]byte(
		" 0123456789abcdef0123456789abcdef01234567 clean\n" +
			"+1123456789abcdef0123456789abcdef01234567 modified\n" +
			"-2123456789abcdef0123456789abcdef01234567 missing\n" +
			"U3123456789abcdef0123456789abcdef01234567 conflict\n",
	))
	if state.Total != 4 || state.Clean != 1 || state.Modified != 1 ||
		state.Uninitialized != 1 || state.Conflicted != 1 {
		t.Fatalf("state = %#v", state)
	}
}

func createCommittedRepository(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	runGit(t, directory, "init", "-qb", "main")
	configureIdentity(t, directory)
	writeFile(t, directory, "tracked.txt", "base\n")
	runGit(t, directory, "add", "tracked.txt")
	runGit(t, directory, "commit", "-qm", "initial")
	return directory
}

func configureIdentity(t *testing.T, directory string) {
	t.Helper()
	runGit(t, directory, "config", "user.name", "GDS Test")
	runGit(t, directory, "config", "user.email", "gds@example.invalid")
}

func writeFile(t *testing.T, directory, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}
