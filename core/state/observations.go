package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RepositoryObservation struct {
	InstallationID       string          `json:"installation_id"`
	ProviderRepositoryID int64           `json:"provider_repository_id"`
	Owner                string          `json:"owner"`
	Name                 string          `json:"name"`
	AccessState          string          `json:"access_state"`
	ObservedAt           time.Time       `json:"observed_at"`
	ETag                 string          `json:"etag,omitempty"`
	Body                 json.RawMessage `json:"body,omitempty"`
	BodyDigest           string          `json:"body_digest,omitempty"`
	RequestID            string          `json:"request_id,omitempty"`
}

func (store *Store) PutRepositoryObservation(
	ctx context.Context,
	observation RepositoryObservation,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if err := validateObservation(observation); err != nil {
		return err
	}
	if len(observation.Body) != 0 {
		observation.BodyDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(observation.Body))
	}
	existing, err := store.GetRepositoryObservation(
		ctx, observation.InstallationID, observation.ProviderRepositoryID,
	)
	if err == nil {
		if observation.ObservedAt.Before(existing.ObservedAt) {
			return ErrStateConflict
		}
		if observation.ObservedAt.Equal(existing.ObservedAt) {
			if observation.AccessState == existing.AccessState && observation.Owner == existing.Owner &&
				observation.Name == existing.Name && observation.ETag == existing.ETag &&
				observation.RequestID == existing.RequestID && bytes.Equal(observation.Body, existing.Body) {
				return nil
			}
			return ErrStateConflict
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	_, err = store.db.ExecContext(
		ctx,
		`INSERT INTO repository_observations(
            installation_id, provider_repository_id, owner, name, access_state,
            observed_at, etag, body_json, body_digest, request_id
         ) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''))
         ON CONFLICT(installation_id, provider_repository_id) DO UPDATE SET
            owner = excluded.owner,
            name = excluded.name,
            access_state = excluded.access_state,
            observed_at = excluded.observed_at,
            etag = excluded.etag,
            body_json = excluded.body_json,
            body_digest = excluded.body_digest,
            request_id = excluded.request_id`,
		observation.InstallationID, observation.ProviderRepositoryID,
		observation.Owner, observation.Name, observation.AccessState,
		formatTime(observation.ObservedAt), observation.ETag, nullableBytes(observation.Body),
		observation.BodyDigest, observation.RequestID,
	)
	if err != nil {
		return fmt.Errorf("store repository observation: %w", err)
	}
	return nil
}

func (store *Store) GetRepositoryObservation(
	ctx context.Context,
	installationID string,
	providerRepositoryID int64,
) (RepositoryObservation, error) {
	var observation RepositoryObservation
	var observedAt string
	var etag, body, bodyDigest, requestID []byte
	err := store.db.QueryRowContext(
		ctx,
		`SELECT installation_id, provider_repository_id, owner, name, access_state,
                observed_at, COALESCE(etag, ''), body_json,
                COALESCE(body_digest, ''), COALESCE(request_id, '')
         FROM repository_observations
         WHERE installation_id = ? AND provider_repository_id = ?`,
		installationID, providerRepositoryID,
	).Scan(
		&observation.InstallationID, &observation.ProviderRepositoryID,
		&observation.Owner, &observation.Name, &observation.AccessState,
		&observedAt, &etag, &body, &bodyDigest, &requestID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryObservation{}, ErrNotFound
	}
	if err != nil {
		return RepositoryObservation{}, fmt.Errorf("load repository observation: %w", err)
	}
	observation.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return RepositoryObservation{}, err
	}
	observation.ETag = string(etag)
	observation.Body = append(json.RawMessage(nil), body...)
	observation.BodyDigest = string(bodyDigest)
	observation.RequestID = string(requestID)
	return observation, nil
}

func validateObservation(observation RepositoryObservation) error {
	if observation.InstallationID == "" || observation.ProviderRepositoryID <= 0 ||
		observation.Owner == "" || observation.Name == "" || observation.ObservedAt.IsZero() {
		return fmt.Errorf("invalid repository observation identity")
	}
	if strings.ContainsAny(
		observation.InstallationID+observation.Owner+observation.Name+
			observation.ETag+observation.RequestID,
		"\x00\r\n",
	) || len(observation.ETag) > 512 || len(observation.RequestID) > 256 ||
		len(observation.Body) > 8<<20 {
		return fmt.Errorf("repository observation exceeds safe text or body bounds")
	}
	switch observation.AccessState {
	case "available":
		if !json.Valid(observation.Body) {
			return fmt.Errorf("available repository observation requires valid JSON body")
		}
	case "inaccessible", "auth-failed", "not-found", "unknown":
		if len(observation.Body) != 0 {
			return fmt.Errorf("unavailable repository observation must not persist a provider body")
		}
	default:
		return fmt.Errorf("invalid repository access state %q", observation.AccessState)
	}
	return nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
