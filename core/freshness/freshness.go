// Package freshness owns claim-specific cache policy and evaluation.
package freshness

import (
	"errors"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
)

type Claim string

const (
	MutationPrecondition Claim = "mutation-precondition"
	WorktreeIndex        Claim = "worktree-index"
	LocalContext         Claim = "local-context"
	ProviderObservation  Claim = "provider-observation"
	DeviceInventory      Claim = "device-inventory"
	HarnessRuntime       Claim = "harness-runtime"
	ReleasePromotion     Claim = "release-promotion"
	VendorFact           Claim = "vendor-fact"
)

type Rule struct {
	Mode   string        `json:"mode" yaml:"mode"`
	MaxAge time.Duration `json:"max_age" yaml:"max_age"`
}

func (policy Policy) Digest() (string, error) {
	return canonicaljson.Digest(policy)
}

type Policy struct {
	Rules map[Claim]Rule `json:"rules" yaml:"rules"`
}

type Assessment struct {
	Claim      Claim         `json:"claim"`
	ObservedAt time.Time     `json:"observed_at"`
	Age        time.Duration `json:"age"`
	MaxAge     time.Duration `json:"max_age"`
	State      string        `json:"freshness_state"`
	Provenance string        `json:"provenance"`
}

func DefaultPolicy() Policy {
	return Policy{Rules: map[Claim]Rule{
		MutationPrecondition: {Mode: "immediate"},
		WorktreeIndex:        {Mode: "cached", MaxAge: 30 * time.Second},
		LocalContext:         {Mode: "cached", MaxAge: 5 * time.Minute},
		ProviderObservation:  {Mode: "cached", MaxAge: 15 * time.Minute},
		DeviceInventory:      {Mode: "cached", MaxAge: 6 * time.Hour},
		HarnessRuntime:       {Mode: "cached", MaxAge: 72 * time.Hour},
		ReleasePromotion:     {Mode: "cached", MaxAge: 72 * time.Hour},
		VendorFact:           {Mode: "cached", MaxAge: 24 * time.Hour},
	}}
}

func (policy Policy) Evaluate(claim Claim, observedAt, now time.Time, provenance string, freshRead bool) (Assessment, error) {
	rule, ok := policy.Rules[claim]
	if !ok || observedAt.IsZero() || now.IsZero() || observedAt.After(now) || provenance == "" {
		return Assessment{}, errors.New("freshness evaluation input is invalid")
	}
	age := now.Sub(observedAt)
	result := Assessment{Claim: claim, ObservedAt: observedAt, Age: age, MaxAge: rule.MaxAge, Provenance: provenance}
	switch rule.Mode {
	case "immediate":
		if freshRead {
			result.State = "fresh"
		} else {
			result.State = "stale"
		}
	case "cached":
		if rule.MaxAge <= 0 {
			return Assessment{}, errors.New("cached freshness rule has no positive maximum age")
		}
		if age <= rule.MaxAge {
			result.State = "fresh"
		} else {
			result.State = "stale"
		}
	default:
		return Assessment{}, errors.New("freshness rule mode is invalid")
	}
	return result, nil
}
