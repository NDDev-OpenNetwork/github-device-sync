package materialize

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixtureFile(path, content string) File {
	value := []byte(content)
	return File{Path: path, Content: value, Digest: digest(value)}
}

func TestNewSetRejectsTraversalAndOversizedContent(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"../escape", "/absolute", "nested/../../escape", "nested//file"} {
		if _, err := NewSet(root, []File{fixtureFile(path, "unsafe")}); err == nil {
			t.Fatalf("unsafe path %q accepted", path)
		}
	}
	if _, err := NewSet(root, []File{{
		Path: "large", Content: make([]byte, maxMaterializationFileBytes+1),
	}}); err == nil {
		t.Fatal("oversized materialization content accepted")
	}
}

func TestApplyWritesAndVerifiesRegularFileSet(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := NewSet(root, []File{
		fixtureFile("a.txt", "new-a\n"),
		fixtureFile("nested/b.txt", "new-b\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	before, after, err := set.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 || len(after) != 2 || before[0].State != "regular" ||
		before[1].State != "missing" {
		t.Fatalf("unexpected observations before=%#v after=%#v", before, after)
	}
	if err := set.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestTargetSymlinkSwapFailsClosedWithoutRedirectingAtomicWrite(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := NewSet(root, []File{fixtureFile("AGENTS.md", "generated\n")})
	if err != nil {
		t.Fatal(err)
	}
	set.hooks.beforeWriteCompare = func(string) {
		if err := os.Remove(target); err != nil {
			t.Error(err)
			return
		}
		if err := os.Symlink(outside, target); err != nil {
			t.Error(err)
		}
	}
	if _, _, err := set.Apply(); !errors.Is(err, ErrMaterializationConflict) {
		t.Fatalf("target race error=%v, want ErrMaterializationConflict", err)
	}
	outsideContent, err := os.ReadFile(outside)
	if err != nil || string(outsideContent) != "outside\n" {
		t.Fatalf("symlink target changed: %q %v", outsideContent, err)
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("external symlink was overwritten: %v %v", info, err)
	}
}

func TestParentSymlinkSwapCannotEscapeOpenedRoot(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "nested")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "file.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideRoot := t.TempDir()
	outside := filepath.Join(outsideRoot, "file.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := NewSet(root, []File{fixtureFile("nested/file.txt", "generated\n")})
	if err != nil {
		t.Fatal(err)
	}
	set.hooks.beforeWriteCompare = func(string) {
		if err := os.Rename(parent, filepath.Join(root, "nested-moved")); err != nil {
			t.Error(err)
			return
		}
		if err := os.Symlink(outsideRoot, parent); err != nil {
			t.Error(err)
		}
	}
	if _, _, err := set.Apply(); !errors.Is(err, ErrMaterializationConflict) {
		t.Fatalf("parent race error=%v, want ErrMaterializationConflict", err)
	}
	content, err := os.ReadFile(outside)
	if err != nil || string(content) != "outside\n" {
		t.Fatalf("parent symlink race escaped root: %q %v", content, err)
	}
	movedContent, err := os.ReadFile(filepath.Join(root, "nested-moved", "file.txt"))
	if err != nil || string(movedContent) != "original\n" {
		t.Fatalf("moved external file was overwritten: %q %v", movedContent, err)
	}
}

func TestApplyRejectsRegularFileChangesBeforeRename(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{
			name: "replacement",
			change: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("external replacement\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "in-place content write",
			change: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("external content\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mode change",
			change: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0o600)
				if info.Mode().Perm() == mode {
					mode = 0o640
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "policy.yaml")
			if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			set, err := NewSet(root, []File{fixtureFile("policy.yaml", "generated\n")})
			if err != nil {
				t.Fatal(err)
			}
			reached := make(chan string, 1)
			resume := make(chan struct{})
			defer closeSignal(resume)
			set.hooks.beforeWriteCompare = func(path string) {
				reached <- path
				<-resume
			}
			finished := applyAsync(set)
			if path := awaitString(t, reached); path != "policy.yaml" {
				t.Fatalf("write barrier path=%q", path)
			}
			test.change(t, target)
			closeSignal(resume)
			err = awaitError(t, finished)
			if !errors.Is(err, ErrMaterializationConflict) {
				t.Fatalf("apply error=%v, want ErrMaterializationConflict", err)
			}
			content, readErr := os.ReadFile(target)
			if readErr != nil || string(content) == "generated\n" {
				t.Fatalf("external content was overwritten: %q %v", content, readErr)
			}
		})
	}
}

func TestApplyRejectsNewFileThatAppearsBeforeRename(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "new-policy.yaml")
	set, err := NewSet(root, []File{fixtureFile("new-policy.yaml", "generated\n")})
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan string, 1)
	resume := make(chan struct{})
	defer closeSignal(resume)
	set.hooks.beforeWriteCompare = func(path string) {
		reached <- path
		<-resume
	}
	finished := applyAsync(set)
	if path := awaitString(t, reached); path != "new-policy.yaml" {
		t.Fatalf("write barrier path=%q", path)
	}
	if err := os.WriteFile(target, []byte("external creation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	closeSignal(resume)
	err = awaitError(t, finished)
	if !errors.Is(err, ErrMaterializationConflict) {
		t.Fatalf("apply error=%v, want ErrMaterializationConflict", err)
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil || string(content) != "external creation\n" {
		t.Fatalf("external creation was overwritten: %q %v", content, readErr)
	}
}

func TestLaterConflictRollsBackUnchangedGDSWrite(t *testing.T) {
	root := t.TempDir()
	aPath := filepath.Join(root, "a.txt")
	bPath := filepath.Join(root, "b.txt")
	for path, content := range map[string]string{aPath: "old-a\n", bPath: "old-b\n"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, err := NewSet(root, []File{
		fixtureFile("a.txt", "gds-a\n"),
		fixtureFile("b.txt", "gds-b\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var hookErr error
	set.hooks.beforeWriteCompare = func(path string) {
		if path == "b.txt" {
			hookErr = os.WriteFile(bPath, []byte("external-b\n"), 0o644)
		}
	}
	_, _, err = set.Apply()
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if !errors.Is(err, ErrMaterializationConflict) || errors.Is(err, ErrMaterializationPartial) {
		t.Fatalf("apply error=%v, want conflict with complete rollback", err)
	}
	for path, expected := range map[string]string{aPath: "old-a\n", bPath: "external-b\n"} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != expected {
			t.Fatalf("content at %s=%q err=%v", path, content, readErr)
		}
	}
}

func TestRollbackPreservesNewerExternalContentAndReportsPartialConflict(t *testing.T) {
	root := t.TempDir()
	aPath := filepath.Join(root, "a.txt")
	bPath := filepath.Join(root, "b.txt")
	for path, content := range map[string]string{aPath: "old-a\n", bPath: "old-b\n"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, err := NewSet(root, []File{
		fixtureFile("a.txt", "gds-a\n"),
		fixtureFile("b.txt", "gds-b\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeReached := make(chan string, 1)
	resumeWrite := make(chan struct{})
	rollbackReached := make(chan string, 1)
	resumeRollback := make(chan struct{})
	defer closeSignal(resumeWrite)
	defer closeSignal(resumeRollback)
	set.hooks.beforeWriteCompare = func(path string) {
		if path == "b.txt" {
			writeReached <- path
			<-resumeWrite
		}
	}
	set.hooks.beforeRollbackCompare = func(path string) {
		rollbackReached <- path
		<-resumeRollback
	}
	finished := applyAsync(set)
	if path := awaitString(t, writeReached); path != "b.txt" {
		t.Fatalf("write barrier path=%q", path)
	}
	if err := os.WriteFile(bPath, []byte("external-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	closeSignal(resumeWrite)
	if path := awaitString(t, rollbackReached); path != "a.txt" {
		t.Fatalf("rollback barrier path=%q", path)
	}
	if err := os.Remove(aPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, []byte("external-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	closeSignal(resumeRollback)
	err = awaitError(t, finished)
	if !errors.Is(err, ErrMaterializationConflict) ||
		!errors.Is(err, ErrMaterializationPartial) {
		t.Fatalf("apply error=%v, want conflict and partial classifications", err)
	}
	for path, expected := range map[string]string{
		aPath: "external-a\n", bPath: "external-b\n",
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != expected {
			t.Fatalf("external content at %s=%q err=%v", path, content, readErr)
		}
	}
}

func TestRollbackPreservesExternalContentWhenOriginalTargetWasMissing(t *testing.T) {
	root := t.TempDir()
	aPath := filepath.Join(root, "a.txt")
	bPath := filepath.Join(root, "b.txt")
	if err := os.WriteFile(bPath, []byte("old-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := NewSet(root, []File{
		fixtureFile("a.txt", "gds-a\n"),
		fixtureFile("b.txt", "gds-b\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var hookErr error
	set.hooks.beforeWriteCompare = func(path string) {
		if path == "b.txt" {
			hookErr = os.WriteFile(bPath, []byte("external-b\n"), 0o644)
		}
	}
	set.hooks.beforeRollbackCompare = func(path string) {
		if path == "a.txt" && hookErr == nil {
			hookErr = os.WriteFile(aPath, []byte("external-a\n"), 0o644)
		}
	}
	_, _, err = set.Apply()
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if !errors.Is(err, ErrMaterializationConflict) ||
		!errors.Is(err, ErrMaterializationPartial) {
		t.Fatalf("apply error=%v, want conflict and partial classifications", err)
	}
	content, readErr := os.ReadFile(aPath)
	if readErr != nil || string(content) != "external-a\n" {
		t.Fatalf("external content was removed: %q %v", content, readErr)
	}
}

func applyAsync(set *Set) <-chan error {
	finished := make(chan error, 1)
	go func() {
		_, _, err := set.Apply()
		finished <- err
	}()
	return finished
}

func awaitString(t *testing.T, values <-chan string) string {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for materialization barrier")
		return ""
	}
}

func awaitError(t *testing.T, values <-chan error) error {
	t.Helper()
	select {
	case err := <-values:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for materialization result")
		return nil
	}
}

func closeSignal(signal chan struct{}) {
	select {
	case <-signal:
	default:
		close(signal)
	}
}
