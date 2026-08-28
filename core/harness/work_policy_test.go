package harness

import "testing"

// The catalogue and the work-policy allowlist were once different sizes: ten
// catalogued harnesses had no setup system and were carried on-pause. Every
// catalogued harness is now delivered by one of the seven setup systems, so the
// two sets are identical and nothing can be catalogued-but-paused. A harness
// that is not in the catalogue is rejected as unknown, not as paused.
func TestWorkPolicyCatalogueEqualsActiveSeven(t *testing.T) {
	if len(CanonicalIDs) != 7 || len(WorkPolicyActiveIDs) != 7 {
		t.Fatalf("catalogue=%d active=%d, want 7 and 7", len(CanonicalIDs), len(WorkPolicyActiveIDs))
	}
	for index, id := range CanonicalIDs {
		if WorkPolicyActiveIDs[index] != id {
			t.Fatalf("catalogue and active set diverge at %d: %q vs %q", index, id, WorkPolicyActiveIDs[index])
		}
	}
	findings := ValidateDeviceSelection([]string{"codex", "zcode"})
	if len(findings) != 1 || findings[0].Code != "GDS_HARNESS_SELECTED_UNKNOWN" ||
		findings[0].Evidence["harness"] != "zcode" {
		t.Fatalf("a retired harness must be unknown, not paused: %#v", findings)
	}
	if findings := ValidateDeviceSelection(WorkPolicyActiveIDs); len(findings) != 0 {
		t.Fatalf("active seven rejected: %#v", findings)
	}
	if findings := ValidateDeviceSelection([]string{"codex", "codex"}); len(findings) != 1 ||
		findings[0].Code != "GDS_DEVICE_HARNESS_DUPLICATE" {
		t.Fatalf("duplicate selection must still be reported: %#v", findings)
	}
}
