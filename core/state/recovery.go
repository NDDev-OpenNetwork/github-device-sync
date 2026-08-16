package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
)

type RecoveryStepEvidence struct {
	StepID         string `json:"step_id"`
	RepositoryID   string `json:"repository_id"`
	Action         string `json:"action"`
	IdempotencyKey string `json:"idempotency_key"`
	Sequence       int    `json:"sequence"`
	Status         string `json:"status"`
}

type RecoveryEventEvidence struct {
	Sequence      int64  `json:"sequence"`
	StepID        string `json:"step_id,omitempty"`
	EventType     string `json:"event_type"`
	PayloadDigest string `json:"payload_digest"`
}

type RecoverySnapshot struct {
	OperationID     string                  `json:"operation_id"`
	PlanID          string                  `json:"plan_id"`
	Operation       string                  `json:"operation"`
	OperationStatus string                  `json:"operation_status"`
	PlanStatus      string                  `json:"plan_status"`
	PlanDigest      string                  `json:"plan_digest"`
	Steps           []RecoveryStepEvidence  `json:"steps"`
	Events          []RecoveryEventEvidence `json:"events"`
	Locks           []Lock                  `json:"locks"`
	Digest          string                  `json:"digest"`
}

type RecoveryMutation struct {
	Expected            RecoverySnapshot `json:"expected"`
	Mode                string           `json:"mode"`
	Reason              string           `json:"reason"`
	NextOperationStatus string           `json:"next_operation_status,omitempty"`
	NextPlanStatus      string           `json:"next_plan_status,omitempty"`
	RecoveredAt         time.Time        `json:"recovered_at"`
}

func (store *Store) RecoverySnapshot(
	ctx context.Context,
	operationID string,
) (RecoverySnapshot, error) {
	return loadRecoverySnapshot(ctx, store.db, operationID)
}

