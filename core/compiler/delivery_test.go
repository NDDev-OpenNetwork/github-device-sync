package compiler

import "testing"

func TestContinuousDevelopmentProfileIsOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		anchor := testAnchor("repository-default")
		if enabled {
			anchor.Policy.Profiles = append(anchor.Policy.Profiles, "continuous-development")
		}
		result := New(testSchemas(t)).CompileDirectory(testRepositoryRoot(t), anchor, DevelopmentBundleVersion)
		if len(result.Findings) != 0 {
			t.Fatalf("findings: %#v", result.Findings)
		}
		if AdvisoryCI(result.Document) != enabled {
			t.Fatalf("advisory=%v want %v", AdvisoryCI(result.Document), enabled)
		}
		if enabled && result.Document.Provenance["/effective/delivery/profile"].Source != "continuous-development" {
			t.Fatal("delivery profile lacks provenance")
		}
	}
}

func TestDeliveryProfileRejectsUnknownValue(t *testing.T) {
	source := testSource("bad-delivery", "stack", 500, map[string]any{"delivery": map[string]any{"profile": "pretend-green"}})
	result := New(testSchemas(t)).Compile(testAnchor("bad-delivery"), map[string]PolicySource{"bad-delivery": source}, DevelopmentBundleVersion)
	if len(result.Findings) == 0 {
		t.Fatal("invalid delivery profile accepted")
	}
}
