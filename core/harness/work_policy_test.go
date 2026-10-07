package harness

import (
	"slices"
	"testing"
)

// The catalogue and the work-policy allowlist were once different sizes: ten
// catalogued harnesses had no setup system and were carried on-pause. Every
// seven execution adapters retain their proof requirements. An installed-only
// CLI may be observed without being selected for configuration or execution.
func TestWorkPolicyCatalogueIncludesObservedDevinAndActiveSeven(t *testing.T) {
	if len(CanonicalIDs) != 8 || len(WorkPolicyActiveIDs) != 7 {
		t.Fatalf("catalogue=%d active=%d, want 8 and 7", len(CanonicalIDs), len(WorkPolicyActiveIDs))
	}
	for _, id := range WorkPolicyActiveIDs {
		if !slices.Contains(CanonicalIDs, id) {
			t.Fatalf("active identity %q is not catalogued", id)
		}
	}
	if findings := ValidateDeviceSelection([]string{"devin"}); len(findings) != 1 || findings[0].Code != "GDS_DEVICE_HARNESS_PAUSED" {
		t.Fatalf("observation-only Devin must not be selected as a configured adapter: %+v", findings)
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
