package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// OperationEvent is an event that must commit with an operation lifecycle transition.
type OperationEvent struct {
	StepID     string
	EventType  string
	OccurredAt time.Time
	Payload    any
}

// ApprovedOperationStart describes the all-or-nothing start of an approved operation.
type ApprovedOperationStart struct {
	Operation  OperationRecord
	Steps      []StepRecord
	Locks      []Lock
	Approval   *OperationEvent
	Enablement *EnablementConsumption
}

// EnablementConsumption binds one locally enabled exact plan to the operation
// that consumes its sole permitted start.
type EnablementConsumption struct {
	EnablementID   string
	PlanDigest     string
	ApprovalID     string
	ApprovalDigest string
	DeviceID       string
	SessionID      string
	ConsumedAt     time.Time
}

// TerminalStepTransition describes an exact step CAS performed during finalization.
type TerminalStepTransition struct {
	StepID    string
	Expected  string
	Next      string
	Before    any
	After     any
	LastError string
}

// OperationFinalization describes one terminal operation transaction.
type OperationFinalization struct {
	OperationID             string
	PlanID                  string
	ExpectedOperationStatus string
	ExpectedPlanStatus      string
	OperationStatus         string
	PlanStatus              string
	FinishedAt              time.Time
	Result                  any
	StepTransitions         []TerminalStepTransition
	Events                  []OperationEvent
	Locks                   []Lock
}

