package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/rollout"
)

type AcceptedBundle struct {
	TrustDomain               string    `json:"trust_domain"`
	ReleaseSequence           int       `json:"release_sequence"`
	BundleVersion             string    `json:"bundle_version"`
	ArtifactDigest            string    `json:"artifact_digest"`
	ManifestDigest            string    `json:"manifest_digest"`
	AttestationIdentityDigest string    `json:"attestation_identity_digest"`
	AcceptedAt                time.Time `json:"accepted_at"`
}

type RolloutSnapshot struct {
	RolloutID       string          `json:"rollout_id"`
	Plan            json.RawMessage `json:"plan"`
	PlanDigest      string          `json:"plan_digest"`
	TargetSetDigest string          `json:"target_set_digest"`
	BundleSequence  int             `json:"bundle_sequence"`
	BundleDigest    string          `json:"bundle_digest"`
	Status          string          `json:"status"`
	CurrentWave     int             `json:"current_wave"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type RolloutTarget struct {
	RolloutID    string          `json:"rollout_id"`
	WaveOrdinal  int             `json:"wave_ordinal"`
	RepositoryID string          `json:"repository_id"`
	Status       string          `json:"status"`
	Result       json.RawMessage `json:"result,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func (store *Store) PutAcceptedBundle(
	ctx context.Context,
	record AcceptedBundle,
	authorization *bundle.RollbackAuthorization,
	now time.Time,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if record.TrustDomain == "" || record.ReleaseSequence < 1 || record.BundleVersion == "" ||
		record.ArtifactDigest == "" || record.ManifestDigest == "" ||
		record.AttestationIdentityDigest == "" || record.AcceptedAt.IsZero() {
		return fmt.Errorf("invalid accepted bundle record")
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin bundle acceptance: %w", err)
	}
	defer transaction.Rollback()

	authorizationRaw, err := json.Marshal(authorization)
	if err != nil {
		return fmt.Errorf("encode rollback authorization: %w", err)
	}
	if authorization == nil {
		authorizationRaw = nil
	}
	var existing AcceptedBundle
	var acceptedAt string
	var existingAuthorization []byte
	var existingApproval string
	err = transaction.QueryRowContext(
		ctx,
		`SELECT trust_domain, release_sequence, bundle_version, artifact_digest,
                manifest_digest, attestation_identity_digest, accepted_at,
                COALESCE(rollback_approval_ref, ''), rollback_authorization_json
         FROM accepted_bundles WHERE trust_domain = ? AND release_sequence = ?`,
		record.TrustDomain, record.ReleaseSequence,
	).Scan(
		&existing.TrustDomain, &existing.ReleaseSequence, &existing.BundleVersion,
		&existing.ArtifactDigest, &existing.ManifestDigest,
		&existing.AttestationIdentityDigest, &acceptedAt, &existingApproval,
		&existingAuthorization,
	)
	if err == nil {
		existing.AcceptedAt, err = time.Parse(time.RFC3339Nano, acceptedAt)
		if err != nil {
			return err
		}
		if existing.TrustDomain != record.TrustDomain ||
			existing.ReleaseSequence != record.ReleaseSequence ||
			existing.BundleVersion != record.BundleVersion ||
			existing.ArtifactDigest != record.ArtifactDigest ||
			existing.ManifestDigest != record.ManifestDigest ||
			existing.AttestationIdentityDigest != record.AttestationIdentityDigest ||
			!existing.AcceptedAt.Equal(record.AcceptedAt) ||
			existingApproval != rollbackApprovalRef(authorization) ||
			!bytes.Equal(existingAuthorization, authorizationRaw) {
			return ErrBundleConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect accepted bundle: %w", err)
	}

	var highest int
	if err := transaction.QueryRowContext(
		ctx, `SELECT COALESCE(MAX(release_sequence), 0) FROM accepted_bundles WHERE trust_domain = ?`,
		record.TrustDomain,
	).Scan(&highest); err != nil {
		return fmt.Errorf("read bundle acceptance floor: %w", err)
	}
	if record.ReleaseSequence < highest {
		if !validStoredRollback(record, authorization, now) {
			return ErrRollbackBlocked
		}
	} else if authorization != nil {
		return fmt.Errorf("rollback authorization is only valid for a sequence downgrade")
	}
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO accepted_bundles(
            trust_domain, release_sequence, bundle_version, artifact_digest,
            manifest_digest, attestation_identity_digest, accepted_at,
            rollback_approval_ref, rollback_authorization_json
         ) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)`,
		record.TrustDomain, record.ReleaseSequence, record.BundleVersion,
		record.ArtifactDigest, record.ManifestDigest, record.AttestationIdentityDigest,
		formatTime(record.AcceptedAt), rollbackApprovalRef(authorization), authorizationRaw,
	); err != nil {
		return fmt.Errorf("record accepted bundle: %w", err)
	}
	if err := store.commit(transaction); err != nil {
		return fmt.Errorf("commit bundle acceptance: %w", err)
	}
	return nil
}

func validStoredRollback(
	record AcceptedBundle,
	authorization *bundle.RollbackAuthorization,
	now time.Time,
) bool {
	return authorization != nil && authorization.TargetSequence == record.ReleaseSequence &&
		authorization.TargetDigest == record.ArtifactDigest &&
		authorization.RolloutID != "" && authorization.ScopeDigest != "" &&
		authorization.Reason != "" && authorization.ApprovalRef != "" &&
		authorization.ExpiresAt.After(now)
}

func rollbackApprovalRef(authorization *bundle.RollbackAuthorization) string {
	if authorization == nil {
		return ""
	}
	return authorization.ApprovalRef
}

func (store *Store) BundleAcceptanceState(
	ctx context.Context,
	trustDomain string,
) (bundle.AcceptanceState, error) {
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT release_sequence, artifact_digest FROM accepted_bundles
         WHERE trust_domain = ? ORDER BY release_sequence`,
		trustDomain,
	)
	if err != nil {
		return bundle.AcceptanceState{}, fmt.Errorf("read bundle acceptance state: %w", err)
	}
	defer rows.Close()
	result := bundle.AcceptanceState{AcceptedDigests: map[int]string{}}
	for rows.Next() {
		var sequence int
		var digest string
		if err := rows.Scan(&sequence, &digest); err != nil {
			return bundle.AcceptanceState{}, err
		}
		result.AcceptedDigests[sequence] = digest
		if sequence > result.HighestSequence {
			result.HighestSequence = sequence
		}
	}
	return result, rows.Err()
}

