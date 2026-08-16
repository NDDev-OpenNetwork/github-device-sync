package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type processorFunc func(context.Context, state.WebhookDelivery) error

func (function processorFunc) ProcessWebhook(
	ctx context.Context,
	delivery state.WebhookDelivery,
) error {
	return function(ctx, delivery)
}

type safeProcessorError struct {
	safe   string
	secret string
}

func (processorError safeProcessorError) Error() string       { return processorError.secret }
func (processorError safeProcessorError) SafeMessage() string { return processorError.safe }

func TestWorkerCompletesSuccessfulDelivery(t *testing.T) {
	store, now := workerStore(t)
	enqueueWorkerDelivery(t, store, *now, "delivery-success")
	calls := 0
	worker := Worker{
		Store: store, Now: func() time.Time { return *now },
		Processor: processorFunc(func(_ context.Context, delivery state.WebhookDelivery) error {
			calls++
			if delivery.DeliveryID != "delivery-success" {
				t.Fatalf("delivery=%#v", delivery)
			}
			return nil
		}),
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil || !result.Processed || result.Status != "succeeded" || calls != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", result, calls, err)
	}
	completed, err := store.GetWebhook(context.Background(), "delivery-success")
	if err != nil || completed.Status != "succeeded" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
}

func TestWorkerRetriesTransientFailureThenSucceeds(t *testing.T) {
	store, now := workerStore(t)
	enqueueWorkerDelivery(t, store, *now, "delivery-retry")
	calls := 0
	worker := Worker{
		Store: store, Now: func() time.Time { return *now }, MaxAttempts: 3,
		Backoff: func(int) time.Duration { return time.Minute },
		Processor: processorFunc(func(context.Context, state.WebhookDelivery) error {
			calls++
			if calls == 1 {
				return errors.New("temporary private detail")
			}
			return nil
		}),
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Status != "failed" {
		t.Fatalf("first result=%#v err=%v", result, err)
	}
	failed, _ := store.GetWebhook(context.Background(), "delivery-retry")
	if failed.LastError != "webhook processor failed" {
		t.Fatalf("unsafe worker error persisted: %q", failed.LastError)
	}
	*now = now.Add(time.Minute)
	result, err = worker.RunOnce(context.Background())
	if err != nil || result.Status != "succeeded" || calls != 2 {
		t.Fatalf("retry result=%#v calls=%d err=%v", result, calls, err)
	}
}

func TestWorkerDeadLettersPermanentFailureWithSafeMessage(t *testing.T) {
	store, now := workerStore(t)
	enqueueWorkerDelivery(t, store, *now, "delivery-dead")
	worker := Worker{
		Store: store, Now: func() time.Time { return *now }, MaxAttempts: 5,
		Processor: processorFunc(func(context.Context, state.WebhookDelivery) error {
			return &PermanentError{Err: safeProcessorError{
				safe: "unsupported verified event shape", secret: "secret payload detail",
			}}
		}),
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Status != "dead-letter" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	dead, _ := store.GetWebhook(context.Background(), "delivery-dead")
	if dead.Status != "dead-letter" || dead.LastError != "unsupported verified event shape" {
		t.Fatalf("dead=%#v", dead)
	}
}

func TestWorkerReturnsInfrastructureErrorWhenCompletionCannotPersist(t *testing.T) {
	store, now := workerStore(t)
	enqueueWorkerDelivery(t, store, *now, "delivery-storage-error")
	worker := Worker{
		Store: store, Now: func() time.Time { return *now },
		Processor: processorFunc(func(context.Context, state.WebhookDelivery) error {
			return store.Close()
		}),
	}
	result, err := worker.RunOnce(context.Background())
	if err == nil || !result.Processed || result.Status != "processing" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestWorkerReturnsEmptyResultForEmptyQueue(t *testing.T) {
	store, now := workerStore(t)
	worker := Worker{
		Store: store, Now: func() time.Time { return *now },
		Processor: processorFunc(func(context.Context, state.WebhookDelivery) error {
			t.Fatal("processor called for empty queue")
			return nil
		}),
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Processed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func workerStore(t *testing.T) (*state.Store, *time.Time) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	return store, &now
}

func enqueueWorkerDelivery(t *testing.T, store *state.Store, now time.Time, id string) {
	t.Helper()
	if _, err := store.EnqueueWebhook(context.Background(), state.WebhookDelivery{
		DeliveryID: id, EventType: "push", Payload: json.RawMessage(`{"ref":"main"}`),
		ReceivedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}
