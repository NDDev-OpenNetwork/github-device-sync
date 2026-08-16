package releasebuilder

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestReleaseArtifactUsesInspectedCommitSnapshot(t *testing.T) {
	const relative = "policies/base/test.yaml"
	committed := []byte("value: committed\n")
	mutated := []byte("value: mutated-after-inspection\n")
	repository := snapshotRepository(t, map[string][]byte{
		relative:                      committed,
		"skills/registry.yaml":        []byte("schema_version: 5\nskills: []\n"),
		"harnesses/test/profile.yaml": []byte("fixture: true\n"),
	})
	source, err := inspectSource(context.Background(), repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, filepath.FromSlash(relative)), mutated, 0o644); err != nil {
		t.Fatal(err)
	}

	temporary := t.TempDir()
	home := filepath.Join(temporary, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, tracked, err := materializeSourceSnapshot(
		context.Background(), repository, temporary, source.Commit, home,
	)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := os.ReadFile(filepath.Join(snapshot, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(observed, committed) {
		t.Fatalf("snapshot used moving worktree bytes: %q", observed)
	}
	if err := os.WriteFile(filepath.Join(snapshot, filepath.FromSlash(relative)), mutated, 0o644); err == nil {
		t.Fatal("read-only release source snapshot was mutable")
	}

	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	trust := bundle.TrustPolicy{
		Source: bundle.TrustSource{
			Owner: "owner", Repository: "repository",
			AllowedWorkflows: []string{".github/workflows/release.yml"},
			AllowedRefs:      []string{"refs/heads/main"},
		},
		Release: bundle.TrustRelease{
			MinimumReleaseSequence: 1,
			AllowedChannels:        []string{"canary"},
		},
	}
	candidate, findings := bundle.Build(snapshot, bundle.BuildOptions{
		BundleVersion: "1.2.3-canary.1", ReleaseSequence: 1, Channel: "canary",
		SourceCommit: source.Commit, SourceRef: source.Ref, MinimumCLIVersion: "1.2.3",
		Workflow: trust.Source.AllowedWorkflows[0], TrackedSources: tracked,
		HarnessEvidenceProvisional: true,
	}, trust, schemas)
	if len(findings) != 0 {
		t.Fatalf("build snapshot artifact: %+v", findings)
	}
	manifest, findings := bundle.VerifyReleaseUnit(candidate.Artifact, candidate.Envelope, schemas)
	if len(findings) != 0 {
		t.Fatalf("verify snapshot artifact: %+v", findings)
	}
	expectedDigest := digestBytes(committed)
	verified := false
	for _, record := range manifest.Files {
		if record.Path == relative {
			if record.Digest != expectedDigest {
				t.Fatalf("artifact bound moving bytes: got %s want %s", record.Digest, expectedDigest)
			}
			verified = true
			break
		}
	}
	if !verified {
		t.Fatalf("artifact omitted committed source %s", relative)
	}
	if err := cleanupReleaseTemporary(temporary); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(temporary); !os.IsNotExist(err) {
		t.Fatalf("release temporary workspace survived cleanup: %v", err)
	}
}

func TestReleaseSourceInspectionIgnoresAmbientGitExecutableAndRepositorySelection(t *testing.T) {
	repository := snapshotRepository(t, map[string][]byte{
		"skills/registry.yaml": []byte("schema_version: 5\nskills: []\n"),
	})
	other := snapshotRepository(t, map[string][]byte{
		"skills/registry.yaml": []byte("schema_version: 5\nskills: []\n"),
	})
	marker := filepath.Join(t.TempDir(), "fake-git-ran")
	fakeDirectory := t.TempDir()
	fakeGit := filepath.Join(fakeDirectory, "git")
	fake := "#!/bin/sh\ntouch '" + marker + "'\nprintf '%040d\\n' 0\n"
	if err := os.WriteFile(fakeGit, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDirectory)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(other, ".git", "objects"))

	source, err := inspectSource(context.Background(), repository, "refs/heads/main")
	if err != nil || source.Commit == "" || source.Ref != "refs/heads/main" {
		t.Fatalf("inspect trusted release source: source=%+v err=%v", source, err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("ambient PATH Git executed during release inspection: %v", err)
	}
}

func TestValidateRequestRejectsOutputInsideSourceRoot(t *testing.T) {
	repository := snapshotRepository(t, map[string][]byte{
		"skills/registry.yaml": []byte("schema_version: 5\nskills: []\n"),
	})
	_, _, err := validateRequest(Request{
		Root: repository, OutputDirectory: filepath.Join(repository, "release-output"),
		Version: "1.2.3", ReleaseSequence: 1, Channel: "canary",
		MinimumCLIVersion: "1.2.3",
	})
	if err == nil {
		t.Fatal("release output inside the source root was accepted")
	}
}

func TestResolveGoBinaryIgnoresAmbientPath(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fake-go-ran")
	fakeDirectory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(fakeDirectory, "go"),
		[]byte("#!/bin/sh\ntouch '"+marker+"'\nexit 97\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDirectory)
	resolved, err := resolveGoBinary("go")
	if err != nil || !filepath.IsAbs(resolved) || filepath.Dir(resolved) == fakeDirectory {
		t.Fatalf("resolved Go binary = %q, err=%v", resolved, err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("ambient PATH Go executed during resolution: %v", err)
	}
}

func TestSnapshotTreePreflightRejectsOversizedBlobBeforeArchive(t *testing.T) {
	t.Parallel()
	entry := []byte(fmt.Sprintf(
		"100644 blob %s %d\toversized.bin%c",
		strings.Repeat("a", 40), maxSourceSnapshotFileBytes+1, 0,
	))
	if _, err := parseSnapshotTree(entry); err == nil {
		t.Fatal("oversized source blob passed preflight")
	}
}

func TestSnapshotTreeSkipsSubmoduleGitlinks(t *testing.T) {
	t.Parallel()
	// A submodule appears as a "160000 commit" gitlink entry with a "-" size. The
	// control-plane release snapshot pins modules by commit but embeds only its own
	// blobs, so the gitlink is skipped rather than rejected as a non-blob.
	entry := []byte(fmt.Sprintf(
		"160000 commit %s -\tmodules/ci-workflows%c100644 blob %s 12\tREADME.md%c",
		strings.Repeat("a", 40), 0, strings.Repeat("b", 40), 0,
	))
	files, err := parseSnapshotTree(entry)
	if err != nil {
		t.Fatalf("gitlink entry must be skipped, not rejected: %v", err)
	}
	if len(files) != 1 || files[0].path != "README.md" {
		t.Fatalf("expected only the blob to be captured, got %#v", files)
	}
}

func TestSourceSnapshotRejectsOversizedCommitBeforePrivateFetch(t *testing.T) {
	repository := snapshotRepository(t, map[string][]byte{
		"skills/registry.yaml": []byte("schema_version: 5\nskills: []\n"),
	})
	gitTestCommand(
		t, repository, "commit", "--allow-empty", "-m", strings.Repeat("oversized-commit-message-", 128),
	)
	source, err := inspectSource(context.Background(), repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	home := filepath.Join(temporary, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := productionSourceSnapshotLimits()
	limits.commitObjectBytes = 512
	_, _, err = materializeSourceSnapshotWithLimits(
		context.Background(), repository, temporary, source.Commit, home, limits,
	)
	if !errors.Is(err, errSourceSnapshotCommitLimit) {
		t.Fatalf("oversized commit object was not rejected: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(temporary, "source.git")); !os.IsNotExist(err) {
		t.Fatalf("private fetch workspace was created before commit-size rejection: %v", err)
	}
}

func TestSourceSnapshotBoundsArchiveExportSubstExpansion(t *testing.T) {
	repository := snapshotRepository(t, map[string][]byte{
		".gitattributes":       []byte("expanded.txt export-subst\n"),
		"expanded.txt":         []byte(strings.Repeat("$Format:%B$\n", 128)),
		"skills/registry.yaml": []byte("schema_version: 5\nskills: []\n"),
	})
	gitTestCommand(
		t, repository, "commit", "--amend", "-m", strings.Repeat("bounded-archive-message-", 48),
	)
	source, err := inspectSource(context.Background(), repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	home := filepath.Join(temporary, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := productionSourceSnapshotLimits()
	limits.archiveBytes = 20 << 10
	_, _, err = materializeSourceSnapshotWithLimits(
		context.Background(), repository, temporary, source.Commit, home, limits,
	)
	if !errors.Is(err, errSourceSnapshotArchiveLimit) {
		t.Fatalf("export-subst expansion did not hit the streaming archive bound: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(temporary, "source.tar")); !os.IsNotExist(err) {
		t.Fatalf("partial bounded archive was retained: %v", err)
	}
	if err := cleanupReleaseTemporary(temporary); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSnapshotRejectsTrackedSymlink(t *testing.T) {
	repository := snapshotRepository(t, map[string][]byte{
		"skills/registry.yaml": []byte("schema_version: 5\nskills: []\n"),
	})
	link := filepath.Join(repository, "policies", "base", "link.yaml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../outside", link); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, repository, "add", "policies/base/link.yaml")
	gitTestCommand(t, repository, "commit", "-m", "add link")
	source, err := inspectSource(context.Background(), repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	home := filepath.Join(temporary, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err = materializeSourceSnapshot(context.Background(), repository, temporary, source.Commit, home)
	if err == nil || !strings.Contains(err.Error(), "unsupported mode") {
		t.Fatalf("tracked symlink was not rejected: %v", err)
	}
	if err := cleanupReleaseTemporary(temporary); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSnapshotRejectsArchiveTraversal(t *testing.T) {
	temporary := t.TempDir()
	archivePath := filepath.Join(temporary, "source.tar")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(archive)
	content := []byte("escape")
	if err := writer.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(temporary, "source")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := extractSourceSnapshot(archivePath, snapshot); err == nil {
		t.Fatal("traversing release archive path was accepted")
	}
	if _, err := os.Lstat(filepath.Join(temporary, "escape")); !os.IsNotExist(err) {
		t.Fatalf("archive traversal escaped snapshot root: %v", err)
	}
}

func TestSourceSnapshotRejectsArchiveContentTransformation(t *testing.T) {
	repository := snapshotRepository(t, map[string][]byte{
		".gitattributes":          []byte("policies/base/test.yaml export-subst\n"),
		"policies/base/test.yaml": []byte("commit: $Format:%H$\n"),
		"skills/registry.yaml":    []byte("schema_version: 5\nskills: []\n"),
	})
	source, err := inspectSource(context.Background(), repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	home := filepath.Join(temporary, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err = materializeSourceSnapshot(context.Background(), repository, temporary, source.Commit, home)
	if err == nil || !strings.Contains(err.Error(), "differs from commit") {
		t.Fatalf("Git archive content transformation was not rejected: %v", err)
	}
	if err := cleanupReleaseTemporary(temporary); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseTemporaryCleanupDoesNotFollowSymlinks(t *testing.T) {
	parent := t.TempDir()
	temporary := filepath.Join(parent, "release-temporary")
	if err := os.Mkdir(temporary, 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(parent, "external")
	if err := os.WriteFile(external, []byte("external"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(temporary, "link")); err != nil {
		t.Fatal(err)
	}
	if err := cleanupReleaseTemporary(temporary); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(external)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("cleanup changed symlink target mode: %o", info.Mode().Perm())
	}
}

func snapshotRepository(t *testing.T, files map[string][]byte) string {
	t.Helper()
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = realRoot
	gitTestCommand(t, root, "init", "--initial-branch=main")
	gitTestCommand(t, root, "config", "user.name", "GDS Test")
	gitTestCommand(t, root, "config", "user.email", "gds-test@example.invalid")
	for relative, content := range files {
		name := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitTestCommand(t, root, "add", "--all")
	gitTestCommand(t, root, "commit", "-m", "fixture")
	return root
}

func gitTestCommand(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	if _, err := run(context.Background(), directory, nil, "git", arguments...); err != nil {
		t.Fatalf("git %s: %v", strings.Join(arguments, " "), err)
	}
}
