package harness

import "testing"

func TestWorkPolicySeparatesCatalogueActiveAndDeviceSelection(t *testing.T) {
	if len(CanonicalIDs) != 17 || len(WorkPolicyActiveIDs) != 7 {
		t.Fatalf("catalogue=%d active=%d", len(CanonicalIDs), len(WorkPolicyActiveIDs))
	}
	findings := ValidateDeviceSelection([]string{"codex", "zcode"})
	if len(findings) != 1 || findings[0].Code != "GDS_DEVICE_HARNESS_PAUSED" || findings[0].Evidence["harness"] != "zcode" {
		t.Fatalf("findings=%#v", findings)
	}
	if findings := ValidateDeviceSelection(WorkPolicyActiveIDs); len(findings) != 0 {
		t.Fatalf("active seven rejected: %#v", findings)
	}
}
