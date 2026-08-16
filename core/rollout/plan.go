// Package rollout builds immutable, bounded canary and wave rollout plans.
package rollout

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maxTargets = 2000

type Plan struct {
	SchemaVersion   int       `json:"schema_version"`
	RolloutID       string    `json:"rollout_id"`
	CreatedAt       time.Time `json:"created_at"`
	Bundle          Bundle    `json:"bundle"`
	TargetSetDigest string    `json:"target_set_digest"`
	TargetCount     int       `json:"target_count"`
	Waves           []Wave    `json:"waves"`
	Gates           Gates     `json:"gates"`
	Mutation        Mutation  `json:"mutation"`
	PlanDigest      string    `json:"plan_digest"`
}

type Bundle struct {
	Version         string `json:"version"`
	ReleaseSequence int    `json:"release_sequence"`
	Channel         string `json:"channel"`
	ArtifactDigest  string `json:"artifact_digest"`
	ManifestDigest  string `json:"manifest_digest"`
}

type Wave struct {
	ID            string   `json:"id"`
	Ordinal       int      `json:"ordinal"`
	RepositoryIDs []string `json:"repository_ids"`
	Status        string   `json:"status"`
}

type Gates struct {
	MaxFailureRate                float64 `json:"max_failure_rate"`
	SecurityFailureTolerance      int     `json:"security_failure_tolerance"`
	RequiredCheckFailureTolerance int     `json:"required_check_failure_tolerance"`
}

type Mutation struct {
	Mode      string `json:"mode"`
	AutoMerge bool   `json:"auto_merge"`
}

type RingSpec struct {
	ID                string  `json:"id"`
	MaxRepositories   int     `json:"max_repositories,omitempty"`
	CumulativePercent float64 `json:"cumulative_percent,omitempty"`
}

type BuildInput struct {
	RolloutID      string
	CreatedAt      time.Time
	Envelope       bundle.ReleaseEnvelope
	RepositoryIDs  []string
	Rings          []RingSpec
	MaxFailureRate float64
}

func Build(input BuildInput, schemas *validation.Set) (Plan, []domain.Finding) {
	if len(input.RepositoryIDs) == 0 || len(input.RepositoryIDs) > maxTargets {
		return Plan{}, []domain.Finding{finding(
			"GDS_ROLLOUT_TARGET_COUNT_INVALID", "Rollout target count must be between 1 and 2000.",
		)}
	}
	if len(input.Rings) == 0 || input.MaxFailureRate < 0 || input.MaxFailureRate > 1 {
		return Plan{}, []domain.Finding{finding(
			"GDS_ROLLOUT_POLICY_INVALID", "Rollout rings and failure gate must be explicit and bounded.",
		)}
	}
	targets := append([]string(nil), input.RepositoryIDs...)
	sort.Strings(targets)
	for index := 1; index < len(targets); index++ {
		if targets[index] == targets[index-1] {
			return Plan{}, []domain.Finding{finding(
				"GDS_ROLLOUT_DUPLICATE_TARGET", "A repository occurs more than once in the rollout target set.",
			)}
		}
	}
	targetDigest, err := canonicaljson.Digest(targets)
	if err != nil {
		return Plan{}, []domain.Finding{finding("GDS_ROLLOUT_DIGEST_FAILED", err.Error())}
	}
	ordered := deterministicOrder(input.RolloutID, targets)
	waves, findings := allocateWaves(ordered, input.Rings)
	if len(findings) != 0 {
		return Plan{}, findings
	}
	plan := Plan{
		SchemaVersion: domain.SchemaVersion,
		RolloutID:     input.RolloutID, CreatedAt: input.CreatedAt.UTC(),
		Bundle: Bundle{
			Version: input.Envelope.BundleVersion, ReleaseSequence: input.Envelope.ReleaseSequence,
			Channel: input.Envelope.Channel, ArtifactDigest: input.Envelope.ArtifactDigest,
			ManifestDigest: input.Envelope.ManifestDigest,
		},
		TargetSetDigest: targetDigest, TargetCount: len(targets), Waves: waves,
		Gates:    Gates{MaxFailureRate: input.MaxFailureRate},
		Mutation: Mutation{Mode: "pull-request", AutoMerge: false},
	}
	digest, err := planDigest(plan)
	if err != nil {
		return Plan{}, []domain.Finding{finding("GDS_ROLLOUT_DIGEST_FAILED", err.Error())}
	}
	plan.PlanDigest = digest
	if findings := Validate(plan, schemas); len(findings) != 0 {
		return Plan{}, findings
	}
	return plan, nil
}

