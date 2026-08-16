package harness

import "testing"

func TestRuntimeEvalPlanCountsImplicitPositiveQueriesOnly(t *testing.T) {
	t.Parallel()

	core, findings := loadRuntimeEvalPlan(repositoryRoot(t), "core")
	if len(findings) != 0 {
		t.Fatalf("core findings=%+v", findings)
	}
	coreCounts := runtimeEvalPlanCounts(core)
	if coreCounts["trigger-positive-recall"] != 72 {
		t.Fatalf("core positive attempts=%d, want 72", coreCounts["trigger-positive-recall"])
	}
	if coreCounts["trigger-near-miss-specificity"] != 168 {
		t.Fatalf("core specificity attempts=%d, want 168", coreCounts["trigger-near-miss-specificity"])
	}

	device, findings := loadRuntimeEvalPlan(repositoryRoot(t), "device")
	if len(findings) != 0 {
		t.Fatalf("device findings=%+v", findings)
	}
	deviceCounts := runtimeEvalPlanCounts(device)
	if attempts := deviceCounts["trigger-positive-recall"]; attempts != 0 {
		t.Fatalf("explicit-only device positive attempts=%d, want 0", attempts)
	}
	if attempts := deviceCounts["trigger-near-miss-specificity"]; attempts != 144 {
		t.Fatalf("explicit-only device specificity attempts=%d, want 144", attempts)
	}
}

func TestRuntimeEvidenceAllowsNotApplicableImplicitRecall(t *testing.T) {
	t.Parallel()

	threshold := 0.9
	findings := validateRuntimeEvidenceMetrics(RuntimeEvidence{Metrics: []EvalMetric{{
		ID: "trigger-positive-recall", Status: "not-applicable", Threshold: &threshold,
	}}})
	if len(findings) != 0 {
		t.Fatalf("findings=%+v", findings)
	}
}
