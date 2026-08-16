package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type OperationRecord struct {
	OperationID string          `json:"operation_id"`
	PlanID      string          `json:"plan_id"`
	Operation   string          `json:"operation"`
	Status      string          `json:"status"`
	Actor       json.RawMessage `json:"actor"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

type StepRecord struct {
	OperationID    string          `json:"operation_id"`
	StepID         string          `json:"step_id"`
	RepositoryID   string          `json:"repository_id"`
	Action         string          `json:"action"`
	IdempotencyKey string          `json:"idempotency_key"`
	Sequence       int             `json:"sequence"`
	Status         string          `json:"status"`
	Before         json.RawMessage `json:"before,omitempty"`
	After          json.RawMessage `json:"after,omitempty"`
	LastError      string          `json:"last_error,omitempty"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
}

type EventRecord struct {
	Sequence      int64           `json:"sequence"`
	OperationID   string          `json:"operation_id"`
	PlanID        string          `json:"plan_id"`
	StepID        string          `json:"step_id,omitempty"`
	EventType     string          `json:"event_type"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
	PayloadDigest string          `json:"payload_digest"`
}

func (store *Store) StartOperation(
	ctx context.Context,
	operation OperationRecord,
	steps []StepRecord,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if err := validateOperationRecords(operation, steps); err != nil {
		return err
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin operation journal: %w", err)
	}
	defer transaction.Rollback()
	if err := insertOperationJournalTx(ctx, transaction, operation, steps); err != nil {
		return err
	}
	if err := store.commit(transaction); err != nil {
		return fmt.Errorf("commit operation journal: %w", err)
	}
	return nil
}

func validateOperationRecords(operation OperationRecord, steps []StepRecord) error {
	if operation.OperationID == "" || operation.PlanID == "" || operation.Operation == "" ||
		operation.Status != "applying" || operation.StartedAt.IsZero() || !json.Valid(operation.Actor) {
		return fmt.Errorf("invalid operation record")
	}
	for index, step := range steps {
		if step.OperationID != operation.OperationID || step.Sequence != index ||
			step.Status != "pending" || step.StepID == "" || step.RepositoryID == "" ||
			step.Action == "" || step.IdempotencyKey == "" {
			return fmt.Errorf("invalid operation step at index %d", index)
		}
	}
	return nil
}

func insertOperationJournalTx(
	ctx context.Context,
	transaction *sql.Tx,
	operation OperationRecord,
	steps []StepRecord,
) error {
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO operations(
            operation_id, plan_id, operation, status, actor_json, started_at
        ) VALUES (?, ?, ?, ?, ?, ?)`,
		operation.OperationID, operation.PlanID, operation.Operation, operation.Status,
		[]byte(operation.Actor), formatTime(operation.StartedAt),
	); err != nil {
		return fmt.Errorf("insert operation journal: %w", err)
	}
	for _, step := range steps {
		if _, err := transaction.ExecContext(
			ctx,
			`INSERT INTO operation_steps(
                operation_id, step_id, repository_id, action, idempotency_key,
                sequence, status
            ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			step.OperationID, step.StepID, step.RepositoryID, step.Action,
			step.IdempotencyKey, step.Sequence, step.Status,
		); err != nil {
			return fmt.Errorf("insert operation step %s: %w", step.StepID, err)
		}
	}
	if _, err := appendEventTx(
		ctx, transaction, operation.OperationID, operation.PlanID, "", "operation-started",
		operation.StartedAt, map[string]any{"operation": operation.Operation, "steps": len(steps)},
	); err != nil {
		return err
	}
	return nil
}

func (store *Store) AppendEvent(
	ctx context.Context,
	operationID string,
	planID string,
	stepID string,
	eventType string,
	occurredAt time.Time,
	payload any,
) (EventRecord, error) {
	if store.readOnly {
		return EventRecord{}, ErrReadOnly
	}
	return appendEventTx(
		ctx, store.db, operationID, planID, stepID, eventType, occurredAt, payload,
	)
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendEventTx(
	ctx context.Context,
	executor sqlExecutor,
	operationID string,
	planID string,
	stepID string,
	eventType string,
	occurredAt time.Time,
	payload any,
) (EventRecord, error) {
	if operationID == "" || planID == "" || eventType == "" {
		return EventRecord{}, fmt.Errorf("invalid journal event identity")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return EventRecord{}, fmt.Errorf("encode journal payload: %w", err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	result, err := executor.ExecContext(
		ctx,
		`INSERT INTO operation_events(
            operation_id, plan_id, step_id, event_type, occurred_at,
            payload_json, payload_digest
        ) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?)`,
		operationID, planID, stepID, eventType, formatTime(occurredAt), raw, digest,
	)
	if err != nil {
		return EventRecord{}, fmt.Errorf("append operation event: %w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return EventRecord{}, fmt.Errorf("read operation event sequence: %w", err)
	}
	return EventRecord{
		Sequence: sequence, OperationID: operationID, PlanID: planID, StepID: stepID,
		EventType: eventType, OccurredAt: occurredAt.UTC(), Payload: raw, PayloadDigest: digest,
	}, nil
}

func (store *Store) TransitionStep(
	ctx context.Context,
	operationID string,
	stepID string,
	expected string,
	next string,
	now time.Time,
	before any,
	after any,
	lastError string,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	beforeRaw, err := optionalJSON(before)
	if err != nil {
		return err
	}
	afterRaw, err := optionalJSON(after)
	if err != nil {
		return err
	}
	startedAt := any(nil)
	finishedAt := any(nil)
	if next == "applying" {
		startedAt = formatTime(now)
	}
	if next == "succeeded" || next == "failed" || next == "blocked" || next == "compensated" {
		finishedAt = formatTime(now)
	}
	result, err := store.db.ExecContext(
		ctx,
		`UPDATE operation_steps
         SET status = ?,
             before_json = COALESCE(?, before_json),
             after_json = COALESCE(?, after_json),
             last_error = NULLIF(?, ''),
             started_at = COALESCE(?, started_at),
             finished_at = CASE WHEN ? = 'compensating' THEN NULL ELSE COALESCE(?, finished_at) END
         WHERE operation_id = ? AND step_id = ? AND status = ?`,
		next, beforeRaw, afterRaw, lastError, startedAt, next, finishedAt,
		operationID, stepID, expected,
	)
	if err != nil {
		return fmt.Errorf("transition operation step: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect operation step transition: %w", err)
	}
	if changed != 1 {
		return ErrStateConflict
	}
	return nil
}

func (store *Store) FinishOperation(
	ctx context.Context,
	operationID string,
	expected string,
	next string,
	finishedAt time.Time,
	resultPayload any,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	raw, err := json.Marshal(resultPayload)
	if err != nil {
		return fmt.Errorf("encode operation result: %w", err)
	}
	result, err := store.db.ExecContext(
		ctx,
		`UPDATE operations SET status = ?, finished_at = ?, result_json = ?
         WHERE operation_id = ? AND status = ?`,
		next, formatTime(finishedAt), raw, operationID, expected,
	)
	if err != nil {
		return fmt.Errorf("finish operation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect operation transition: %w", err)
	}
	if changed != 1 {
		return ErrStateConflict
	}
	return nil
}

func (store *Store) GetOperation(ctx context.Context, operationID string) (OperationRecord, error) {
	var record OperationRecord
	var actor, result []byte
	var startedAt string
	var finishedAt sql.NullString
	err := store.db.QueryRowContext(
		ctx,
		`SELECT operation_id, plan_id, operation, status, actor_json,
                started_at, finished_at, result_json
         FROM operations WHERE operation_id = ?`,
		operationID,
	).Scan(
		&record.OperationID, &record.PlanID, &record.Operation, &record.Status, &actor,
		&startedAt, &finishedAt, &result,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OperationRecord{}, ErrNotFound
	}
	if err != nil {
		return OperationRecord{}, fmt.Errorf("load operation: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return OperationRecord{}, err
	}
	record.StartedAt = parsed
	if finishedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err != nil {
			return OperationRecord{}, err
		}
		record.FinishedAt = &parsed
	}
	record.Actor = append(json.RawMessage(nil), actor...)
	record.Result = append(json.RawMessage(nil), result...)
	return record, nil
}

func (store *Store) GetOperationByPlan(ctx context.Context, planID string) (OperationRecord, error) {
	var operationID string
	err := store.db.QueryRowContext(
		ctx, `SELECT operation_id FROM operations WHERE plan_id = ?`, planID,
	).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return OperationRecord{}, ErrNotFound
	}
	if err != nil {
		return OperationRecord{}, fmt.Errorf("load operation by plan: %w", err)
	}
	return store.GetOperation(ctx, operationID)
}

func (store *Store) ListSteps(ctx context.Context, operationID string) ([]StepRecord, error) {
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT operation_id, step_id, repository_id, action, idempotency_key, sequence, status,
		        before_json, after_json, COALESCE(last_error, ''), started_at, finished_at
		 FROM operation_steps WHERE operation_id = ? ORDER BY sequence`,
		operationID,
	)
	if err != nil {
		return nil, fmt.Errorf("list operation steps: %w", err)
	}
	defer rows.Close()
	steps := []StepRecord{}
	for rows.Next() {
		var step StepRecord
		var before, after []byte
		var startedAt, finishedAt sql.NullString
		if err := rows.Scan(
			&step.OperationID, &step.StepID, &step.RepositoryID, &step.Action,
			&step.IdempotencyKey, &step.Sequence, &step.Status, &before, &after, &step.LastError,
			&startedAt, &finishedAt,
		); err != nil {
			return nil, fmt.Errorf("decode operation step: %w", err)
		}
		step.Before = append(json.RawMessage(nil), before...)
		step.After = append(json.RawMessage(nil), after...)
		if startedAt.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, startedAt.String)
			if err != nil {
				return nil, fmt.Errorf("decode step start: %w", err)
			}
			step.StartedAt = &parsed
		}
		if finishedAt.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, finishedAt.String)
			if err != nil {
				return nil, fmt.Errorf("decode step finish: %w", err)
			}
			step.FinishedAt = &parsed
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operation steps: %w", err)
	}
	return steps, nil
}

func (store *Store) ListEvents(ctx context.Context, operationID string) ([]EventRecord, error) {
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT sequence, operation_id, plan_id, COALESCE(step_id, ''), event_type,
                occurred_at, payload_json, payload_digest
         FROM operation_events WHERE operation_id = ? ORDER BY sequence`,
		operationID,
	)
	if err != nil {
		return nil, fmt.Errorf("list operation events: %w", err)
	}
	defer rows.Close()
	events := []EventRecord{}
	for rows.Next() {
		var event EventRecord
		var occurredAt string
		var payload []byte
		if err := rows.Scan(
			&event.Sequence, &event.OperationID, &event.PlanID, &event.StepID,
			&event.EventType, &occurredAt, &payload, &event.PayloadDigest,
		); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, err
		}
		event.OccurredAt = parsed
		event.Payload = append(json.RawMessage(nil), payload...)
		events = append(events, event)
	}
	return events, rows.Err()
}

func optionalJSON(value any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode step evidence: %w", err)
	}
	return raw, nil
}
