package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type DeviceEvidenceRecord struct {
	EvidenceID     string          `json:"evidence_id"`
	DeviceID       string          `json:"device_id"`
	ObservedAt     time.Time       `json:"observed_at"`
	ExpiresAt      time.Time       `json:"expires_at"`
	EvidenceDigest string          `json:"evidence_digest"`
	Body           json.RawMessage `json:"body"`
	InsertedAt     time.Time       `json:"inserted_at"`
}

func (store *Store) PutDeviceEvidence(ctx context.Context, value DeviceEvidenceRecord) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if value.EvidenceID == "" || value.DeviceID == "" || value.ObservedAt.IsZero() ||
		!value.ExpiresAt.After(value.ObservedAt) || value.EvidenceDigest == "" ||
		!json.Valid(value.Body) || value.InsertedAt.IsZero() {
		return errors.New("invalid device evidence record")
	}
	_, err := store.db.ExecContext(ctx, `INSERT INTO device_evidence(
        evidence_id, device_id, observed_at, expires_at, evidence_digest, body_json, inserted_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?)`, value.EvidenceID, value.DeviceID,
		formatTime(value.ObservedAt), formatTime(value.ExpiresAt), value.EvidenceDigest,
		string(value.Body), formatTime(value.InsertedAt))
	if err != nil {
		return fmt.Errorf("store device evidence: %w", err)
	}
	return nil
}

func (store *Store) LatestDeviceEvidence(ctx context.Context, deviceID string) (DeviceEvidenceRecord, error) {
	var value DeviceEvidenceRecord
	var observedAt, expiresAt, insertedAt, body string
	err := store.db.QueryRowContext(ctx, `SELECT evidence_id, device_id, observed_at,
        expires_at, evidence_digest, body_json, inserted_at FROM device_evidence
        WHERE device_id = ? ORDER BY observed_at DESC LIMIT 1`, deviceID).Scan(
		&value.EvidenceID, &value.DeviceID, &observedAt, &expiresAt,
		&value.EvidenceDigest, &body, &insertedAt)
	if err != nil {
		return DeviceEvidenceRecord{}, normalizeNotFound(err, "load latest device evidence")
	}
	var parseErr error
	value.ObservedAt, parseErr = time.Parse(time.RFC3339Nano, observedAt)
	if parseErr == nil {
		value.ExpiresAt, parseErr = time.Parse(time.RFC3339Nano, expiresAt)
	}
	if parseErr == nil {
		value.InsertedAt, parseErr = time.Parse(time.RFC3339Nano, insertedAt)
	}
	if parseErr != nil {
		return DeviceEvidenceRecord{}, fmt.Errorf("decode device evidence timestamps: %w", parseErr)
	}
	value.Body = json.RawMessage(body)
	return value, nil
}

func normalizeNotFound(err error, label string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", label, err)
}
