package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// maxHeartbeatLease caps how far a single heartbeat may extend a lease. It
// prevents a long-lived holder from extending a lock indefinitely and defeats
// the lease as a recovery trigger.
const maxHeartbeatLease = time.Hour

type Lock struct {
	Scope          string    `json:"scope"`
	ScopeID        string    `json:"scope_id"`
	LockID         string    `json:"lock_id"`
	OperationID    string    `json:"operation_id"`
	DeviceID       string    `json:"device_id"`
	SessionID      string    `json:"session_id"`
	PID            int       `json:"pid"`
	FencingToken   int64     `json:"fencing_token"`
	AcquiredAt     time.Time `json:"acquired_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	HeartbeatAt    time.Time `json:"heartbeat_at"`
}

func (store *Store) AcquireLock(ctx context.Context, lock Lock) (Lock, error) {
	if store.readOnly {
		return Lock{}, ErrReadOnly
	}
	if lock.Scope == "" || lock.ScopeID == "" || lock.LockID == "" || lock.OperationID == "" ||
		lock.DeviceID == "" || lock.SessionID == "" || lock.PID <= 0 ||
		!lock.LeaseExpiresAt.After(lock.AcquiredAt) {
		return Lock{}, fmt.Errorf("invalid lock request")
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Lock{}, fmt.Errorf("begin lock acquisition: %w", err)
	}
	defer transaction.Rollback()
	var existing string
	err = transaction.QueryRowContext(
		ctx, `SELECT lock_id FROM locks WHERE scope = ? AND scope_id = ?`, lock.Scope, lock.ScopeID,
	).Scan(&existing)
	if err == nil {
		return Lock{}, ErrLockHeld
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Lock{}, fmt.Errorf("inspect lock scope: %w", err)
	}
	if err := transaction.QueryRowContext(
		ctx,
		`UPDATE counters SET value = value + 1
         WHERE name = 'lock-fencing-token' RETURNING value`,
	).Scan(&lock.FencingToken); err != nil {
		return Lock{}, fmt.Errorf("allocate fencing token: %w", err)
	}
	lock.HeartbeatAt = lock.AcquiredAt
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO locks(
            scope, scope_id, lock_id, operation_id, device_id, session_id, pid,
            fencing_token, acquired_at, lease_expires_at, heartbeat_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		lock.Scope, lock.ScopeID, lock.LockID, lock.OperationID, lock.DeviceID,
		lock.SessionID, lock.PID, lock.FencingToken, formatTime(lock.AcquiredAt),
		formatTime(lock.LeaseExpiresAt), formatTime(lock.HeartbeatAt),
	); err != nil {
		return Lock{}, fmt.Errorf("insert lock: %w", err)
	}
	if err := store.commit(transaction); err != nil {
		return Lock{}, fmt.Errorf("commit lock acquisition: %w", err)
	}
	return lock, nil
}

func (store *Store) GetLock(ctx context.Context, scope, scopeID string) (Lock, error) {
	var lock Lock
	var acquiredAt, leaseExpiresAt, heartbeatAt string
	err := store.db.QueryRowContext(
		ctx,
		`SELECT scope, scope_id, lock_id, operation_id, device_id, session_id, pid,
                fencing_token, acquired_at, lease_expires_at, heartbeat_at
         FROM locks WHERE scope = ? AND scope_id = ?`,
		scope, scopeID,
	).Scan(
		&lock.Scope, &lock.ScopeID, &lock.LockID, &lock.OperationID, &lock.DeviceID,
		&lock.SessionID, &lock.PID, &lock.FencingToken, &acquiredAt, &leaseExpiresAt,
		&heartbeatAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Lock{}, ErrNotFound
	}
	if err != nil {
		return Lock{}, fmt.Errorf("load lock: %w", err)
	}
	lock.AcquiredAt, err = time.Parse(time.RFC3339Nano, acquiredAt)
	if err == nil {
		lock.LeaseExpiresAt, err = time.Parse(time.RFC3339Nano, leaseExpiresAt)
	}
	if err == nil {
		lock.HeartbeatAt, err = time.Parse(time.RFC3339Nano, heartbeatAt)
	}
	if err != nil {
		return Lock{}, fmt.Errorf("decode lock timestamps: %w", err)
	}
	return lock, nil
}

func (store *Store) ListLocksByOperation(
	ctx context.Context,
	operationID string,
) ([]Lock, error) {
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT scope, scope_id, lock_id, operation_id, device_id, session_id, pid,
                fencing_token, acquired_at, lease_expires_at, heartbeat_at
         FROM locks WHERE operation_id = ? ORDER BY scope, scope_id`,
		operationID,
	)
	if err != nil {
		return nil, fmt.Errorf("list operation locks: %w", err)
	}
	defer rows.Close()
	locks := []Lock{}
	for rows.Next() {
		var lock Lock
		var acquiredAt, leaseExpiresAt, heartbeatAt string
		if err := rows.Scan(
			&lock.Scope, &lock.ScopeID, &lock.LockID, &lock.OperationID,
			&lock.DeviceID, &lock.SessionID, &lock.PID, &lock.FencingToken,
			&acquiredAt, &leaseExpiresAt, &heartbeatAt,
		); err != nil {
			return nil, fmt.Errorf("decode operation lock: %w", err)
		}
		lock.AcquiredAt, err = time.Parse(time.RFC3339Nano, acquiredAt)
		if err == nil {
			lock.LeaseExpiresAt, err = time.Parse(time.RFC3339Nano, leaseExpiresAt)
		}
		if err == nil {
			lock.HeartbeatAt, err = time.Parse(time.RFC3339Nano, heartbeatAt)
		}
		if err != nil {
			return nil, fmt.Errorf("decode operation lock timestamps: %w", err)
		}
		locks = append(locks, lock)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operation locks: %w", err)
	}
	return locks, nil
}

func (store *Store) HeartbeatLock(
	ctx context.Context,
	lock Lock,
	heartbeatAt time.Time,
	leaseExpiresAt time.Time,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if !leaseExpiresAt.After(heartbeatAt) {
		return fmt.Errorf("new lock expiry must follow heartbeat")
	}
	if leaseExpiresAt.After(heartbeatAt.Add(maxHeartbeatLease)) {
		return fmt.Errorf("heartbeat lease exceeds the %s ceiling", maxHeartbeatLease)
	}
	result, err := store.db.ExecContext(
		ctx,
		`UPDATE locks SET heartbeat_at = ?, lease_expires_at = ?
         WHERE scope = ? AND scope_id = ? AND lock_id = ? AND operation_id = ?
           AND fencing_token = ?`,
		formatTime(heartbeatAt), formatTime(leaseExpiresAt), lock.Scope, lock.ScopeID,
		lock.LockID, lock.OperationID, lock.FencingToken,
	)
	if err != nil {
		return fmt.Errorf("heartbeat lock: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrLockOwnership
	}
	return nil
}

func (store *Store) ReleaseLock(ctx context.Context, lock Lock) error {
	if store.readOnly {
		return ErrReadOnly
	}
	result, err := store.db.ExecContext(
		ctx,
		`DELETE FROM locks
         WHERE scope = ? AND scope_id = ? AND lock_id = ? AND operation_id = ?
           AND fencing_token = ?`,
		lock.Scope, lock.ScopeID, lock.LockID, lock.OperationID, lock.FencingToken,
	)
	if err != nil {
		return fmt.Errorf("release lock: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrLockOwnership
	}
	return nil
}
