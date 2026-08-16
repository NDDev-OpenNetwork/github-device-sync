package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func postureSchemas(t *testing.T) *validation.Set {
	t.Helper()
	set, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func findingCodes(t *testing.T, root string, posture Posture) []string {
	t.Helper()
	_, findings := ValidateWithPosture(root, postureSchemas(t), posture)
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	return codes
}

func has(codes []string, code string) bool {
	for _, candidate := range codes {
		if candidate == code {
			return true
		}
	}
	return false
}

// `agent.serena.enabled` is a required boolean in the repository schema, so
// declaring it false is a contract statement. It used to mean nothing: a module
// that keeps no memories was told its memory set was empty, which is a defect
// report for complying with its own anchor.
func TestADisabledAnchorIsNotAskedForMemories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	codes := findingCodes(t, root, Posture{Enabled: false})
	if len(codes) != 0 {
		t.Fatalf("codes = %#v", codes)
	}
	// The strict posture is what the same tree used to get unconditionally.
	strict := findingCodes(t, root, StrictPosture)
	if !has(strict, "GDS_MEMORY_ROOT_NOT_PROVEN") {
		t.Fatalf("strict codes = %#v", strict)
	}
}

// The opt-out is about what the repository owes, not about what it may contain.
// Memories present under a disabled declaration are agent state somebody wrote
// into a tree that says it keeps none, and the next reader has no contract
// telling them what it is.
func TestSerenaStateUnderADisabledAnchorIsReported(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, ".serena", "memories")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "core-example.md"), []byte("---\n---\n# x\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	codes := findingCodes(t, root, Posture{Enabled: false})
	if len(codes) != 1 || codes[0] != "GDS_MEMORY_DISABLED_STATE_PRESENT" {
		t.Fatalf("codes = %#v", codes)
	}
}

// A directory with no memory in it is the declared state, not a violation. The
// harness creates one; that is not the repository breaking its contract.
func TestAnEmptyDirectoryUnderADisabledAnchorIsNotAFinding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".serena", "memories"), 0o755); err != nil {
		t.Fatal(err)
	}
	if codes := findingCodes(t, root, Posture{Enabled: false}); len(codes) != 0 {
		t.Fatalf("codes = %#v", codes)
	}
}

// Every posture-dependent code must be one the strict path can actually emit.
// A typo in the drop list would silently keep reporting the finding it names,
// which is the same class of defect as the flag that governed nothing.
func TestEveryProvenanceCodeIsOneTheValidatorEmits(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("validator.go")
	if err != nil {
		t.Fatal(err)
	}
	for code := range provenanceCodes {
		if !strings.Contains(string(source), code) {
			t.Fatalf("%s is dropped by posture but never emitted", code)
		}
	}
}
