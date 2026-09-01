package rollout

import (
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type Request struct {
	SchemaVersion int           `json:"schema_version"`
	RolloutID     string        `json:"rollout_id"`
	CreatedAt     time.Time     `json:"created_at"`
	Bundle        RequestBundle `json:"bundle"`
	RepositoryIDs []string      `json:"repository_ids"`
	Rings         []RingSpec    `json:"rings"`
	Gates         RequestGates  `json:"gates"`
	// AutoMerge lets a rollout hand its pull requests to GitHub auto-merge so
	// they land the moment their required checks pass. The checks stay exactly
	// as strict; only the wait for someone to press merge is removed.
	AutoMerge bool `json:"auto_merge"`
}

type RequestBundle struct {
	Version         string `json:"version"`
	ReleaseSequence int    `json:"release_sequence"`
	Channel         string `json:"channel"`
	ArtifactDigest  string `json:"artifact_digest"`
	ManifestDigest  string `json:"manifest_digest"`
}

type RequestGates struct {
	MaxFailureRate float64 `json:"max_failure_rate"`
}

func BuildRequest(request Request, schemas *validation.Set) (Plan, []domain.Finding) {
	return Build(BuildInput{
		RolloutID: request.RolloutID,
		CreatedAt: request.CreatedAt,
		Envelope: bundle.ReleaseEnvelope{
			SchemaVersion:   domain.SchemaVersion,
			BundleVersion:   request.Bundle.Version,
			ReleaseSequence: request.Bundle.ReleaseSequence,
			Channel:         request.Bundle.Channel,
			ArtifactDigest:  request.Bundle.ArtifactDigest,
			ManifestDigest:  request.Bundle.ManifestDigest,
		},
		RepositoryIDs:  request.RepositoryIDs,
		Rings:          request.Rings,
		MaxFailureRate: request.Gates.MaxFailureRate,
		AutoMerge:      request.AutoMerge,
	}, schemas)
}
