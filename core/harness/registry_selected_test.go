package harness

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func notProvenHarnesses(findings []domain.Finding) map[string]bool {
	got := map[string]bool{}
	for _, finding := range findings {
		if finding.Code == "GDS_HARNESS_RUNTIME_NOT_PROVEN" {
			got[fmt.Sprint(finding.Evidence["harness"])] = true
		}
	}
	return got
}

// TestControlPlaneDelegatesHarnessRuntimeProof proves the control plane does not
// gate on harness runtime evidence: runtime/behavioral proof is owned by the
// isolated per-harness setup systems, so every profile declares
// runtime_tests.required=false and no harness raises GDS_HARNESS_RUNTIME_NOT_PROVEN
// in either all-mode or selected-mode. This supersedes the earlier RVR-P2-009
// selected-only gating; the control plane now requires no harness runtime evidence
// at all, because that responsibility moved into the isolated harness apps.
func TestControlPlaneDelegatesHarnessRuntimeProof(t *testing.T) {
	root := repoRootForTest(t)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatalf("schema set: %v", err)
	}

	_, allFindings := ValidateAll(root, schemas)
	if gated := notProvenHarnesses(allFindings); len(gated) != 0 {
		t.Fatalf("control plane must not runtime-gate any harness in all-mode, got %v", gated)
	}

	_, selectedFindings := ValidateSelected(root, []string{"codex", "zcode"}, schemas)
	if gated := notProvenHarnesses(selectedFindings); len(gated) != 0 {
		t.Fatalf("control plane must not runtime-gate selected harnesses, got %v", gated)
	}
}
