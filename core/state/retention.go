package state

import (
	"context"
	"fmt"
	"time"
)

type RetentionPolicy struct {
	TerminalWebhookAge time.Duration
	ReconciliationAge  time.Duration
}

type RetentionResult struct {
	WebhookDeliveries int64 `json:"webhook_deliveries"`
	Reconciliations   int64 `json:"reconciliations"`
}

func (store *Store) PruneControllerData(
	ctx context.Context,
	now time.Time,
	policy RetentionPolicy,
) (RetentionResult, error) {
	if store.readOnly {
		return RetentionResult{}, ErrReadOnly
	}
	if now.IsZero() || policy.TerminalWebhookAge < 24*time.Hour ||
		policy.TerminalWebhookAge > 90*24*time.Hour ||
		policy.ReconciliationAge < 30*24*time.Hour ||
		policy.ReconciliationAge > 3650*24*time.Hour {
		return RetentionResult{}, fmt.Errorf("controller retention policy is outside safe bounds")
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("begin controller retention: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = transaction.Rollback()
		}
	}()
	webhookResult, err := transaction.ExecContext(
		ctx,
		`DELETE FROM webhook_deliveries
         WHERE status IN ('succeeded', 'dead-letter')
           AND finished_at IS NOT NULL
           AND julianday(finished_at) < julianday(?)`,
		formatTime(now.Add(-policy.TerminalWebhookAge)),
	)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("prune terminal webhook deliveries: %w", err)
	}
	reconciliationResult, err := transaction.ExecContext(
		ctx,
		`DELETE FROM reconciliation_runs
         WHERE status IN ('succeeded', 'failed', 'partial', 'blocked')
           AND finished_at IS NOT NULL
           AND julianday(finished_at) < julianday(?)`,
		formatTime(now.Add(-policy.ReconciliationAge)),
	)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("prune reconciliation journals: %w", err)
	}
	webhooks, err := webhookResult.RowsAffected()
	if err != nil {
		return RetentionResult{}, err
	}
	reconciliations, err := reconciliationResult.RowsAffected()
	if err != nil {
		return RetentionResult{}, err
	}
	if err := store.commit(transaction); err != nil {
		return RetentionResult{}, fmt.Errorf("commit controller retention: %w", err)
	}
	rollback = false
	return RetentionResult{
		WebhookDeliveries: webhooks, Reconciliations: reconciliations,
	}, nil
}
