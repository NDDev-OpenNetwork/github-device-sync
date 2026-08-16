package freshness

import (
	"testing"
	"time"
)

func TestClaimSpecificFreshnessAndImmediateMutation(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	policy := DefaultPolicy()
	fresh, err := policy.Evaluate(LocalContext, now.Add(-4*time.Minute), now, "sqlite:context", false)
	if err != nil || fresh.State != "fresh" || fresh.MaxAge != 5*time.Minute {
		t.Fatalf("assessment=%#v err=%v", fresh, err)
	}
	stale, _ := policy.Evaluate(ProviderObservation, now.Add(-16*time.Minute), now, "sqlite:github", false)
	if stale.State != "stale" {
		t.Fatalf("assessment=%#v", stale)
	}
	cachedMutation, _ := policy.Evaluate(MutationPrecondition, now, now, "sqlite:provider", false)
	freshMutation, _ := policy.Evaluate(MutationPrecondition, now, now, "provider:fresh-read", true)
	if cachedMutation.State != "stale" || freshMutation.State != "fresh" {
		t.Fatalf("cached=%#v fresh=%#v", cachedMutation, freshMutation)
	}
}