func (store *Store) PutRollout(ctx context.Context, plan rollout.Plan) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if plan.RolloutID == "" || plan.PlanDigest == "" || plan.TargetCount < 1 || plan.CreatedAt.IsZero() {
		return fmt.Errorf("invalid rollout plan")
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("encode rollout plan: %w", err)
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rollout insert: %w", err)
	}
	defer transaction.Rollback()
	var existingDigest string
	var existingRaw []byte
	err = transaction.QueryRowContext(
		ctx, `SELECT plan_digest, plan_json FROM rollouts WHERE rollout_id = ?`, plan.RolloutID,
	).Scan(&existingDigest, &existingRaw)
	if err == nil {
		if existingDigest != plan.PlanDigest || !bytes.Equal(existingRaw, raw) {
			return ErrRolloutConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect rollout identity: %w", err)
	}
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO rollouts(
            rollout_id, plan_json, plan_digest, target_set_digest, bundle_sequence,
            bundle_digest, status, current_wave, created_at, updated_at
         ) VALUES (?, ?, ?, ?, ?, ?, 'planned', -1, ?, ?)`,
		plan.RolloutID, raw, plan.PlanDigest, plan.TargetSetDigest,
		plan.Bundle.ReleaseSequence, plan.Bundle.ArtifactDigest,
		formatTime(plan.CreatedAt), formatTime(plan.CreatedAt),
	); err != nil {
		return fmt.Errorf("insert rollout: %w", err)
	}
	for _, wave := range plan.Waves {
		for _, repositoryID := range wave.RepositoryIDs {
			if _, err := transaction.ExecContext(
				ctx,
				`INSERT INTO rollout_targets(
                    rollout_id, wave_ordinal, repository_id, status, updated_at
                 ) VALUES (?, ?, ?, 'pending', ?)`,
				plan.RolloutID, wave.Ordinal, repositoryID, formatTime(plan.CreatedAt),
			); err != nil {
				return fmt.Errorf("insert rollout target: %w", err)
			}
		}
	}
	if err := appendRolloutEvent(
		ctx, transaction, plan.RolloutID, nil, "", "rollout-planned", plan.CreatedAt,
		map[string]any{"plan_digest": plan.PlanDigest, "target_count": plan.TargetCount},
	); err != nil {
		return err
	}
	if err := store.commit(transaction); err != nil {
		return fmt.Errorf("commit rollout: %w", err)
	}
	return nil
}

func (store *Store) GetRollout(ctx context.Context, rolloutID string) (RolloutSnapshot, error) {
	var result RolloutSnapshot
	var createdAt, updatedAt string
	err := store.db.QueryRowContext(
		ctx,
		`SELECT rollout_id, plan_json, plan_digest, target_set_digest,
                bundle_sequence, bundle_digest, status, current_wave, created_at, updated_at
         FROM rollouts WHERE rollout_id = ?`,
		rolloutID,
	).Scan(
		&result.RolloutID, &result.Plan, &result.PlanDigest, &result.TargetSetDigest,
		&result.BundleSequence, &result.BundleDigest, &result.Status, &result.CurrentWave,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RolloutSnapshot{}, ErrNotFound
	}
	if err != nil {
		return RolloutSnapshot{}, fmt.Errorf("load rollout: %w", err)
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err == nil {
		result.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	}
	return result, err
}

func (store *Store) TransitionRollout(
	ctx context.Context,
	rolloutID string,
	expectedStatus string,
	nextStatus string,
	currentWave int,
	now time.Time,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	var previousWave int
	if err := transaction.QueryRowContext(
		ctx, `SELECT current_wave FROM rollouts WHERE rollout_id = ? AND status = ?`,
		rolloutID, expectedStatus,
	).Scan(&previousWave); errors.Is(err, sql.ErrNoRows) {
		return ErrStateConflict
	} else if err != nil {
		return err
	}
	if !validRolloutTransition(expectedStatus, nextStatus, previousWave, currentWave) {
		return ErrStateConflict
	}
	var finalWave int
	if err := transaction.QueryRowContext(
		ctx, `SELECT COALESCE(MAX(wave_ordinal), -1) FROM rollout_targets WHERE rollout_id = ?`,
		rolloutID,
	).Scan(&finalWave); err != nil {
		return err
	}
	if (nextStatus == "active" && currentWave > finalWave) ||
		(nextStatus == "completed" && previousWave != finalWave) {
		return ErrStateConflict
	}
	if expectedStatus == "active" && (nextStatus == "active" || nextStatus == "completed") {
		var incomplete int
		if err := transaction.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM rollout_targets
             WHERE rollout_id = ? AND wave_ordinal = ? AND status != 'succeeded'`,
			rolloutID, previousWave,
		).Scan(&incomplete); err != nil {
			return err
		}
		if incomplete != 0 {
			return ErrStateConflict
		}
	}
	result, err := transaction.ExecContext(
		ctx,
		`UPDATE rollouts SET status = ?, current_wave = ?, updated_at = ?
         WHERE rollout_id = ? AND status = ? AND current_wave = ?`,
		nextStatus, currentWave, formatTime(now), rolloutID, expectedStatus, previousWave,
	)
	if err != nil {
		return fmt.Errorf("transition rollout: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrStateConflict
	}
	wave := currentWave
	if err := appendRolloutEvent(
		ctx, transaction, rolloutID, &wave, "", "rollout-transitioned", now,
		map[string]any{"from": expectedStatus, "to": nextStatus},
	); err != nil {
		return err
	}
	return store.commit(transaction)
}

