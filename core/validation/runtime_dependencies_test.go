package validation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

const (
	shaCodexPinned = "dc6db7522fd0bbaa87312a9345bc28ac6daeb0ea"
	shaCodexHead   = "e8ee0199c255d863467a91d279c0f32aae88db52"
	shaOther       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func writeRuntimeDeps(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "estate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir estate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime-dependencies.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

func writeBootstrapContract(t *testing.T, root, codexCommit string) {
	t.Helper()
	dir := filepath.Join(root, "modules", "macos-ubuntu-bootstrap", "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir contract: %v", err)
	}
	body := `{"harnesses":{"policy":"one-owner-per-harness","active":["codex"],` +
		`"codex":{"owner_module":"nddev-codex-app","module_commit":"` + codexCommit + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "rldyour-contract.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
}

func codexEntry(pinned, available, reconciliation string) string {
	return "schema_version: 1\ndependencies:\n" +
		"  - id: \"nddev-codex-app\"\n" +
		"    repository_slug: \"example-org/nddev-codex-app\"\n" +
		"    url: \"https://github.com/example-org/nddev-codex-app.git\"\n" +
		"    consumption: \"runtime-clone\"\n" +
		"    consumer: \"macos-ubuntu-bootstrap\"\n" +
		"    harness: \"codex\"\n" +
		"    pinned_sha: \"" + pinned + "\"\n" +
		"    available_head: \"" + available + "\"\n" +
		"    reconciliation: \"" + reconciliation + "\"\n"
}

func hasCode(findings []domain.Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func TestRuntimeDependenciesCleanNoContract(t *testing.T) {
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatalf("schema set: %v", err)
	}
	root := t.TempDir()
	writeRuntimeDeps(t, root, codexEntry(shaCodexPinned, shaCodexHead, "pending"))
	count, findings := set.validateRuntimeDependencies(root)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings when bootstrap contract absent, got %v", findings)
	}
}

func TestRuntimeDependenciesMatchingContract(t *testing.T) {
	set, _ := NewSchemaSet()
	root := t.TempDir()
	writeRuntimeDeps(t, root, codexEntry(shaCodexPinned, shaCodexHead, "pending"))
	writeBootstrapContract(t, root, shaCodexPinned)
	_, findings := set.validateRuntimeDependencies(root)
	if len(findings) != 0 {
		t.Fatalf("expected no findings when pin matches contract, got %v", findings)
	}
}

func TestRuntimeDependenciesPinDrift(t *testing.T) {
	set, _ := NewSchemaSet()
	root := t.TempDir()
	writeRuntimeDeps(t, root, codexEntry(shaCodexPinned, shaCodexHead, "pending"))
	writeBootstrapContract(t, root, shaOther) // contract consumes a different commit
	_, findings := set.validateRuntimeDependencies(root)
	if !hasCode(findings, "GDS_RUNTIME_DEPENDENCY_PIN_DRIFT") {
		t.Fatalf("expected pin-drift finding, got %v", findings)
	}
}

func TestRuntimeDependenciesReconciliationInconsistent(t *testing.T) {
	set, _ := NewSchemaSet()
	root := t.TempDir()
	// marked current but pinned != available
	writeRuntimeDeps(t, root, codexEntry(shaCodexPinned, shaCodexHead, "current"))
	_, findings := set.validateRuntimeDependencies(root)
	if !hasCode(findings, "GDS_RUNTIME_DEPENDENCY_RECONCILIATION_INCONSISTENT") {
		t.Fatalf("expected reconciliation-inconsistent finding, got %v", findings)
	}
}

func TestRuntimeDependenciesDuplicate(t *testing.T) {
	set, _ := NewSchemaSet()
	root := t.TempDir()
	// Same id but a different available_head so the rows are not identical
	// (passes schema uniqueItems) and the Go id-uniqueness check must fire.
	body := codexEntry(shaCodexPinned, shaCodexHead, "pending") +
		"  - id: \"nddev-codex-app\"\n" +
		"    repository_slug: \"example-org/nddev-codex-app\"\n" +
		"    url: \"https://github.com/example-org/nddev-codex-app.git\"\n" +
		"    consumption: \"runtime-clone\"\n" +
		"    consumer: \"macos-ubuntu-bootstrap\"\n" +
		"    harness: \"codex\"\n" +
		"    pinned_sha: \"" + shaCodexPinned + "\"\n" +
		"    available_head: \"" + shaOther + "\"\n" +
		"    reconciliation: \"pending\"\n"
	writeRuntimeDeps(t, root, body)
	_, findings := set.validateRuntimeDependencies(root)
	if !hasCode(findings, "GDS_RUNTIME_DEPENDENCY_DUPLICATE") {
		t.Fatalf("expected duplicate finding, got %v", findings)
	}
}
