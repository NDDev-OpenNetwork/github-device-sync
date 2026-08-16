package state

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneControllerDataRemovesOnlyExpiredTerminalRecords(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	old := now.Add(-20 * 24 * time.Hour)
	current := now.Add(-time.Hour)
	completeRetentionWebhook(t, store, "old-delivery", old)
	completeRetentionWebhook(t, store, "current-delivery", current)
	finishRetentionReconciliation(t, store, "old-reconciliation", now.Add(-500*24*time.Hour))
	finishRetentionReconciliation(t, store, "current-reconciliation", current)
	result, err := store.PruneControllerData(context.Background(), now, RetentionPolicy{
		TerminalWebhookAge: 14 * 24 * time.Hour,
		ReconciliationAge:  400 * 24 * time.Hour,
	})
	if err != nil || result.WebhookDeliveries != 1 || result.Reconciliations != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := store.GetWebhook(context.Background(), "old-delivery"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old webhook error=%v", err)
	}
	if _, err := store.GetWebhook(context.Background(), "current-delivery"); err != nil {
		t.Fatalf("current webhook error=%v", err)
	}
	if _, err := store.GetReconciliation(context.Background(), "old-reconciliation"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old reconciliation error=%v", err)
	}
	if _, err := store.GetReconciliation(context.Background(), "current-reconciliation"); err != nil {
		t.Fatalf("current reconciliation error=%v", err)
	}
}

func completeRetentionWebhook(t *testing.T, store *Store, id string, finished time.Time) {
	t.Helper()
	received := finished.Add(-time.Minute)
	if _, err := store.EnqueueWebhook(context.Background(), WebhookDelivery{
		DeliveryID: id, EventType: "push", Payload: json.RawMessage(`{"ref":"main"}`),
		ReceivedAt: received,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimWebhook(
		context.Background(), received, 3, DefaultWebhookProcessingTimeout,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteWebhook(
		context.Background(), id, *claimed.ClaimedAt, true, finished, time.Time{}, 3, "",
	); err != nil {
		t.Fatal(err)
	}
}

func finishRetentionReconciliation(t *testing.T, store *Store, id string, finished time.Time) {
	t.Helper()
	if err := store.StartReconciliation(context.Background(), ReconciliationRecord{
		ReconciliationID: id, Scope: "estate", ScopeID: "estate:test",
		Status: "running", StartedAt: finished.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishReconciliation(
		context.Background(), id, "succeeded", finished, map[string]any{"ok": true}, "",
	); err != nil {
		t.Fatal(err)
	}
}