func (store *Store) TransitionRolloutTarget(
	ctx context.Context,
	rolloutID string,
	repositoryID string,
	expectedStatus string,
	nextStatus string,
	now time.Time,
	payload any,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode rollout target result: %w", err)
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if !validRolloutTargetTransition(expectedStatus, nextStatus) {
		return ErrStateConflict
	}
	var wave, activeWave int
	var rolloutStatus string
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT target.wave_ordinal, rollout.status, rollout.current_wave
         FROM rollout_targets AS target
         JOIN rollouts AS rollout ON rollout.rollout_id = target.rollout_id
         WHERE target.rollout_id = ? AND target.repository_id = ? AND target.status = ?`,
		rolloutID, repositoryID, expectedStatus,
	).Scan(&wave, &rolloutStatus, &activeWave); errors.Is(err, sql.ErrNoRows) {
		return ErrStateConflict
	} else if err != nil {
		return err
	}
	if nextStatus == "rolled-back" {
		if rolloutStatus != "rolling-back" {
			return ErrStateConflict
		}
	} else if rolloutStatus != "active" || activeWave != wave {
		return ErrStateConflict
	}
	result, err := transaction.ExecContext(
		ctx,
		`UPDATE rollout_targets SET status = ?, result_json = ?, updated_at = ?
         WHERE rollout_id = ? AND repository_id = ? AND status = ?`,
		nextStatus, raw, formatTime(now), rolloutID, repositoryID, expectedStatus,
	)
	if err != nil {
		return fmt.Errorf("transition rollout target: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrStateConflict
	}
	if err := appendRolloutEvent(
		ctx, transaction, rolloutID, &wave, repositoryID, "target-transitioned", now,
		map[string]any{"from": expectedStatus, "to": nextStatus, "result": payload},
	); err != nil {
		return err
	}
	return store.commit(transaction)
}

func validRolloutTransition(from, to string, previousWave, nextWave int) bool {
	switch {
	case from == "planned" && to == "active":
		return previousWave == -1 && nextWave == 0
	case from == "planned" && to == "failed":
		return previousWave == -1 && nextWave == -1
	case from == "active" && to == "active":
		return nextWave == previousWave+1
	case from == "active" && (to == "paused" || to == "failed" || to == "completed" || to == "rolling-back"):
		return nextWave == previousWave
	case from == "paused" && (to == "active" || to == "failed" || to == "rolling-back"):
		return nextWave == previousWave
	case from == "rolling-back" && (to == "rolled-back" || to == "failed"):
		return nextWave == previousWave
	default:
		return false
	}
}

func validRolloutTargetTransition(from, to string) bool {
	switch from {
	case "pending":
		return to == "active" || to == "quarantined"
	case "active":
		return to == "pull-request-open" || to == "checks-pending" || to == "succeeded" ||
			to == "failed" || to == "quarantined"
	case "pull-request-open":
		return to == "checks-pending" || to == "succeeded" || to == "failed" || to == "quarantined"
	case "checks-pending":
		return to == "succeeded" || to == "failed" || to == "quarantined"
	case "failed":
		return to == "active" || to == "quarantined"
	case "succeeded":
		return to == "rolled-back"
	default:
		return false
	}
}

func (store *Store) ListRolloutTargets(
	ctx context.Context,
	rolloutID string,
	waveOrdinal int,
) ([]RolloutTarget, error) {
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT rollout_id, wave_ordinal, repository_id, status, result_json, updated_at
         FROM rollout_targets WHERE rollout_id = ? AND wave_ordinal = ?
         ORDER BY repository_id`,
		rolloutID, waveOrdinal,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []RolloutTarget{}
	for rows.Next() {
		var target RolloutTarget
		var raw []byte
		var updatedAt string
		if err := rows.Scan(
			&target.RolloutID, &target.WaveOrdinal, &target.RepositoryID,
			&target.Status, &raw, &updatedAt,
		); err != nil {
			return nil, err
		}
		target.Result = append(json.RawMessage(nil), raw...)
		target.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, err
		}
		results = append(results, target)
	}
	return results, rows.Err()
}

func appendRolloutEvent(
	ctx context.Context,
	transaction *sql.Tx,
	rolloutID string,
	waveOrdinal *int,
	repositoryID string,
	eventType string,
	occurredAt time.Time,
	payload any,
) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode rollout event: %w", err)
	}
	var wave any
	if waveOrdinal != nil {
		wave = *waveOrdinal
	}
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO rollout_events(
            rollout_id, wave_ordinal, repository_id, event_type, occurred_at, body_json
         ) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?)`,
		rolloutID, wave, repositoryID, eventType, formatTime(occurredAt), raw,
	); err != nil {
		return fmt.Errorf("append rollout event: %w", err)
	}
	return nil
}
