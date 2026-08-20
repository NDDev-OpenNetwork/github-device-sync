package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func TestScanRejectsPortablePathsAndHighConfidenceSecrets(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "core", "unsafe.txt"),
		[]byte("path=/"+"Users/example/private\ntoken=ghp_"+strings.Repeat("a", 30)+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	report, findings := Scan(root, []gitprovider.TrackedPath{{
		Mode: "100644", Path: "core/unsafe.txt",
	}})
	if report.ScannedFiles != 1 || report.PortableFiles != 1 || len(findings) != 2 {
		t.Fatalf("report = %#v, findings = %#v", report, findings)
	}
}

func TestScanAllowsPortableVariablesAndPrivateDocumentationPaths(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"core/safe.txt", "docs/private.md"} {
		absolute := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "root=${HOME}/Developer\n"
		if path == "docs/private.md" {
			content = "migration evidence: /" + "Users/example/old\n"
		}
		if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, findings := Scan(root, []gitprovider.TrackedPath{
		{Mode: "100644", Path: "core/safe.txt"},
		{Mode: "100644", Path: "docs/private.md"},
	})
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestScanAllowsCanonicalLinuxbrewPrefixButRejectsUserHome(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		findings int
	}{
		{name: "canonical Linuxbrew", content: "/home/linuxbrew/.linuxbrew/bin/brew\n"},
		{name: "Linuxbrew child path", content: "/home/linuxbrew/.linuxbrew/opt/tool/bin/tool\n"},
		{name: "arbitrary Linux home", content: "/" + "home/example/.local/bin/tool\n", findings: 1},
		{name: "Linuxbrew account outside prefix", content: "/" + "home/linuxbrew/private/tool\n", findings: 1},
		{name: "Linuxbrew lookalike prefix", content: "/" + "home/linuxbrew/.linuxbrew-private/tool\n", findings: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "templates"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "templates", "path.txt"), []byte(test.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, findings := Scan(root, []gitprovider.TrackedPath{{Mode: "100644", Path: "templates/path.txt"}})
			if len(findings) != test.findings {
				t.Fatalf("findings = %#v, want %d", findings, test.findings)
			}
		})
	}
}
