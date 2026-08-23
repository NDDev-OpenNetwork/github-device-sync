package assurance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestRunProducesValidatedBoundedEvidence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := assuranceRepositoryRoot(t)
	stateParent := t.TempDir()
	if err := os.Chmod(stateParent, 0o700); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), Options{
		Root: root, StateDirectory: stateParent,
		RepositoryCount: 100, ForkCount: 50,
		SharedModuleCount: 2, ModuleConsumerCount: 50,
		// Use the production webhook sample size so fixed SQLite/WAL startup cost
		// cannot dominate the throughput acceptance metric on shared CI runners.
		WebhookDeliveryCount:      DefaultWebhookDeliveryCount,
		ReconciliationConcurrency: 2, ProjectionConcurrency: 2,
		ContextSamples: 2, RepositoryStatusSamples: 2,
	}, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if !testReportStatusAcceptable(report) || report.Scenario.Repositories != 100 ||
		report.Scenario.Installations != 5 || report.Scenario.Forks != 50 ||
		len(report.Checks) != 16 || len(report.Metrics) != 13 ||
		len(Validate(report, schemas)) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestExplicitPerformanceModesNeverUseAmbientCI(t *testing.T) {
	for id, configured := range budgets {
		if configured.Evidence != budgetEvidenceWallClock && configured.Evidence != budgetEvidenceStable {
			t.Fatalf("budget %s has no evidence class", id)
		}
	}
	timingFailure := Report{Status: "fail", Metrics: []Metric{{
		ID: "webhook-throughput-per-second", Passed: false,
	}}}
	if !ReportAcceptable(timingFailure, DeterministicRequired) ||
		!ReportAcceptable(timingFailure, AbsoluteCalibrated) {
		t.Fatal("wall clock observations bypassed their explicit evidence oracle")
	}
	// Adding a stable-budget failure must always be rejected, even under
	// timing relaxation: stable budgets are invariant contracts.
	timingFailure.Metrics = append(timingFailure.Metrics, Metric{
		ID: "state-db-bytes", Passed: false,
	})
	if testReportStatusAcceptable(timingFailure) {
		t.Fatal("timing-relaxed smoke accepted a non-timing budget failure")
	}
}

func TestCalibratedPolicyRequiresVarianceRunnerAndExactDigest(t *testing.T) {
	candidate := Report{Environment: Environment{OS: "linux", Architecture: "amd64", GoVersion: "go1.25", CPUCount: 8},
		Scenario: Scenario{Repositories: 2000}, Metrics: []Metric{{ID: "context-p95-ms", Unit: "milliseconds", Comparison: "at-most", Observed: 90}}}
	policy := CalibratedPolicy{SchemaVersion: 1, PolicyID: "runner-a", GeneratedAt: time.Now().UTC(), RunnerDigest: "sha256:runner",
		Environment: candidate.Environment, Scenario: candidate.Scenario, SampleCount: 20, VarianceEvidenceDigest: "sha256:variance",
		Metrics: []CalibratedMetric{{ID: "context-p95-ms", Unit: "milliseconds", Comparison: "at-most", Threshold: 100, Variance: 0.05}}}
	digestInput := policy
	digestInput.Digest = ""
	policy.Digest, _ = canonicaljson.Digest(digestInput)
	if err := EvaluateCalibrated(candidate, policy, "sha256:runner"); err != nil {
		t.Fatal(err)
	}
	if err := EvaluateCalibrated(candidate, policy, "sha256:other"); err == nil {
		t.Fatal("runner substitution accepted")
	}
	candidate.Metrics[0].Observed = 101
	if err := EvaluateCalibrated(candidate, policy, "sha256:runner"); err == nil {
		t.Fatal("absolute regression accepted")
	}
}

