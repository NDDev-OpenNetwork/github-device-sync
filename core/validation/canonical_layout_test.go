package validation

import (
	"os"
	"path/filepath"
	"testing"
)

// An external estate owns its anchor and skills registry but pins the engine
// as a module; the engine-distribution inputs must be validated from the
// engine root, not the authority root. Measured on the live estate: reading
// them from the authority root produced five unconditional read failures
// (issue #65).
func TestValidateCanonicalReadsEngineInputsFromTheEngineRoot(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatalf("NewSchemaSet() error = %v", err)
	}
	engineRoot := repositoryRoot(t)
	authorityRoot := t.TempDir()
	for _, relative := range []string{
		filepath.Join(".gds", "repository.yaml"),
		filepath.Join("skills", "registry.yaml"),
	} {
		raw, err := os.ReadFile(filepath.Join(engineRoot, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		target := filepath.Join(authorityRoot, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if findings := set.ValidateCanonical(authorityRoot, engineRoot, ""); len(findings) != 0 {
		t.Fatalf("external layout findings = %#v", findings)
	}

	// The control pins the defect this split fixes: collapsing both roots
	// onto the authority loses exactly the five engine-distribution inputs.
	missing := map[string]bool{}
	for _, finding := range set.ValidateCanonical(authorityRoot, authorityRoot, "") {
		if finding.Code != "GDS_INPUT_READ_FAILED" {
			t.Fatalf("unexpected finding on collapsed roots: %#v", finding)
		}
		missing[finding.Message] = true
	}
	if len(missing) != 5 {
		t.Fatalf("collapsed roots lost %d inputs, want the 5 engine inputs: %v", len(missing), missing)
	}
}
