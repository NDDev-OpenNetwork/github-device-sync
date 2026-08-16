package githubruleset

import (
	"encoding/json"
	"testing"
	"time"

	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

func TestRulesetAdoptionIsObservationOnlyAndRequiresFullState(t *testing.T) {
	state := githubprovider.RepositoryRulesetState{ID: 9, BypassActorsKnown: true, WritablePayload: json.RawMessage(`{"name":"main"}`), WritableDigest: "sha256:full"}
	plan, err := PlanAdoption(state, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC))
	if err != nil || plan.Status != "observation-only" || plan.UnknownPolicy != "preserve-or-refuse" || plan.PlanDigest == "" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	state.WritablePayload = nil
	if _, err := PlanAdoption(state, time.Now()); err == nil {
		t.Fatal("partial observation was adoptable")
	}
}
