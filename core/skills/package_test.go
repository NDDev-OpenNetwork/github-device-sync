package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestBuildPackagesAreDeterministicAndStandalone(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, plugin := range []string{"gds-core", "gds-estate-admin", "gds-module"} {
		first, findings := BuildPackage(root, plugin, schemas)
		if len(findings) != 0 {
			t.Fatalf("%s findings: %+v", plugin, findings)
		}
		second, findings := BuildPackage(root, plugin, schemas)
		if len(findings) != 0 {
			t.Fatalf("%s second findings: %+v", plugin, findings)
		}
		firstJSON, _ := json.Marshal(first)
		secondJSON, _ := json.Marshal(second)
		if string(firstJSON) != string(secondJSON) {
			t.Fatalf("%s package is not deterministic", plugin)
		}
		releaseFiles, err := first.ReleaseFiles()
		if err != nil || len(releaseFiles) != len(first.Files) {
			t.Fatalf("%s release files: count=%d err=%v", plugin, len(releaseFiles), err)
		}
		mutated := false
		for path, content := range releaseFiles {
			if len(content) != 0 {
				releaseFiles[path][0] ^= 0xff
				mutated = true
				break
			}
		}
		secondCopy, err := first.ReleaseFiles()
		if err != nil || !mutated || stringMapEqual(releaseFiles, secondCopy) {
			t.Fatalf("%s release files are not isolated", plugin)
		}
		destination := filepath.Join(t.TempDir(), plugin)
		if err := first.WriteNew(destination); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(destination, ".codex-plugin", "plugin.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(destination, "gds-package.json")); err != nil {
			t.Fatal(err)
		}
		for _, file := range first.Files {
			if filepath.IsAbs(file.Path) || file.Path == ".." {
				t.Fatalf("unsafe package path: %s", file.Path)
			}
			if ignoredPluginSourcePath(file.Path, false) {
				t.Fatalf("runtime noise entered %s package: %s", plugin, file.Path)
			}
		}
	}
}

func stringMapEqual(left, right map[string][]byte) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func TestWriteNewRejectsExistingDestination(t *testing.T) {
	destination := t.TempDir()
	candidate := PackageCandidate{Plugin: "gds-core", contents: map[string][]byte{"file": []byte("x")}}
	if err := candidate.WriteNew(destination); err == nil {
		t.Fatal("expected existing destination failure")
	}
}

func TestIgnoredPluginSourcePath(t *testing.T) {
	tests := []struct {
		path      string
		directory bool
		ignored   bool
	}{
		{path: ".DS_Store", ignored: true},
		{path: "hooks/._hook.py", ignored: true},
		{path: "hooks/__pycache__", directory: true, ignored: true},
		{path: "hooks/cache.pyc", ignored: true},
		{path: "hooks/editor.swp", ignored: true},
		{path: "hooks/editor.py~", ignored: true},
		{path: ".codex-plugin", directory: true},
		{path: ".codex-plugin/plugin.json"},
		{path: "hooks/gds_hook.py"},
	}
	for _, test := range tests {
		if actual := ignoredPluginSourcePath(test.path, test.directory); actual != test.ignored {
			t.Fatalf("path=%s directory=%v ignored=%v want=%v", test.path, test.directory, actual, test.ignored)
		}
	}
}
