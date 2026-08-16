package rollout

import (
	"fmt"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const rolloutID = "rollout_01J00000000000000000000000"

func TestBuildAllocatesTwoThousandTargetsDeterministically(t *testing.T) {
	targets := repositoryIDs(2000)
	input := rolloutInput(targets)
	first, findings := Build(input, rolloutSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("build findings: %#v", findings)
	}
	if len(first.Waves) != 4 {
		t.Fatalf("unexpected wave count: %d", len(first.Waves))
	}
	wantCounts := []int{5, 15, 180, 1800}
	for index, expected := range wantCounts {
		if observed := len(first.Waves[index].RepositoryIDs); observed != expected {
			t.Fatalf("wave %d count = %d, want %d", index, observed, expected)
		}
	}

	for left, right := 0, len(targets)-1; left < right; left, right = left+1, right-1 {
		targets[left], targets[right] = targets[right], targets[left]
	}
	secondInput := rolloutInput(targets)
	second, findings := Build(secondInput, rolloutSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("second build findings: %#v", findings)
	}
	if first.PlanDigest != second.PlanDigest || first.TargetSetDigest != second.TargetSetDigest {
		t.Fatal("target input order changed deterministic rollout plan")
	}
}

func TestBuildRejectsDuplicateAndUnassignedTargets(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		input := rolloutInput([]string{repositoryID(1), repositoryID(1)})
		_, findings := Build(input, rolloutSchemas(t))
		assertRolloutFinding(t, findings, "GDS_ROLLOUT_DUPLICATE_TARGET")
	})

	t.Run("unassigned", func(t *testing.T) {
		input := rolloutInput(repositoryIDs(10))
		input.Rings = []RingSpec{{ID: "canary", MaxRepositories: 5}}
		_, findings := Build(input, rolloutSchemas(t))
		assertRolloutFinding(t, findings, "GDS_ROLLOUT_TARGETS_UNASSIGNED")
	})
}

func TestValidateDetectsPlanTampering(t *testing.T) {
	plan, findings := Build(rolloutInput(repositoryIDs(10)), rolloutSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("build findings: %#v", findings)
	}
	plan.Waves[0].RepositoryIDs[0] = repositoryID(999)
	findings = Validate(plan, rolloutSchemas(t))
	assertRolloutFinding(t, findings, "GDS_ROLLOUT_PLAN_DIGEST_MISMATCH")
}

func TestEvaluateWaveBlocksLaterWavesUntilGatesPass(t *testing.T) {
	plan, findings := Build(rolloutInput(repositoryIDs(10)), rolloutSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("build findings: %#v", findings)
	}
	first := plan.Waves[0]
	results := make([]TargetResult, 0, len(first.RepositoryIDs))
	for _, repositoryID := range first.RepositoryIDs {
		results = append(results, TargetResult{RepositoryID: repositoryID, Status: "succeeded"})
	}

	decision, findings := EvaluateWave(plan, 0, results)
	if len(findings) != 0 || decision.Action != "advance" || decision.NextWaveID == "" {
		t.Fatalf("passing wave did not advance: decision=%#v findings=%#v", decision, findings)
	}

	results[0].SecurityFailure = true
	decision, findings = EvaluateWave(plan, 0, results)
	assertRolloutFinding(t, findings, "GDS_ROLLOUT_GATE_FAILED")
	if decision.Action != "pause" {
		t.Fatalf("security failure did not pause rollout: %#v", decision)
	}

	results[0].SecurityFailure = false
	results = results[:len(results)-1]
	decision, findings = EvaluateWave(plan, 0, results)
	assertRolloutFinding(t, findings, "GDS_ROLLOUT_WAVE_NOT_PROVEN")
	if decision.Action != "wait" {
		t.Fatalf("incomplete wave did not wait: %#v", decision)
	}
}

func rolloutInput(repositoryIDs []string) BuildInput {
	return BuildInput{
		RolloutID: rolloutID,
		CreatedAt: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
		Envelope: bundle.ReleaseEnvelope{
			SchemaVersion: 1, BundleVersion: "1.0.0", ReleaseSequence: 1,
			Channel: "canary", SourceCommit: "0123456789abcdef0123456789abcdef01234567",
			ManifestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ArtifactDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		RepositoryIDs: repositoryIDs,
		Rings: []RingSpec{
			{ID: "canary", MaxRepositories: 5},
			{ID: "representative", CumulativePercent: 1},
			{ID: "early", CumulativePercent: 10},
			{ID: "general", CumulativePercent: 100},
		},
		MaxFailureRate: 0.02,
	}
}

func repositoryIDs(count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = repositoryID(index + 1)
	}
	return values
}

func repositoryID(index int) string {
	return fmt.Sprintf("repo_01J%023d", index)
}

func rolloutSchemas(t *testing.T) *validation.Set {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	return schemas
}

func assertRolloutFinding(t *testing.T, findings []domain.Finding, code string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("expected finding %s, got %#v", code, findings)
}
