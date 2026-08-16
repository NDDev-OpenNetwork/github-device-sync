package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var reconciliationErrorCodePattern = regexp.MustCompile(`^GDS_[A-Z0-9_]+$`)

type ReconciliationRecord struct {
	ReconciliationID string          `json:"reconciliation_id"`
	Scope            string          `json:"scope"`
	ScopeID          string          `json:"scope_id"`
	Status           string          `json:"status"`
	StartedAt        time.Time       `json:"started_at"`
	FinishedAt       *time.Time      `json:"finished_at,omitempty"`
	Cursor           json.RawMessage `json:"cursor,omitempty"`
	CursorSequence   int64           `json:"cursor_sequence"`
	CursorDigest     string          `json:"cursor_digest,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	LastError        string          `json:"last_error,omitempty"`
}

func (store *Store) StartReconciliation(
	ctx context.Context,
	record ReconciliationRecord,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if record.ReconciliationID == "" || record.Scope == "" || record.ScopeID == "" ||
		record.Status != "running" || record.StartedAt.IsZero() ||
		(len(record.Cursor) != 0 && !json.Valid(record.Cursor)) {
		return fmt.Errorf("invalid reconciliation record")
	}
	cursor, cursorDigest, err := normalizeCursor(record.Cursor)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(
		ctx,
		`INSERT INTO reconciliation_runs(
            reconciliation_id, scope, scope_id, status, started_at, cursor_json,
            cursor_sequence, cursor_digest
         ) VALUES (?, ?, ?, 'running', ?, ?, 0, ?)`,
		record.ReconciliationID, record.Scope, record.ScopeID,
		formatTime(record.StartedAt), nullableBytes(cursor), cursorDigest,
	)
	if err != nil {
		return fmt.Errorf("start reconciliation: %w", err)
	}
	return nil
}

func (store *Store) UpdateReconciliationCursor(
	ctx context.Context,
	reconciliationID string,
	expectedSequence int64,
	next any,
) (ReconciliationRecord, error) {
	if store.readOnly {
		return ReconciliationRecord{}, ErrReadOnly
	}
	if reconciliationID == "" || expectedSequence < 0 {
		return ReconciliationRecord{}, fmt.Errorf("invalid reconciliation cursor update")
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return ReconciliationRecord{}, fmt.Errorf("encode reconciliation cursor: %w", err)
	}
	cursor, digest, err := normalizeCursor(raw)
	if err != nil {
		return ReconciliationRecord{}, err
	}
	result, err := store.db.ExecContext(
		ctx,
		`UPDATE reconciliation_runs
         SET cursor_json = ?, cursor_sequence = cursor_sequence + 1,
             cursor_digest = ?
         WHERE reconciliation_id = ? AND status = 'running'
           AND cursor_sequence = ?`,
		[]byte(cursor), digest, reconciliationID, expectedSequence,
	)
	if err != nil {
		return ReconciliationRecord{}, fmt.Errorf("update reconciliation cursor: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ReconciliationRecord{}, err
	}
	if changed != 1 {
		return ReconciliationRecord{}, ErrStateConflict
	}
	return store.GetReconciliation(ctx, reconciliationID)
}

func (store *Store) FinishReconciliation(
	ctx context.Context,
	reconciliationID string,
	status string,
	finishedAt time.Time,
	result any,
	lastError string,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if status != "succeeded" && status != "failed" && status != "partial" && status != "blocked" {
		return fmt.Errorf("invalid reconciliation terminal status")
	}
	if lastError != "" && !reconciliationErrorCodePattern.MatchString(lastError) {
		return fmt.Errorf("reconciliation error must be an empty value or stable GDS code")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode reconciliation result: %w", err)
	}
	databaseResult, err := store.db.ExecContext(
		ctx,
		`UPDATE reconciliation_runs
         SET status = ?, finished_at = ?, result_json = ?, last_error = NULLIF(?, '')
         WHERE reconciliation_id = ? AND status = 'running'`,
		status, formatTime(finishedAt), raw, lastError, reconciliationID,
	)
	if err != nil {
		return fmt.Errorf("finish reconciliation: %w", err)
	}
	changed, err := databaseResult.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrStateConflict
	}
	return nil
}

func (store *Store) GetReconciliation(
	ctx context.Context,
	reconciliationID string,
) (ReconciliationRecord, error) {
	var record ReconciliationRecord
	var startedAt string
	var finishedAt sql.NullString
	var cursor, result []byte
	err := store.db.QueryRowContext(
		ctx,
		`SELECT reconciliation_id, scope, scope_id, status, started_at, finished_at,
                cursor_json, cursor_sequence, cursor_digest,
                result_json, COALESCE(last_error, '')
         FROM reconciliation_runs WHERE reconciliation_id = ?`,
		reconciliationID,
	).Scan(
		&record.ReconciliationID, &record.Scope, &record.ScopeID, &record.Status,
		&startedAt, &finishedAt, &cursor, &record.CursorSequence,
		&record.CursorDigest, &result, &record.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ReconciliationRecord{}, ErrNotFound
	}
	if err != nil {
		return ReconciliationRecord{}, fmt.Errorf("load reconciliation: %w", err)
	}
	record.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return ReconciliationRecord{}, err
	}
	if finishedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, finishedAt.String)
		if parseErr != nil {
			return ReconciliationRecord{}, parseErr
		}
		record.FinishedAt = &parsed
	}
	record.Cursor = append(json.RawMessage(nil), cursor...)
	record.Result = append(json.RawMessage(nil), result...)
	return record, nil
}

func normalizeCursor(raw []byte) (json.RawMessage, string, error) {
	if len(raw) == 0 {
		return nil, "", nil
	}
	if !json.Valid(raw) {
		return nil, "", fmt.Errorf("reconciliation cursor is not valid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, "", fmt.Errorf("compact reconciliation cursor: %w", err)
	}
	content := append(json.RawMessage(nil), compact.Bytes()...)
	return content, fmt.Sprintf("sha256:%x", sha256.Sum256(content)), nil
}
