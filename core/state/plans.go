package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type PlanRecord struct {
	PlanID     string          `json:"plan_id"`
	Operation  string          `json:"operation"`
	PlanDigest string          `json:"plan_digest"`
	Body       json.RawMessage `json:"body"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	ExpiresAt  time.Time       `json:"expires_at"`
	InsertedAt time.Time       `json:"inserted_at"`
}

func (store *Store) PutPlan(ctx context.Context, record PlanRecord) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if record.PlanID == "" || record.Operation == "" || record.PlanDigest == "" ||
		!json.Valid(record.Body) || record.Status != "planned" ||
		!record.ExpiresAt.After(record.CreatedAt) {
		return fmt.Errorf("invalid plan record")
	}
	_, err := store.db.ExecContext(
		ctx,
		`INSERT INTO plans(
            plan_id, operation, plan_digest, body_json, status,
            created_at, expires_at, inserted_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		record.PlanID, record.Operation, record.PlanDigest, []byte(record.Body), record.Status,
		formatTime(record.CreatedAt), formatTime(record.ExpiresAt), formatTime(record.InsertedAt),
	)
	if err == nil {
		return nil
	}
	existing, loadErr := store.GetPlan(ctx, record.PlanID)
	if loadErr != nil {
		return fmt.Errorf("insert plan: %w", err)
	}
	if existing.Operation == record.Operation && existing.PlanDigest == record.PlanDigest &&
		bytes.Equal(existing.Body, record.Body) && existing.CreatedAt.Equal(record.CreatedAt) &&
		existing.ExpiresAt.Equal(record.ExpiresAt) {
		return nil
	}
	return ErrPlanConflict
}

func (store *Store) GetPlan(ctx context.Context, planID string) (PlanRecord, error) {
	var record PlanRecord
	var body []byte
	var createdAt, expiresAt, insertedAt string
	err := store.db.QueryRowContext(
		ctx,
		`SELECT plan_id, operation, plan_digest, body_json, status,
                created_at, expires_at, inserted_at
         FROM plans WHERE plan_id = ?`,
		planID,
	).Scan(
		&record.PlanID, &record.Operation, &record.PlanDigest, &body, &record.Status,
		&createdAt, &expiresAt, &insertedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanRecord{}, ErrNotFound
	}
	if err != nil {
		return PlanRecord{}, fmt.Errorf("load plan: %w", err)
	}
	var parseErr error
	record.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt)
	if parseErr == nil {
		record.ExpiresAt, parseErr = time.Parse(time.RFC3339Nano, expiresAt)
	}
	if parseErr == nil {
		record.InsertedAt, parseErr = time.Parse(time.RFC3339Nano, insertedAt)
	}
	if parseErr != nil {
		return PlanRecord{}, fmt.Errorf("decode plan timestamps: %w", parseErr)
	}
	record.Body = append(json.RawMessage(nil), body...)
	return record, nil
}

func (store *Store) TransitionPlan(
	ctx context.Context,
	planID string,
	expected string,
	next string,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	result, err := store.db.ExecContext(
		ctx, `UPDATE plans SET status = ? WHERE plan_id = ? AND status = ?`,
		next, planID, expected,
	)
	if err != nil {
		return fmt.Errorf("transition plan: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect plan transition: %w", err)
	}
	if changed != 1 {
		return ErrStateConflict
	}
	return nil
}