// StartApprovedOperation atomically approves a plan, creates its journal, records
// approval evidence, and acquires the complete lock set. A lock conflict leaves no
// operation and keeps the plan in planned state.
func (store *Store) StartApprovedOperation(
	ctx context.Context,
	start ApprovedOperationStart,
) ([]Lock, error) {
	if store.readOnly {
		return nil, ErrReadOnly
	}
	if err := validateOperationRecords(start.Operation, start.Steps); err != nil {
		return nil, err
	}
	if err := validateStartLocks(start.Operation.OperationID, start.Locks); err != nil {
		return nil, err
	}
	requestedLocks := append([]Lock(nil), start.Locks...)
	sort.Slice(requestedLocks, func(left, right int) bool {
		if requestedLocks[left].Scope != requestedLocks[right].Scope {
			return requestedLocks[left].Scope < requestedLocks[right].Scope
		}
		return requestedLocks[left].ScopeID < requestedLocks[right].ScopeID
	})
	if start.Approval != nil {
		if start.Approval.EventType != "approval-recorded" || start.Approval.StepID != "" ||
			start.Approval.OccurredAt.IsZero() {
			return nil, errors.New("invalid approval journal event")
		}
	}
	if start.Enablement != nil {
		value := start.Enablement
		if value.EnablementID == "" || value.PlanDigest == "" || value.ApprovalID == "" ||
			value.ApprovalDigest == "" || value.DeviceID == "" || value.SessionID == "" ||
			value.ConsumedAt.IsZero() {
			return nil, errors.New("invalid plan enablement consumption")
		}
	}

	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin approved operation start: %w", err)
	}
	defer transaction.Rollback()

	// Acquire SQLite's writer serialization before opening a read snapshot. In
	// WAL mode a transaction that reads first cannot reliably upgrade after a
	// competing writer commits, even with busy_timeout configured.
	result, err := transaction.ExecContext(
		ctx,
		`UPDATE plans SET status = 'approved' WHERE plan_id = ? AND status = 'planned'`,
		start.Operation.PlanID,
	)
	if err != nil {
		return nil, fmt.Errorf("approve operation plan: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return nil, err
	}
	if start.Enablement != nil {
		value := start.Enablement
		result, err := transaction.ExecContext(ctx, `
            UPDATE plan_enablements
            SET starts = 1, status = 'consumed', consumed_at = ?, operation_id = ?
            WHERE enablement_id = ? AND plan_id = ? AND plan_digest = ?
              AND approval_id = ? AND approval_digest = ?
              AND device_id = ? AND session_id = ?
              AND maximum_starts = 1 AND starts = 0 AND status = 'active'
              AND expires_at > ?`,
			formatTime(value.ConsumedAt), start.Operation.OperationID,
			value.EnablementID, start.Operation.PlanID, value.PlanDigest,
			value.ApprovalID, value.ApprovalDigest, value.DeviceID, value.SessionID,
			formatTime(value.ConsumedAt),
		)
		if err != nil {
			return nil, fmt.Errorf("consume plan enablement: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return nil, fmt.Errorf("consume plan enablement: %w", err)
		}
	}

	for _, lock := range requestedLocks {
		var existing string
		err := transaction.QueryRowContext(
			ctx,
			`SELECT lock_id FROM locks WHERE scope = ? AND scope_id = ?`,
			lock.Scope, lock.ScopeID,
		).Scan(&existing)
		if err == nil {
			return nil, ErrLockHeld
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("inspect operation lock scope: %w", err)
		}
	}
	if err := insertOperationJournalTx(ctx, transaction, start.Operation, start.Steps); err != nil {
		return nil, err
	}
	if start.Approval != nil {
		if _, err := appendEventTx(
			ctx, transaction, start.Operation.OperationID, start.Operation.PlanID,
			start.Approval.StepID, start.Approval.EventType, start.Approval.OccurredAt,
			start.Approval.Payload,
		); err != nil {
			return nil, err
		}
	}

	acquired := requestedLocks
	for index := range acquired {
		lock := &acquired[index]
		if err := transaction.QueryRowContext(
			ctx,
			`UPDATE counters SET value = value + 1
             WHERE name = 'lock-fencing-token' RETURNING value`,
		).Scan(&lock.FencingToken); err != nil {
			return nil, fmt.Errorf("allocate operation fencing token: %w", err)
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
			return nil, fmt.Errorf("insert operation lock: %w", err)
		}
	}
	if err := store.commit(transaction); err != nil {
		return nil, fmt.Errorf("commit approved operation start: %w", err)
	}
	return acquired, nil
}

// FinalizeOperation commits all remaining step transitions, operation and plan
// terminal states, terminal evidence, and exact lock release as one transaction.
func (store *Store) FinalizeOperation(
	ctx context.Context,
	finalization OperationFinalization,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	if err := validateFinalization(finalization); err != nil {
		return err
	}
	resultRaw, err := optionalJSON(finalization.Result)
	if err != nil {
		return fmt.Errorf("encode operation result: %w", err)
	}
	if resultRaw == nil {
		return errors.New("operation finalization result is required")
	}

	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin operation finalization: %w", err)
	}
	defer transaction.Rollback()

	for _, transition := range finalization.StepTransitions {
		if err := transitionTerminalStepTx(
			ctx, transaction, finalization.OperationID, transition, finalization.FinishedAt,
		); err != nil {
			return err
		}
	}
	if err := validateTerminalStepsTx(ctx, transaction, finalization); err != nil {
		return err
	}

	result, err := transaction.ExecContext(
		ctx,
		`UPDATE operations SET status = ?, finished_at = ?, result_json = ?
         WHERE operation_id = ? AND plan_id = ? AND status = ?`,
		finalization.OperationStatus, formatTime(finalization.FinishedAt), resultRaw,
		finalization.OperationID, finalization.PlanID, finalization.ExpectedOperationStatus,
	)
	if err != nil {
		return fmt.Errorf("finish operation atomically: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	result, err = transaction.ExecContext(
		ctx,
		`UPDATE plans SET status = ? WHERE plan_id = ? AND status = ?`,
		finalization.PlanStatus, finalization.PlanID, finalization.ExpectedPlanStatus,
	)
	if err != nil {
		return fmt.Errorf("finish operation plan atomically: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	for _, event := range finalization.Events {
		if _, err := appendEventTx(
			ctx, transaction, finalization.OperationID, finalization.PlanID,
			event.StepID, event.EventType, event.OccurredAt, event.Payload,
		); err != nil {
			return err
		}
	}
	if _, err := appendEventTx(
		ctx, transaction, finalization.OperationID, finalization.PlanID, "",
		"operation-"+finalization.OperationStatus, finalization.FinishedAt,
		finalization.Result,
	); err != nil {
		return err
	}
	if err := releaseExactOperationLocksTx(ctx, transaction, finalization); err != nil {
		return err
	}

	if err := store.commit(transaction); err != nil {
		return fmt.Errorf("commit operation finalization: %w", err)
	}
	return nil
}

func validateStartLocks(operationID string, locks []Lock) error {
	if len(locks) == 0 {
		return errors.New("approved operation requires a non-empty lock set")
	}
	seen := make(map[string]struct{}, len(locks))
	seenIDs := make(map[string]struct{}, len(locks))
	for _, lock := range locks {
		key := lock.Scope + "\x00" + lock.ScopeID
		if lock.Scope == "" || lock.ScopeID == "" || lock.LockID == "" ||
			lock.OperationID != operationID || lock.DeviceID == "" || lock.SessionID == "" ||
			lock.PID <= 0 || lock.FencingToken != 0 || lock.AcquiredAt.IsZero() ||
			!lock.LeaseExpiresAt.After(lock.AcquiredAt) {
			return errors.New("invalid approved operation lock")
		}
		if _, found := seen[key]; found {
			return errors.New("duplicate approved operation lock scope")
		}
		if _, found := seenIDs[lock.LockID]; found {
			return errors.New("duplicate approved operation lock identity")
		}
		seen[key] = struct{}{}
		seenIDs[lock.LockID] = struct{}{}
	}
	return nil
}

func validateFinalization(finalization OperationFinalization) error {
	if finalization.OperationID == "" || finalization.PlanID == "" ||
		finalization.ExpectedOperationStatus != "applying" ||
		finalization.ExpectedPlanStatus == "" || finalization.FinishedAt.IsZero() {
		return errors.New("invalid operation finalization identity")
	}
	validPair := false
	switch finalization.OperationStatus + "/" + finalization.PlanStatus {
	case "succeeded/succeeded", "failed/failed", "partial/partial", "blocked/failed", "blocked/stale":
		validPair = true
	}
	if !validPair {
		return errors.New("invalid operation and plan terminal status pair")
	}
	if len(finalization.Locks) == 0 {
		return errors.New("operation finalization requires the exact lock set")
	}
	seenSteps := make(map[string]struct{}, len(finalization.StepTransitions))
	for _, transition := range finalization.StepTransitions {
		if transition.StepID == "" || transition.Expected == "" || transition.Next == "" ||
			(transition.Next != "failed" && transition.Next != "blocked") {
			return errors.New("invalid terminal step transition")
		}
		if _, found := seenSteps[transition.StepID]; found {
			return errors.New("duplicate terminal step transition")
		}
		seenSteps[transition.StepID] = struct{}{}
	}
	for _, event := range finalization.Events {
		if event.EventType == "" || event.OccurredAt.IsZero() {
			return errors.New("invalid operation finalization event")
		}
	}
	return nil
}

func transitionTerminalStepTx(
	ctx context.Context,
	transaction *sql.Tx,
	operationID string,
	transition TerminalStepTransition,
	finishedAt time.Time,
) error {
	beforeRaw, err := optionalJSON(transition.Before)
	if err != nil {
		return err
	}
	afterRaw, err := optionalJSON(transition.After)
	if err != nil {
		return err
	}
	result, err := transaction.ExecContext(
		ctx,
		`UPDATE operation_steps
         SET status = ?,
             before_json = COALESCE(?, before_json),
             after_json = COALESCE(?, after_json),
             last_error = NULLIF(?, ''),
             finished_at = ?
         WHERE operation_id = ? AND step_id = ? AND status = ?`,
		transition.Next, beforeRaw, afterRaw, transition.LastError, formatTime(finishedAt),
		operationID, transition.StepID, transition.Expected,
	)
	if err != nil {
		return fmt.Errorf("finalize operation step %s: %w", transition.StepID, err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	return nil
}

func validateTerminalStepsTx(
	ctx context.Context,
	transaction *sql.Tx,
	finalization OperationFinalization,
) error {
	var total, succeeded, nonterminal int
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT COUNT(*),
                COALESCE(SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END), 0),
                COALESCE(SUM(CASE WHEN status IN ('pending', 'applying', 'compensating') THEN 1 ELSE 0 END), 0)
           FROM operation_steps WHERE operation_id = ?`,
		finalization.OperationID,
	).Scan(&total, &succeeded, &nonterminal); err != nil {
		return fmt.Errorf("inspect terminal operation steps: %w", err)
	}
	if total == 0 || nonterminal != 0 {
		return errors.New("operation finalization requires only terminal steps")
	}
	switch finalization.OperationStatus {
	case "succeeded":
		if succeeded != total {
			return errors.New("succeeded operation requires every step to succeed")
		}
	case "failed", "blocked":
		if succeeded != 0 {
			return errors.New("failed or blocked operation cannot contain succeeded steps")
		}
	}
	return nil
}

func releaseExactOperationLocksTx(
	ctx context.Context,
	transaction *sql.Tx,
	finalization OperationFinalization,
) error {
	var count int
	if err := transaction.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM locks WHERE operation_id = ?`, finalization.OperationID,
	).Scan(&count); err != nil {
		return fmt.Errorf("count operation locks: %w", err)
	}
	if count != len(finalization.Locks) {
		return ErrLockOwnership
	}
	locks := append([]Lock(nil), finalization.Locks...)
	sort.Slice(locks, func(left, right int) bool {
		if locks[left].Scope != locks[right].Scope {
			return locks[left].Scope < locks[right].Scope
		}
		return locks[left].ScopeID < locks[right].ScopeID
	})
	seen := make(map[string]struct{}, len(locks))
	seenIDs := make(map[string]struct{}, len(locks))
	for _, lock := range locks {
		key := lock.Scope + "\x00" + lock.ScopeID
		if lock.OperationID != finalization.OperationID || lock.LockID == "" ||
			lock.DeviceID == "" || lock.SessionID == "" || lock.PID <= 0 || lock.FencingToken <= 0 {
			return ErrLockOwnership
		}
		if _, found := seen[key]; found {
			return ErrLockOwnership
		}
		if _, found := seenIDs[lock.LockID]; found {
			return ErrLockOwnership
		}
		seen[key] = struct{}{}
		seenIDs[lock.LockID] = struct{}{}
		result, err := transaction.ExecContext(
			ctx,
			`DELETE FROM locks
             WHERE scope = ? AND scope_id = ? AND lock_id = ? AND operation_id = ?
               AND device_id = ? AND session_id = ? AND pid = ? AND fencing_token = ?`,
			lock.Scope, lock.ScopeID, lock.LockID, lock.OperationID,
			lock.DeviceID, lock.SessionID, lock.PID, lock.FencingToken,
		)
		if err != nil {
			return fmt.Errorf("release exact operation lock: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return ErrLockOwnership
		}
	}
	return nil
}

func requireOneRow(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect state transition: %w", err)
	}
	if changed != 1 {
		return ErrStateConflict
	}
	return nil
}
