package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallationCandidateWritesAndVerifiesExactTree(t *testing.T) {
	root := newPortableFixture(t)
	candidate, findings := Build(root, testBuildOptions(root), testTrust(), testSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("build findings: %+v", findings)
	}
	installation, findings := PrepareInstallation(candidate.Artifact, candidate.Envelope, testSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("prepare findings: %+v", findings)
	}
	destination := filepath.Join(t.TempDir(), "payload")
	if err := installation.WriteNew(destination); err != nil {
		t.Fatal(err)
	}
	if err := installation.Verify(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(destination, "unexpected-empty-directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installation.Verify(destination); err == nil {
		t.Fatal("undeclared empty directory was accepted")
	}
	if err := os.Remove(filepath.Join(destination, "unexpected-empty-directory")); err != nil {
		t.Fatal(err)
	}

	tampered := filepath.Join(destination, filepath.FromSlash(installation.Files[0].Path))
	if err := os.WriteFile(tampered, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installation.Verify(destination); err == nil {
		t.Fatal("tampered installation was accepted")
	}
}

func TestInstallationCandidateRejectsSymlink(t *testing.T) {
	root := newPortableFixture(t)
	candidate, findings := Build(root, testBuildOptions(root), testTrust(), testSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("build findings: %+v", findings)
	}
	installation, findings := PrepareInstallation(candidate.Artifact, candidate.Envelope, testSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("prepare findings: %+v", findings)
	}
	destination := filepath.Join(t.TempDir(), "payload")
	if err := installation.WriteNew(destination); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(destination, filepath.FromSlash(installation.Files[0].Path))
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", target); err != nil {
		t.Fatal(err)
	}
	if err := installation.Verify(destination); err == nil {
		t.Fatal("symlinked installation member was accepted")
	}
}
