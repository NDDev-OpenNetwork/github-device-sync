package bundle

import (
	"strings"
	"testing"
)

func TestReleaseExecutableMatrixRequiresEveryCommandAndTarget(t *testing.T) {
	t.Parallel()
	valid := completeReleaseExecutableManifest()
	if err := ValidateReleaseExecutableMatrix(valid); err != nil {
		t.Fatalf("complete matrix rejected: %v", err)
	}

	for _, omitted := range RequiredReleaseExecutablePaths() {
		omitted := omitted
		t.Run(omitted, func(t *testing.T) {
			t.Parallel()
			candidate := valid
			candidate.Files = make([]FileRecord, 0, len(valid.Files)-1)
			for _, file := range valid.Files {
				if file.Path != omitted {
					candidate.Files = append(candidate.Files, file)
				}
			}
			if err := ValidateReleaseExecutableMatrix(candidate); err == nil || !strings.Contains(err.Error(), omitted) {
				t.Fatalf("omission %s error = %v", omitted, err)
			}
		})
	}
}

func TestReleaseExecutableMatrixRejectsUnexpectedAndWrongModeEntries(t *testing.T) {
	t.Parallel()
	manifest := completeReleaseExecutableManifest()
	manifest.Files = append(manifest.Files, FileRecord{
		Path: "bin/linux/amd64/unregistered", Digest: "sha256:" + strings.Repeat("a", 64),
		Size: 1, Mode: "0755",
	})
	if err := ValidateReleaseExecutableMatrix(manifest); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("unexpected executable error = %v", err)
	}

	manifest = completeReleaseExecutableManifest()
	manifest.Files[0].Mode = "0644"
	if err := ValidateReleaseExecutableMatrix(manifest); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("wrong mode error = %v", err)
	}
}

func TestReleaseContractAccessorsReturnCopies(t *testing.T) {
	t.Parallel()
	targets := RequiredReleaseTargets()
	executables := RequiredReleaseExecutables()
	targets[0].OS = "changed"
	executables[0] = "changed"
	if RequiredReleaseTargets()[0].OS != "darwin" || RequiredReleaseExecutables()[0] != "gds" {
		t.Fatal("release contract accessors exposed mutable canonical state")
	}
}

func completeReleaseExecutableManifest() Manifest {
	manifest := Manifest{}
	for _, path := range RequiredReleaseExecutablePaths() {
		manifest.Files = append(manifest.Files, FileRecord{
			Path: path, Digest: "sha256:" + strings.Repeat("a", 64), Size: 1, Mode: "0755",
		})
	}
	return manifest
}