func (store *Store) RecoverOperation(
	ctx context.Context,
	mutation RecoveryMutation,
) (RecoverySnapshot, error) {
	if store.readOnly {
		return RecoverySnapshot{}, ErrReadOnly
	}
	if mutation.Expected.OperationID == "" || mutation.Expected.Digest == "" ||
		mutation.Reason == "" || mutation.RecoveredAt.IsZero() {
		return RecoverySnapshot{}, errors.New("invalid recovery mutation")
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return RecoverySnapshot{}, fmt.Errorf("begin operation recovery: %w", err)
	}
	defer transaction.Rollback()
	current, err := loadRecoverySnapshot(
		ctx, transactionRecoveryReader{transaction: transaction}, mutation.Expected.OperationID,
	)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	if current.Digest != mutation.Expected.Digest {
		return RecoverySnapshot{}, ErrStateConflict
	}
	if len(current.Locks) == 0 {
		return RecoverySnapshot{}, errors.New("recovery requires an exact stale lock set")
	}
	for _, lock := range current.Locks {
		if !lock.LeaseExpiresAt.Before(mutation.RecoveredAt) {
			return RecoverySnapshot{}, ErrStateConflict
		}
	}
	for _, step := range current.Steps {
		if step.Status == "applying" {
			return RecoverySnapshot{}, errors.New("cannot automate recovery with unknown applying-step side effects")
		}
	}
	if err := applyRecoveryStatuses(ctx, transaction, current, mutation); err != nil {
		return RecoverySnapshot{}, err
	}
	for _, lock := range current.Locks {
		result, err := transaction.ExecContext(
			ctx,
			`DELETE FROM locks
			 WHERE scope = ? AND scope_id = ? AND lock_id = ? AND operation_id = ?
			   AND fencing_token = ? AND lease_expires_at = ?`,
			lock.Scope, lock.ScopeID, lock.LockID, lock.OperationID,
			lock.FencingToken, formatTime(lock.LeaseExpiresAt),
		)
		if err != nil {
			return RecoverySnapshot{}, fmt.Errorf("release exact stale recovery lock: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return RecoverySnapshot{}, ErrStateConflict
		}
	}
	payload := map[string]any{
		"mode": mutation.Mode, "reason": mutation.Reason,
		"expected_state_digest": current.Digest, "locks_released": len(current.Locks),
		"next_operation_status": mutation.NextOperationStatus,
	}
	if err := appendRecoveryEvent(ctx, transaction, current, mutation.RecoveredAt, payload); err != nil {
		return RecoverySnapshot{}, err
	}
	if err := store.commit(transaction); err != nil {
		return RecoverySnapshot{}, fmt.Errorf("commit operation recovery: %w", err)
	}
	return store.RecoverySnapshot(ctx, current.OperationID)
}

type recoveryReader interface {
	QueryContext(context.Context, string, ...any) (rowsScanner, error)
	QueryRowContext(context.Context, string, ...any) rowScanner
}

type rowsScanner interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type transactionRecoveryReader struct{ transaction *sql.Tx }

func (reader transactionRecoveryReader) QueryContext(
	ctx context.Context, query string, arguments ...any,
) (rowsScanner, error) {
	return reader.transaction.QueryContext(ctx, query, arguments...)
}

func (reader transactionRecoveryReader) QueryRowContext(
	ctx context.Context, query string, arguments ...any,
) rowScanner {
	return reader.transaction.QueryRowContext(ctx, query, arguments...)
}

func loadRecoverySnapshot(
	ctx context.Context,
	reader recoveryReader,
	operationID string,
) (RecoverySnapshot, error) {
	var snapshot RecoverySnapshot
	err := reader.QueryRowContext(
		ctx,
		`SELECT o.operation_id, o.plan_id, o.operation, o.status,
		        p.status, p.plan_digest
		 FROM operations o JOIN plans p ON p.plan_id = o.plan_id
		 WHERE o.operation_id = ?`,
		operationID,
	).Scan(
		&snapshot.OperationID, &snapshot.PlanID, &snapshot.Operation,
		&snapshot.OperationStatus, &snapshot.PlanStatus, &snapshot.PlanDigest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RecoverySnapshot{}, ErrNotFound
	}
	if err != nil {
		return RecoverySnapshot{}, fmt.Errorf("load recovery operation identity: %w", err)
	}
	stepRows, err := reader.QueryContext(
		ctx,
		`SELECT step_id, repository_id, action, idempotency_key, sequence, status
		 FROM operation_steps WHERE operation_id = ? ORDER BY sequence`,
		operationID,
	)
	if err != nil {
		return RecoverySnapshot{}, fmt.Errorf("load recovery steps: %w", err)
	}
	for stepRows.Next() {
		var step RecoveryStepEvidence
		if err := stepRows.Scan(
			&step.StepID, &step.RepositoryID, &step.Action, &step.IdempotencyKey,
			&step.Sequence, &step.Status,
		); err != nil {
			stepRows.Close()
			return RecoverySnapshot{}, fmt.Errorf("decode recovery step: %w", err)
		}
		snapshot.Steps = append(snapshot.Steps, step)
	}
	if err := stepRows.Close(); err != nil {
		return RecoverySnapshot{}, err
	}
	eventRows, err := reader.QueryContext(
		ctx,
		`SELECT sequence, COALESCE(step_id, ''), event_type, payload_digest
		 FROM operation_events WHERE operation_id = ? ORDER BY sequence`,
		operationID,
	)
	if err != nil {
		return RecoverySnapshot{}, fmt.Errorf("load recovery events: %w", err)
	}
	for eventRows.Next() {
		var event RecoveryEventEvidence
		if err := eventRows.Scan(
			&event.Sequence, &event.StepID, &event.EventType, &event.PayloadDigest,
		); err != nil {
			eventRows.Close()
			return RecoverySnapshot{}, fmt.Errorf("decode recovery event: %w", err)
		}
		snapshot.Events = append(snapshot.Events, event)
	}
	if err := eventRows.Close(); err != nil {
		return RecoverySnapshot{}, err
	}
	lockRows, err := reader.QueryContext(
		ctx,
		`SELECT scope, scope_id, lock_id, operation_id, device_id, session_id, pid,
		        fencing_token, acquired_at, lease_expires_at, heartbeat_at
		 FROM locks WHERE operation_id = ? ORDER BY scope, scope_id`,
		operationID,
	)
	if err != nil {
		return RecoverySnapshot{}, fmt.Errorf("load recovery locks: %w", err)
	}
	for lockRows.Next() {
		var lock Lock
		var acquiredAt, leaseExpiresAt, heartbeatAt string
		if err := lockRows.Scan(
			&lock.Scope, &lock.ScopeID, &lock.LockID, &lock.OperationID,
			&lock.DeviceID, &lock.SessionID, &lock.PID, &lock.FencingToken,
			&acquiredAt, &leaseExpiresAt, &heartbeatAt,
		); err != nil {
			lockRows.Close()
			return RecoverySnapshot{}, fmt.Errorf("decode recovery lock: %w", err)
		}
		lock.AcquiredAt, err = time.Parse(time.RFC3339Nano, acquiredAt)
		if err == nil {
			lock.LeaseExpiresAt, err = time.Parse(time.RFC3339Nano, leaseExpiresAt)
		}
		if err == nil {
			lock.HeartbeatAt, err = time.Parse(time.RFC3339Nano, heartbeatAt)
		}
		if err != nil {
			lockRows.Close()
			return RecoverySnapshot{}, fmt.Errorf("decode recovery lock timestamps: %w", err)
		}
		snapshot.Locks = append(snapshot.Locks, lock)
	}
	if err := lockRows.Close(); err != nil {
		return RecoverySnapshot{}, err
	}
	digest, err := digestRecoverySnapshot(snapshot)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	snapshot.Digest = digest
	return snapshot, nil
}

func digestRecoverySnapshot(snapshot RecoverySnapshot) (string, error) {
	snapshot.Digest = ""
	return canonicaljson.Digest(snapshot)
}

func applyRecoveryStatuses(
	ctx context.Context,
	transaction *sql.Tx,
	current RecoverySnapshot,
	mutation RecoveryMutation,
) error {
	switch mutation.Mode {
	case "abort-interrupted":
		if current.OperationStatus != "applying" || mutation.NextOperationStatus != "failed" ||
			mutation.NextPlanStatus != "failed" {
			return errors.New("invalid abort-interrupted recovery transition")
		}
		for _, step := range current.Steps {
			if step.Status == "succeeded" || step.Status == "failed" {
				return errors.New("abort-interrupted recovery cannot discard completed or failed steps")
			}
		}
	case "close-partial":
		if current.OperationStatus != "applying" || mutation.NextOperationStatus != "partial" ||
			mutation.NextPlanStatus != "partial" {
			return errors.New("invalid close-partial recovery transition")
		}
	case "release-stale-locks":
		if current.OperationStatus == "applying" || mutation.NextOperationStatus != "" ||
			mutation.NextPlanStatus != "" {
			return errors.New("release-stale-locks requires an already-terminal operation")
		}
	default:
		return fmt.Errorf("unsupported recovery mode %q", mutation.Mode)
	}
	if mutation.Mode == "abort-interrupted" || mutation.Mode == "close-partial" {
		for _, step := range current.Steps {
			if step.Status != "pending" {
				continue
			}
			result, err := transaction.ExecContext(
				ctx,
				`UPDATE operation_steps
				 SET status = 'blocked', last_error = ?, finished_at = ?
				 WHERE operation_id = ? AND step_id = ? AND status = 'pending'`,
				mutation.Reason, formatTime(mutation.RecoveredAt),
				current.OperationID, step.StepID,
			)
			if err != nil {
				return fmt.Errorf("block interrupted recovery step: %w", err)
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return ErrStateConflict
			}
		}
		resultJSON, err := json.Marshal(map[string]any{
			"recovery_mode": mutation.Mode, "reason": mutation.Reason,
			"source_state_digest": current.Digest,
		})
		if err != nil {
			return err
		}
		result, err := transaction.ExecContext(
			ctx,
			`UPDATE operations SET status = ?, finished_at = ?, result_json = ?
			 WHERE operation_id = ? AND status = 'applying'`,
			mutation.NextOperationStatus, formatTime(mutation.RecoveredAt), resultJSON,
			current.OperationID,
		)
		if err != nil {
			return fmt.Errorf("close interrupted operation: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrStateConflict
		}
		result, err = transaction.ExecContext(
			ctx, `UPDATE plans SET status = ? WHERE plan_id = ? AND status = ?`,
			mutation.NextPlanStatus, current.PlanID, current.PlanStatus,
		)
		if err != nil {
			return fmt.Errorf("close interrupted plan: %w", err)
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrStateConflict
		}
	}
	return nil
}

func appendRecoveryEvent(
	ctx context.Context,
	transaction *sql.Tx,
	snapshot RecoverySnapshot,
	occurredAt time.Time,
	payload any,
) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode recovery event: %w", err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO operation_events(
		    operation_id, plan_id, step_id, event_type, occurred_at,
		    payload_json, payload_digest
		 ) VALUES (?, ?, NULL, 'operation-recovered', ?, ?, ?)`,
		snapshot.OperationID, snapshot.PlanID, formatTime(occurredAt), raw, digest,
	); err != nil {
		return fmt.Errorf("append operation recovery event: %w", err)
	}
	return nil
}
