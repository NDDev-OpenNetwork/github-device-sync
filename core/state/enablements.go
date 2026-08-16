package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type PlanEnablement struct {
	EnablementID   string    `json:"enablement_id"`
	PlanID         string    `json:"plan_id"`
	PlanDigest     string    `json:"plan_digest"`
	ApprovalID     string    `json:"approval_id"`
	ApprovalDigest string    `json:"approval_digest"`
	DeviceID       string    `json:"device_id"`
	SessionID      string    `json:"session_id"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	MaximumStarts  int       `json:"maximum_starts"`
	Starts         int       `json:"starts"`
	Status         string    `json:"status"`
	ConsumedAt     time.Time `json:"consumed_at,omitempty"`
	OperationID    string    `json:"operation_id,omitempty"`
}

func (store *Store) CreatePlanEnablement(ctx context.Context, value PlanEnablement) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if value.EnablementID == "" || value.PlanID == "" || value.PlanDigest == "" ||
		value.ApprovalID == "" || value.ApprovalDigest == "" || value.DeviceID == "" ||
		value.SessionID == "" || value.CreatedAt.IsZero() || !value.ExpiresAt.After(value.CreatedAt) ||
		value.MaximumStarts != 1 || value.Starts != 0 || value.Status != "active" ||
		!value.ConsumedAt.IsZero() || value.OperationID != "" {
		return errors.New("invalid plan enablement")
	}
	result, err := store.db.ExecContext(ctx, `
        INSERT INTO plan_enablements(
            enablement_id, plan_id, plan_digest, approval_id, approval_digest,
            device_id, session_id, created_at, expires_at, maximum_starts, starts, status
        )
        SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, 'active'
        WHERE EXISTS (
            SELECT 1 FROM plans
            WHERE plan_id = ? AND plan_digest = ? AND status = 'planned'
        )`,
		value.EnablementID, value.PlanID, value.PlanDigest, value.ApprovalID,
		value.ApprovalDigest, value.DeviceID, value.SessionID,
		formatTime(value.CreatedAt), formatTime(value.ExpiresAt),
		value.PlanID, value.PlanDigest,
	)
	if err != nil {
		return fmt.Errorf("create plan enablement: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return fmt.Errorf("create plan enablement: %w", err)
	}
	return nil
}

func (store *Store) GetPlanEnablement(ctx context.Context, enablementID string) (PlanEnablement, error) {
	var value PlanEnablement
	var createdAt, expiresAt string
	var consumedAt *string
	err := store.db.QueryRowContext(ctx, `
        SELECT enablement_id, plan_id, plan_digest, approval_id, approval_digest,
               device_id, session_id, created_at, expires_at, maximum_starts,
               starts, status, consumed_at, COALESCE(operation_id, '')
        FROM plan_enablements WHERE enablement_id = ?`, enablementID,
	).Scan(
		&value.EnablementID, &value.PlanID, &value.PlanDigest, &value.ApprovalID,
		&value.ApprovalDigest, &value.DeviceID, &value.SessionID, &createdAt, &expiresAt,
		&value.MaximumStarts, &value.Starts, &value.Status, &consumedAt, &value.OperationID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PlanEnablement{}, ErrNotFound
		}
		return PlanEnablement{}, fmt.Errorf("load plan enablement: %w", err)
	}
	value.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err == nil {
		value.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	}
	if err == nil && consumedAt != nil {
		value.ConsumedAt, err = time.Parse(time.RFC3339Nano, *consumedAt)
	}
	if err != nil {
		return PlanEnablement{}, fmt.Errorf("decode plan enablement timestamps: %w", err)
	}
	return value, nil
}
