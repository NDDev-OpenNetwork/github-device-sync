package projections

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

func TestMaterializerAppliesAndVerifiesExactCandidate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{
		InputDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OutputDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Files: []File{
			newFile(".gds/compiled-policy.json", []byte("{}\n")),
			newFile("AGENTS.md", []byte("generated\n")),
		},
	}
	materializer, err := NewMaterializer(root, candidate)
	if err != nil {
		t.Fatal(err)
	}
	step := operations.Step{Action: MaterializeAction, Parameters: Parameters(candidate)}
	evidence, err := materializer.Apply(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Before == nil || evidence.After == nil {
		t.Fatalf("evidence = %#v", evidence)
	}
	if err := materializer.Verify(context.Background(), step, nil); err != nil {
		t.Fatal(err)
	}
	if findings := Verify(root, candidate); len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestMaterializerRejectsSymlinkWithoutChangingOtherFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("safe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{
		InputDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OutputDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Files:        []File{newFile("AGENTS.md", []byte("generated\n"))},
	}
	materializer, err := NewMaterializer(root, candidate)
	if err != nil {
		t.Fatal(err)
	}
	_, err = materializer.Apply(context.Background(), operations.Step{
		Action: MaterializeAction, Parameters: Parameters(candidate),
	})
	if err == nil {
		t.Fatal("expected symlink rejection")
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil || string(content) != "safe\n" {
		t.Fatalf("target changed: %q %v", content, readErr)
	}
}

func TestMaterializerRollsBackEarlierFileWhenLaterFileIsUnsafe(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "a-first.txt")
	if err := os.WriteFile(firstPath, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "z-unsafe.txt")); err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{
		InputDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OutputDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Files: []File{
			newFile("a-first.txt", []byte("generated\n")),
			newFile("z-unsafe.txt", []byte("unsafe\n")),
		},
	}
	materializer, err := NewMaterializer(root, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.Apply(context.Background(), operations.Step{
		Action: MaterializeAction, Parameters: Parameters(candidate),
	}); err == nil {
		t.Fatal("expected unsafe later file to fail")
	}
	content, err := os.ReadFile(firstPath)
	if err != nil || string(content) != "original\n" {
		t.Fatalf("earlier file was not restored: %q %v", content, err)
	}
	targetContent, err := os.ReadFile(target)
	if err != nil || string(targetContent) != "target\n" {
		t.Fatalf("symlink target changed: %q %v", targetContent, err)
	}
}

func TestFingerprintChangesWithInputOrExistingOutput(t *testing.T) {
	root := t.TempDir()
	candidate := Candidate{
		InputDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Files:       []File{newFile("AGENTS.md", []byte("generated\n"))},
	}
	first, err := Fingerprint(root, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Fingerprint(root, candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate.InputDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	third, err := Fingerprint(root, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || second == third {
		t.Fatalf("fingerprints did not change: %s %s %s", first, second, third)
	}
}
