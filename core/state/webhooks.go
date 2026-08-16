package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type WebhookDelivery struct {
	DeliveryID    string          `json:"delivery_id"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	PayloadDigest string          `json:"payload_digest"`
	ReceivedAt    time.Time       `json:"received_at"`
	Status        string          `json:"status"`
	AttemptCount  int             `json:"attempt_count"`
	AvailableAt   time.Time       `json:"available_at"`
	ClaimedAt     *time.Time      `json:"claimed_at,omitempty"`
	FinishedAt    *time.Time      `json:"finished_at,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
}

type WebhookQueueSummary struct {
	Queued     int `json:"queued"`
	Processing int `json:"processing"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	DeadLetter int `json:"dead_letter"`
}

const (
	// DefaultWebhookProcessingTimeout is the conservative claim lease used by
	// programmatic workers that do not provide an explicit runtime value.
	DefaultWebhookProcessingTimeout = time.Hour
	// MinWebhookProcessingTimeout and MaxWebhookProcessingTimeout bound claim
	// recovery without allowing immediate duplicate work or indefinite leases.
	MinWebhookProcessingTimeout = time.Minute
	MaxWebhookProcessingTimeout = 24 * time.Hour
)

func (store *Store) WebhookQueueSummary(ctx context.Context) (WebhookQueueSummary, error) {
	rows, err := store.db.QueryContext(
		ctx, `SELECT status, COUNT(*) FROM webhook_deliveries GROUP BY status ORDER BY status`,
	)
	if err != nil {
		return WebhookQueueSummary{}, fmt.Errorf("summarize webhook queue: %w", err)
	}
	defer rows.Close()
	summary := WebhookQueueSummary{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return WebhookQueueSummary{}, fmt.Errorf("scan webhook queue summary: %w", err)
		}
		switch status {
		case "queued":
			summary.Queued = count
		case "processing":
			summary.Processing = count
		case "succeeded":
			summary.Succeeded = count
		case "failed":
			summary.Failed = count
		case "dead-letter":
			summary.DeadLetter = count
		default:
			return WebhookQueueSummary{}, fmt.Errorf("unknown webhook queue status %q", status)
		}
	}
	if err := rows.Err(); err != nil {
		return WebhookQueueSummary{}, fmt.Errorf("iterate webhook queue summary: %w", err)
	}
	return summary, nil
}

func (store *Store) EnqueueWebhook(
	ctx context.Context,
	delivery WebhookDelivery,
) (bool, error) {
	if store.readOnly {
		return false, ErrReadOnly
	}
	if delivery.DeliveryID == "" || delivery.EventType == "" || !json.Valid(delivery.Payload) ||
		delivery.ReceivedAt.IsZero() {
		return false, fmt.Errorf("invalid webhook delivery")
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(delivery.Payload))
	if delivery.PayloadDigest != "" && delivery.PayloadDigest != digest {
		return false, fmt.Errorf("webhook payload digest mismatch")
	}
	delivery.PayloadDigest = digest
	if delivery.AvailableAt.IsZero() {
		delivery.AvailableAt = delivery.ReceivedAt
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin webhook enqueue: %w", err)
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO webhook_deliveries(
            delivery_id, event_type, payload_json, payload_digest, received_at,
            status, attempt_count, available_at
         ) VALUES (?, ?, ?, ?, ?, 'queued', 0, ?)`,
		delivery.DeliveryID, delivery.EventType, []byte(delivery.Payload), digest,
		formatTime(delivery.ReceivedAt), formatTime(delivery.AvailableAt),
	)
	if err != nil {
		return false, fmt.Errorf("enqueue webhook delivery: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 1 {
		var duplicateDelivery string
		err = transaction.QueryRowContext(
			ctx,
			`SELECT delivery_id FROM webhook_deliveries
             WHERE payload_digest = ? AND delivery_id != ? AND status IN ('queued', 'processing', 'failed')
             ORDER BY received_at DESC LIMIT 1`,
			digest, delivery.DeliveryID,
		).Scan(&duplicateDelivery)
		if err == nil && duplicateDelivery != "" {
			if _, err := transaction.ExecContext(
				ctx,
				`DELETE FROM webhook_deliveries WHERE delivery_id = ?`,
				delivery.DeliveryID,
			); err != nil {
				return false, fmt.Errorf("remove duplicate webhook delivery: %w", err)
			}
			if err := store.commit(transaction); err != nil {
				return false, fmt.Errorf("commit webhook dedup: %w", err)
			}
			return false, ErrWebhookConflict
		}
		if err := store.commit(transaction); err != nil {
			return false, fmt.Errorf("commit webhook enqueue: %w", err)
		}
		return true, nil
	}
	transaction.Rollback()
	existing, err := store.GetWebhook(ctx, delivery.DeliveryID)
	if err != nil {
		return false, err
	}
	if existing.EventType != delivery.EventType || existing.PayloadDigest != digest ||
		!bytes.Equal(existing.Payload, delivery.Payload) {
		return false, ErrWebhookConflict
	}
	return false, nil
}