func allocateWaves(targets []string, rings []RingSpec) ([]Wave, []domain.Finding) {
	seenIDs := map[string]struct{}{}
	waves := []Wave{}
	assigned := 0
	for _, ring := range rings {
		if ring.ID == "" {
			return nil, []domain.Finding{finding("GDS_ROLLOUT_RING_INVALID", "Rollout ring id is empty.")}
		}
		if _, duplicate := seenIDs[ring.ID]; duplicate {
			return nil, []domain.Finding{finding("GDS_ROLLOUT_RING_DUPLICATE", "Rollout ring ids must be unique.")}
		}
		seenIDs[ring.ID] = struct{}{}
		usesMax := ring.MaxRepositories > 0
		usesPercent := ring.CumulativePercent > 0
		if usesMax == usesPercent || ring.CumulativePercent > 100 {
			return nil, []domain.Finding{finding(
				"GDS_ROLLOUT_RING_INVALID", "Each ring must define exactly one positive bound.",
			)}
		}
		desired := ring.MaxRepositories
		if usesPercent {
			desired = int(math.Ceil(float64(len(targets)) * ring.CumulativePercent / 100))
		}
		if desired > len(targets) {
			desired = len(targets)
		}
		if desired <= assigned {
			continue
		}
		repositories := append([]string(nil), targets[assigned:desired]...)
		sort.Strings(repositories)
		waves = append(waves, Wave{
			ID: ring.ID, Ordinal: len(waves), RepositoryIDs: repositories, Status: "pending",
		})
		assigned = desired
	}
	if assigned != len(targets) {
		return nil, []domain.Finding{finding(
			"GDS_ROLLOUT_TARGETS_UNASSIGNED", "Rollout rings do not cover the complete target set.",
		)}
	}
	return waves, nil
}

func deterministicOrder(rolloutID string, repositories []string) []string {
	type ranked struct {
		id   string
		hash [32]byte
	}
	values := make([]ranked, 0, len(repositories))
	for _, repositoryID := range repositories {
		values = append(values, ranked{
			id: repositoryID, hash: sha256.Sum256([]byte(rolloutID + "\x00" + repositoryID)),
		})
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].hash != values[right].hash {
			return string(values[left].hash[:]) < string(values[right].hash[:])
		}
		return values[left].id < values[right].id
	})
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.id
	}
	return result
}

func Validate(plan Plan, schemas *validation.Set) []domain.Finding {
	raw, err := json.Marshal(plan)
	if err != nil {
		return []domain.Finding{finding("GDS_ROLLOUT_ENCODE_FAILED", err.Error())}
	}
	value, err := serialization.Decode("rollout.json", raw)
	if err != nil {
		return []domain.Finding{finding("GDS_ROLLOUT_DECODE_FAILED", err.Error())}
	}
	findings := schemas.Validate("rollout", value, "in-memory-rollout")
	if len(findings) != 0 {
		return findings
	}
	object := value.(map[string]any)
	expectedDigest, err := canonicaljson.DigestObjectWithoutField(object, "plan_digest")
	if err != nil || expectedDigest != plan.PlanDigest {
		return []domain.Finding{finding("GDS_ROLLOUT_PLAN_DIGEST_MISMATCH", "Rollout plan digest is invalid.")}
	}
	seen := map[string]struct{}{}
	allTargets := []string{}
	for ordinal, wave := range plan.Waves {
		if wave.Ordinal != ordinal {
			findings = append(findings, finding("GDS_ROLLOUT_WAVE_ORDER_INVALID", "Rollout wave ordinals must be contiguous."))
		}
		for _, repositoryID := range wave.RepositoryIDs {
			if _, duplicate := seen[repositoryID]; duplicate {
				findings = append(findings, finding("GDS_ROLLOUT_DUPLICATE_TARGET", "A rollout target occurs in multiple waves."))
			}
			seen[repositoryID] = struct{}{}
			allTargets = append(allTargets, repositoryID)
		}
	}
	sort.Strings(allTargets)
	targetDigest, _ := canonicaljson.Digest(allTargets)
	if len(allTargets) != plan.TargetCount || targetDigest != plan.TargetSetDigest {
		findings = append(findings, finding("GDS_ROLLOUT_TARGET_SET_MISMATCH", "Rollout waves do not match the bound target set."))
	}
	return findings
}

func planDigest(plan Plan) (string, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	value, err := serialization.Decode("rollout.json", raw)
	if err != nil {
		return "", err
	}
	return canonicaljson.DigestObjectWithoutField(value.(map[string]any), "plan_digest")
}

func finding(code, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
