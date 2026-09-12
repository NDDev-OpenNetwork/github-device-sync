package governance

import (
	"context"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"testing"
)

func availabilityPolicy(mode string) compiler.CompiledPolicyDocument {
	return compiler.CompiledPolicyDocument{
		CompiledPolicy: compiler.CompiledPolicyMetadata{Digest: "sha256:policy"},
		Effective: map[string]any{"github": map[string]any{
			"merge":    map[string]any{"allow_squash_merge": map[string]any{"management": "managed", "value": true}},
			"rulesets": map[string]any{"management": mode},
		}},
	}
}

func TestAvailableSettingsCanBeAppliedWithExplicitUnavailableRulesets(t *testing.T) {
	snapshot := governanceTestSnapshot()
	snapshot.Repository.Merge.AllowSquashMerge = false
	snapshot.Unavailable = map[string]string{"github.rulesets": "plan-restricted"}
	comparison := Compare(availabilityPolicy("observed"), snapshot)
	if comparison.Counts["unavailable"] != 1 || comparison.Counts["drift"] != 1 {
		t.Fatalf("comparison=%#v", comparison)
	}
	for _, f := range comparison.Fields {
		if f.Path == "github.rulesets" && (f.Observed != nil || f.Reason != "plan-restricted") {
			t.Fatalf("unknown rulesets presented as observed: %#v", f)
		}
	}
	remediation, err := BuildRemediation(governanceTestScope(), snapshot, comparison)
	if err != nil {
		t.Fatal(err)
	}
	if len(remediation.Steps) != 1 || remediation.Steps[0].Action != RepositorySettingsAction {
		t.Fatalf("steps=%#v", remediation.Steps)
	}
	fixture := &governanceFixture{snapshot: snapshot}
	handler := &Handler{Reader: fixture, Writer: fixture, Scope: fixture.Scope(), Action: RepositorySettingsAction}
	if _, err := handler.Apply(context.Background(), remediation.Steps[0]); err != nil {
		t.Fatal(err)
	}
	if fixture.writes != 1 || !fixture.snapshot.Repository.Merge.AllowSquashMerge {
		t.Fatalf("available field not updated: %#v", fixture)
	}
	final := Compare(availabilityPolicy("observed"), fixture.snapshot)
	if final.Status != "partially-observed" || final.Counts["compliant"] != 1 || final.Counts["unavailable"] != 1 {
		t.Fatalf("partial coverage hidden: %#v", final)
	}
}

func TestManagedUnavailableFieldCannotProduceMutationPlan(t *testing.T) {
	snapshot := governanceTestSnapshot()
	snapshot.Unavailable = map[string]string{"github.rulesets": "plan-restricted"}
	comparison := Compare(availabilityPolicy("managed"), snapshot)
	if _, err := BuildRemediation(governanceTestScope(), snapshot, comparison); err == nil {
		t.Fatal("managed unavailable field was accepted")
	}
}

func TestUnavailableEvidenceIsImmutableAndDoesNotChangeHistoricalDigests(t *testing.T) {
	snapshot := governanceTestSnapshot()
	before := mustGovernanceDigest(t, snapshot)
	snapshot.Unavailable = map[string]string{}
	if mustGovernanceDigest(t, snapshot) != before {
		t.Fatal("empty availability metadata changed historical digest")
	}
	snapshot.Unavailable["github.rulesets"] = "plan-restricted"
	if mustGovernanceDigest(t, snapshot) == before {
		t.Fatal("unavailable rulesets indistinguishable from empty rulesets")
	}
	stable := Stabilize(snapshot)
	snapshot.Unavailable["github.rulesets"] = "changed"
	if stable.Unavailable["github.rulesets"] != "plan-restricted" {
		t.Fatal("stable evidence aliases mutable input")
	}
}