func (store *Store) GetWebhook(ctx context.Context, deliveryID string) (WebhookDelivery, error) {
	row := store.db.QueryRowContext(
		ctx,
		`SELECT delivery_id, event_type, payload_json, payload_digest, received_at,
                status, attempt_count, available_at, claimed_at, finished_at,
                COALESCE(last_error, '')
         FROM webhook_deliveries WHERE delivery_id = ?`,
		deliveryID,
	)
	return scanWebhook(row)
}

func (store *Store) ClaimWebhook(
	ctx context.Context,
	now time.Time,
	maxAttempts int,
	processingTimeout time.Duration,
) (WebhookDelivery, error) {
	if store.readOnly {
		return WebhookDelivery{}, ErrReadOnly
	}
	if now.IsZero() || maxAttempts < 1 ||
		processingTimeout < MinWebhookProcessingTimeout ||
		processingTimeout > MaxWebhookProcessingTimeout {
		return WebhookDelivery{}, fmt.Errorf("invalid webhook claim policy")
	}
	// Sweep on first use, policy change, or one visibility interval. The
	// watermark advances only after the same transaction commits, keeping the
	// ordinary claim path to one write without losing crash recovery.
	store.webhookMaintenanceMu.Lock()
	defer store.webhookMaintenanceMu.Unlock()
	needsMaintenance := store.webhookMaintenanceAt.IsZero() ||
		store.webhookMaintenanceMaxAttempts != maxAttempts ||
		now.Before(store.webhookMaintenanceAt) ||
		now.Sub(store.webhookMaintenanceAt) >= processingTimeout
	staleAt := now.Add(-processingTimeout)
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return WebhookDelivery{}, fmt.Errorf("begin webhook claim: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if needsMaintenance {
		if _, err := transaction.ExecContext(
			ctx,
			`UPDATE webhook_deliveries
             SET status = 'dead-letter', finished_at = ?,
                 last_error = CASE
                     WHEN status = 'processing'
                     THEN 'webhook processing claim expired at attempt limit'
                     WHEN last_error IS NULL OR last_error = ''
                     THEN 'webhook retry attempt limit reached'
                     ELSE last_error
                 END
             WHERE status IN ('failed', 'processing') AND attempt_count >= ?
               AND (status = 'failed'
                    OR (claimed_at IS NOT NULL AND claimed_at <= ?))`,
			formatTime(now), maxAttempts, formatTime(staleAt),
		); err != nil {
			return WebhookDelivery{}, fmt.Errorf("expire exhausted webhooks: %w", err)
		}
	}
	if needsMaintenance {
		delivery, claimErr := claimStaleWebhookTx(
			ctx, transaction, now, staleAt, maxAttempts,
		)
		if claimErr == nil {
			if err := store.commit(transaction); err != nil {
				return WebhookDelivery{}, fmt.Errorf("commit stale webhook claim: %w", err)
			}
			// Keep the sweep due so the next claim drains any remaining stale rows.
			return delivery, nil
		}
		if !errors.Is(claimErr, ErrNotFound) {
			return WebhookDelivery{}, claimErr
		}
	}
	delivery, err := claimReadyWebhookTx(ctx, transaction, now, maxAttempts)
	if errors.Is(err, ErrNotFound) && !needsMaintenance {
		delivery, err = claimStaleWebhookTx(ctx, transaction, now, staleAt, maxAttempts)
	}
	if errors.Is(err, ErrNotFound) {
		if err := store.commit(transaction); err != nil {
			return WebhookDelivery{}, fmt.Errorf("commit webhook claim expiration: %w", err)
		}
		store.recordWebhookMaintenance(now, maxAttempts, needsMaintenance)
		return WebhookDelivery{}, ErrNotFound
	}
	if err != nil {
		return WebhookDelivery{}, err
	}
	if err := store.commit(transaction); err != nil {
		return WebhookDelivery{}, fmt.Errorf("commit webhook claim: %w", err)
	}
	store.recordWebhookMaintenance(now, maxAttempts, needsMaintenance)
	return delivery, nil
}

func claimReadyWebhookTx(
	ctx context.Context,
	transaction *sql.Tx,
	now time.Time,
	maxAttempts int,
) (WebhookDelivery, error) {
	row := transaction.QueryRowContext(
		ctx,
		`UPDATE webhook_deliveries
         SET status = 'processing', attempt_count = attempt_count + 1,
             claimed_at = ?, finished_at = NULL, last_error = NULL
         WHERE delivery_id = (
             SELECT delivery_id FROM webhook_deliveries
             WHERE status IN ('queued', 'failed') AND attempt_count < ?
               AND available_at <= ?
             ORDER BY available_at, received_at, delivery_id
             LIMIT 1
         )
         RETURNING delivery_id, event_type, payload_json, payload_digest,
                   received_at, status, attempt_count, available_at, claimed_at,
                   finished_at, COALESCE(last_error, '')`,
		formatTime(now), maxAttempts, formatTime(now),
	)
	return scanWebhook(row)
}

