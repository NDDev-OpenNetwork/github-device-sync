package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type TelemetryEvent struct {
	EventID        string          `json:"event_id"`
	SignalType     string          `json:"signal_type"`
	Body           json.RawMessage `json:"body"`
	Status         string          `json:"status"`
	Attempts       int             `json:"attempts"`
	NextAttemptAt  time.Time       `json:"next_attempt_at"`
	CreatedAt      time.Time       `json:"created_at"`
	LastErrorClass string          `json:"last_error_class,omitempty"`
}

func (store *Store) EnqueueTelemetry(ctx context.Context, event TelemetryEvent, maxPending int, maxBytes int64) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if event.EventID == "" || (event.SignalType != "log" && event.SignalType != "metric" && event.SignalType != "trace") ||
		!json.Valid(event.Body) || len(event.Body) > 1<<20 || event.CreatedAt.IsZero() || event.NextAttemptAt.IsZero() ||
		maxPending < 1 || maxBytes < 2 || int64(len(event.Body)) > maxBytes {
		return errors.New("invalid telemetry outbox event")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	var bytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(body_bytes),0) FROM telemetry_outbox WHERE status='pending'`).Scan(&count, &bytes); err != nil {
		return err
	}
	for count >= maxPending || bytes+int64(len(event.Body)) > maxBytes {
		var oldestID string
		var oldestBytes int64
		if err := tx.QueryRowContext(ctx, `SELECT event_id,body_bytes FROM telemetry_outbox
			WHERE status='pending' ORDER BY created_at,event_id LIMIT 1`).Scan(&oldestID, &oldestBytes); err != nil {
			return fmt.Errorf("select telemetry capacity victim: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE telemetry_outbox SET status='dropped',
			last_error_class='outbox-capacity' WHERE event_id=? AND status='pending'`, oldestID)
		if err != nil {
			return err
		}
		if err := requireOneRow(result); err != nil {
			return err
		}
		count--
		bytes -= oldestBytes
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO telemetry_outbox(event_id,signal_type,body_json,body_bytes,status,attempts,next_attempt_at,created_at)
        VALUES(?,?,?,?, 'pending',0,?,?)`, event.EventID, event.SignalType, string(event.Body), len(event.Body), formatTime(event.NextAttemptAt), formatTime(event.CreatedAt))
	if err != nil {
		return fmt.Errorf("enqueue telemetry: %w", err)
	}
	return store.commit(tx)
}

func (store *Store) PendingTelemetry(ctx context.Context, now time.Time, limit int) ([]TelemetryEvent, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("invalid telemetry batch limit")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT event_id,signal_type,body_json,status,attempts,next_attempt_at,created_at,last_error_class
        FROM telemetry_outbox WHERE status='pending' AND next_attempt_at<=? ORDER BY created_at,event_id LIMIT ?`, formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TelemetryEvent{}
	for rows.Next() {
		var item TelemetryEvent
		var body, next, created string
		if err := rows.Scan(&item.EventID, &item.SignalType, &body, &item.Status, &item.Attempts, &next, &created, &item.LastErrorClass); err != nil {
			return nil, err
		}
		item.Body = json.RawMessage(body)
		item.NextAttemptAt, _ = time.Parse(time.RFC3339Nano, next)
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) CompleteTelemetry(ctx context.Context, eventID string) error {
	result, err := store.db.ExecContext(ctx, `UPDATE telemetry_outbox SET status='sent' WHERE event_id=? AND status='pending'`, eventID)
	if err != nil {
		return err
	}
	return requireOneRow(result)
}

func (store *Store) RetryTelemetry(ctx context.Context, eventID, errorClass string, next time.Time, maximumAttempts int) error {
	if eventID == "" || errorClass == "" || next.IsZero() || maximumAttempts < 1 || maximumAttempts > 100 {
		return errors.New("invalid telemetry retry")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE telemetry_outbox SET attempts=attempts+1,
        status=CASE WHEN attempts+1>=? THEN 'dropped' ELSE 'pending' END,
        next_attempt_at=?,last_error_class=? WHERE event_id=? AND status='pending'`, maximumAttempts, formatTime(next), errorClass, eventID)
	if err != nil {
		return err
	}
	return requireOneRow(result)
}
