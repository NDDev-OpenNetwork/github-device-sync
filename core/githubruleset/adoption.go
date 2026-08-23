package githubruleset

import (
	"errors"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

// AdoptionPlan is observation-only evidence. It does not authorize update or
// create; a subsequent mutation plan must bind its digest and receive a new
// signed approval.
type AdoptionPlan struct {
	SchemaVersion  int       `json:"schema_version"`
	RulesetID      int64     `json:"ruleset_id"`
	ObservedAt     time.Time `json:"observed_at"`
	ObservedDigest string    `json:"observed_digest"`
	OwnedPaths     []string  `json:"owned_paths"`
	ExternalPaths  []string  `json:"external_paths"`
	UnknownPolicy  string    `json:"unknown_policy"`
	Status         string    `json:"status"`
	PlanDigest     string    `json:"plan_digest"`
}

func PlanAdoption(state githubprovider.RepositoryRulesetState, observedAt time.Time) (AdoptionPlan, error) {
	if state.ID <= 0 || observedAt.IsZero() || !state.BypassActorsKnown || len(state.WritablePayload) == 0 || state.WritableDigest == "" {
		return AdoptionPlan{}, errors.New("ruleset adoption requires a fresh full privileged observation")
	}
	plan := AdoptionPlan{SchemaVersion: 1, RulesetID: state.ID, ObservedAt: observedAt.UTC(), ObservedDigest: state.WritableDigest,
		OwnedPaths:    []string{"/enforcement", "/rules/pull_request", "/rules/required_status_checks"},
		ExternalPaths: []string{"/bypass_actors", "/conditions", "/rules/*"},
		UnknownPolicy: "preserve-or-refuse", Status: "observation-only"}
	digest, err := canonicaljson.Digest(plan)
	if err != nil {
		return AdoptionPlan{}, err
	}
	plan.PlanDigest = digest
	return plan, nil
}