func claimStaleWebhookTx(
	ctx context.Context,
	transaction *sql.Tx,
	now time.Time,
	staleAt time.Time,
	maxAttempts int,
) (WebhookDelivery, error) {
	row := transaction.QueryRowContext(
		ctx,
		`UPDATE webhook_deliveries
         SET status = 'processing', attempt_count = attempt_count + 1,
             claimed_at = ?, finished_at = NULL, last_error = NULL
         WHERE delivery_id = (
             SELECT delivery_id FROM webhook_deliveries
             WHERE status = 'processing' AND attempt_count < ?
               AND claimed_at IS NOT NULL AND claimed_at <= ?
             ORDER BY claimed_at, received_at, delivery_id
             LIMIT 1
         )
         RETURNING delivery_id, event_type, payload_json, payload_digest,
                   received_at, status, attempt_count, available_at, claimed_at,
                   finished_at, COALESCE(last_error, '')`,
		formatTime(now), maxAttempts, formatTime(staleAt),
	)
	return scanWebhook(row)
}

func (store *Store) recordWebhookMaintenance(now time.Time, maxAttempts int, performed bool) {
	if !performed {
		return
	}
	store.webhookMaintenanceAt = now
	store.webhookMaintenanceMaxAttempts = maxAttempts
}

func (store *Store) CompleteWebhook(
	ctx context.Context,
	deliveryID string,
	claimedAt time.Time,
	succeeded bool,
	now time.Time,
	retryAt time.Time,
	maxAttempts int,
	lastError string,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if deliveryID == "" || claimedAt.IsZero() || now.IsZero() ||
		now.Before(claimedAt) || maxAttempts < 1 {
		return fmt.Errorf("invalid webhook completion")
	}
	// Default to "failed" before the SELECT so the read+write stays atomic: a
	// dead-letter decision is made only when attempts are exhausted, and the
	// retryAt validation remains inside the retryable branch.
	nextStatus := "succeeded"
	availableAt := any(nil)
	if !succeeded {
		nextStatus = "failed"
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin webhook completion: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if !succeeded {
		var attempts int
		err := transaction.QueryRowContext(
			ctx,
			`SELECT attempt_count FROM webhook_deliveries
             WHERE delivery_id = ? AND status = 'processing' AND claimed_at = ?`,
			deliveryID, formatTime(claimedAt),
		).Scan(&attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStateConflict
		}
		if err != nil {
			return fmt.Errorf("inspect webhook attempts: %w", err)
		}
		if attempts >= maxAttempts {
			nextStatus = "dead-letter"
		} else {
			if retryAt.IsZero() || !retryAt.After(now) {
				return fmt.Errorf("failed webhook retry time must follow completion")
			}
			availableAt = formatTime(retryAt)
		}
	}
	result, err := transaction.ExecContext(
		ctx,
		`UPDATE webhook_deliveries
         SET status = ?, finished_at = ?, available_at = COALESCE(?, available_at),
             last_error = NULLIF(?, '')
		 WHERE delivery_id = ? AND status = 'processing' AND claimed_at = ?`,
		nextStatus, formatTime(now), availableAt, lastError, deliveryID, formatTime(claimedAt),
	)
	if err != nil {
		return fmt.Errorf("complete webhook delivery: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrStateConflict
	}
	if err := store.commit(transaction); err != nil {
		return fmt.Errorf("commit webhook completion: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanWebhook(row rowScanner) (WebhookDelivery, error) {
	var delivery WebhookDelivery
	var payload []byte
	var receivedAt, availableAt string
	var claimedAt, finishedAt sql.NullString
	err := row.Scan(
		&delivery.DeliveryID, &delivery.EventType, &payload, &delivery.PayloadDigest,
		&receivedAt, &delivery.Status, &delivery.AttemptCount, &availableAt,
		&claimedAt, &finishedAt, &delivery.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookDelivery{}, ErrNotFound
	}
	if err != nil {
		return WebhookDelivery{}, fmt.Errorf("load webhook delivery: %w", err)
	}
	delivery.Payload = append(json.RawMessage(nil), payload...)
	if delivery.ReceivedAt, err = time.Parse(time.RFC3339Nano, receivedAt); err != nil {
		return WebhookDelivery{}, err
	}
	if delivery.AvailableAt, err = time.Parse(time.RFC3339Nano, availableAt); err != nil {
		return WebhookDelivery{}, err
	}
	if claimedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, claimedAt.String)
		if parseErr != nil {
			return WebhookDelivery{}, parseErr
		}
		delivery.ClaimedAt = &parsed
	}
	if finishedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, finishedAt.String)
		if parseErr != nil {
			return WebhookDelivery{}, parseErr
		}
		delivery.FinishedAt = &parsed
	}
	return delivery, nil
}