func TestPinnedRelativeBaselineRejectsRunnerOrEvidenceSubstitution(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	baselineReport := Report{Status: "pass", ResultDigest: "sha256:baseline", Source: Source{WorktreeClean: true}, Environment: Environment{OS: "linux", Architecture: "amd64", GoVersion: "go1.25", CPUCount: 8}, Scenario: Scenario{Repositories: 2000}}
	candidate := baselineReport
	candidate.ResultDigest = "sha256:candidate"
	baseline, err := NewBaseline("baseline-1", "sha256:runner", now, baselineReport)
	if err != nil {
		t.Fatal(err)
	}
	if err := ComparePinnedRelative(candidate, baselineReport, baseline, "sha256:runner", 0.1); err != nil {
		t.Fatal(err)
	}
	if err := ComparePinnedRelative(candidate, baselineReport, baseline, "sha256:other", 0.1); err == nil {
		t.Fatal("runner substitution accepted")
	}
	tampered := baseline
	tampered.ReportDigest = "sha256:other"
	if err := ComparePinnedRelative(candidate, baselineReport, tampered, "sha256:runner", 0.1); err == nil {
		t.Fatal("baseline substitution accepted")
	}
}

func TestCalibrationRequiresTenComparableCleanReports(t *testing.T) {
	reports := make([]Report, 10)
	for index := range reports {
		reports[index] = Report{Status: "pass", ResultDigest: fmt.Sprintf("sha256:report-%02d", index), Source: Source{WorktreeClean: true},
			Environment: Environment{OS: "linux", Architecture: "amd64", GoVersion: "go1.25", CPUCount: 8}, Scenario: Scenario{Repositories: 2000},
			Metrics: []Metric{{ID: "context-p95-ms", Unit: "milliseconds", Comparison: "at-most", Observed: 100 + float64(index), Passed: true}}}
	}
	policy, err := Calibrate(reports, "runner-a", "sha256:runner", time.Now())
	if err != nil || policy.SampleCount != 10 || len(policy.Metrics) != 1 {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	if _, err := Calibrate(reports[:9], "runner-a", "sha256:runner", time.Now()); err == nil {
		t.Fatal("insufficient samples accepted")
	}
	reports[9].Environment.OS = "darwin"
	if _, err := Calibrate(reports, "runner-a", "sha256:runner", time.Now()); err == nil {
		t.Fatal("mixed runner evidence accepted")
	}
}

func TestValidateRejectsDigestAndBudgetTampering(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := assuranceRepositoryRoot(t)
	stateParent := t.TempDir()
	if err := os.Chmod(stateParent, 0o700); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), Options{
		Root: root, StateDirectory: stateParent,
		RepositoryCount: 20, ForkCount: 10,
		SharedModuleCount: 2, ModuleConsumerCount: 10,
		// Keep performance evidence representative even though this test focuses
		// on digest and budget tamper detection.
		WebhookDeliveryCount:      DefaultWebhookDeliveryCount,
		ReconciliationConcurrency: 2, ProjectionConcurrency: 2,
		ContextSamples: 1, RepositoryStatusSamples: 1,
	}, schemas)
	if err != nil {
		t.Fatal(err)
	}
	report.Metrics[0].Limit++
	findings := Validate(report, schemas)
	if !hasFinding(findings, "GDS_ASSURANCE_BUDGET_DRIFT") ||
		!hasFinding(findings, "GDS_ASSURANCE_DIGEST_MISMATCH") {
		t.Fatalf("tampering findings: %+v", findings)
	}
}

func TestRunRejectsUnboundedOptions(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), Options{
		Root: assuranceRepositoryRoot(t), RepositoryCount: 2001,
	}, schemas)
	if err == nil {
		t.Fatal("unbounded repository count was accepted")
	}
}

func TestFullAssuranceScenario(t *testing.T) {
	if os.Getenv("GDS_FULL_ASSURANCE") != "1" {
		t.Skip("set GDS_FULL_ASSURANCE=1 to run the full 2000-repository scenario")
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), Options{
		Root: assuranceRepositoryRoot(t),
	}, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "pass" || report.Scenario.Repositories != DefaultRepositoryCount ||
		report.Scenario.Forks != DefaultForkCount {
		t.Fatalf("full assurance report: %+v", report)
	}
}

func assuranceRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

// testReportStatusAcceptable delegates to the production ReportAcceptable so
// the test and the CLI cannot diverge. See acceptance.go for the policy.
func testReportStatusAcceptable(report Report) bool {
	return ReportAcceptable(report, DeterministicRequired)
}
