package rollout

import (
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

type TargetResult struct {
	RepositoryID         string `json:"repository_id"`
	Status               string `json:"status"`
	SecurityFailure      bool   `json:"security_failure"`
	RequiredCheckFailure bool   `json:"required_check_failure"`
}

type GateDecision struct {
	Action      string  `json:"action"`
	WaveID      string  `json:"wave_id"`
	NextWaveID  string  `json:"next_wave_id,omitempty"`
	FailureRate float64 `json:"failure_rate"`
}

func EvaluateWave(plan Plan, ordinal int, results []TargetResult) (GateDecision, []domain.Finding) {
	if ordinal < 0 || ordinal >= len(plan.Waves) {
		return GateDecision{}, []domain.Finding{finding(
			"GDS_ROLLOUT_WAVE_UNKNOWN", "Rollout wave ordinal is outside the immutable plan.",
		)}
	}
	wave := plan.Waves[ordinal]
	expected := make(map[string]struct{}, len(wave.RepositoryIDs))
	for _, repositoryID := range wave.RepositoryIDs {
		expected[repositoryID] = struct{}{}
	}
	seen := map[string]struct{}{}
	failed := 0
	securityFailures := 0
	requiredCheckFailures := 0
	incomplete := 0
	for _, result := range results {
		if _, found := expected[result.RepositoryID]; !found {
			return GateDecision{}, []domain.Finding{finding(
				"GDS_ROLLOUT_RESULT_OUTSIDE_WAVE",
				fmt.Sprintf("Repository %s is outside rollout wave %s.", result.RepositoryID, wave.ID),
			)}
		}
		if _, duplicate := seen[result.RepositoryID]; duplicate {
			return GateDecision{}, []domain.Finding{finding(
				"GDS_ROLLOUT_RESULT_DUPLICATE", "A repository has multiple results in one wave.",
			)}
		}
		seen[result.RepositoryID] = struct{}{}
		switch result.Status {
		case "succeeded":
		case "failed":
			failed++
		case "pending", "not-proven":
			incomplete++
		default:
			return GateDecision{}, []domain.Finding{finding(
				"GDS_ROLLOUT_RESULT_STATUS_INVALID", "Rollout target result status is invalid.",
			)}
		}
		if result.SecurityFailure {
			securityFailures++
		}
		if result.RequiredCheckFailure {
			requiredCheckFailures++
		}
	}
	incomplete += len(expected) - len(seen)
	denominator := len(expected)
	failureRate := 0.0
	if denominator > 0 {
		failureRate = float64(failed) / float64(denominator)
	}
	decision := GateDecision{WaveID: wave.ID, FailureRate: failureRate}
	if securityFailures > plan.Gates.SecurityFailureTolerance ||
		requiredCheckFailures > plan.Gates.RequiredCheckFailureTolerance ||
		failureRate > plan.Gates.MaxFailureRate {
		decision.Action = "pause"
		return decision, []domain.Finding{finding(
			"GDS_ROLLOUT_GATE_FAILED", "The current wave exceeded a release gate; later waves remain blocked.",
		)}
	}
	if incomplete > 0 {
		decision.Action = "wait"
		return decision, []domain.Finding{finding(
			"GDS_ROLLOUT_WAVE_NOT_PROVEN", "The current wave does not have a final result for every target.",
		)}
	}
	if ordinal == len(plan.Waves)-1 {
		decision.Action = "complete"
		return decision, nil
	}
	decision.Action = "advance"
	decision.NextWaveID = plan.Waves[ordinal+1].ID
	return decision, nil
}
