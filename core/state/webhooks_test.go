package state

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func webhookFixture(id string) WebhookDelivery {
	return WebhookDelivery{
		DeliveryID: id, EventType: "push",
		Payload:    json.RawMessage(`{"ref":"refs/heads/main"}`),
		ReceivedAt: testTime,
	}
}

func TestWebhookEnqueueIsIdempotentAndRejectsDigestConflict(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	delivery := webhookFixture("delivery-1")
	inserted, err := store.EnqueueWebhook(ctx, delivery)
	if err != nil || !inserted {
		t.Fatalf("first enqueue inserted=%v err=%v", inserted, err)
	}
	inserted, err = store.EnqueueWebhook(ctx, delivery)
	if err != nil || inserted {
		t.Fatalf("duplicate enqueue inserted=%v err=%v", inserted, err)
	}
	conflict := delivery
	conflict.Payload = json.RawMessage(`{"ref":"refs/heads/other"}`)
	if _, err := store.EnqueueWebhook(ctx, conflict); !errors.Is(err, ErrWebhookConflict) {
		t.Fatalf("conflicting delivery error=%v", err)
	}
	if _, err := store.db.ExecContext(
		ctx, `UPDATE webhook_deliveries SET payload_json = '{}' WHERE delivery_id = ?`,
		delivery.DeliveryID,
	); err == nil {
		t.Fatal("immutable webhook payload was updated")
	}
}

func TestWebhookClaimRetryAndCompletion(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, webhookFixture("delivery-2")); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimWebhook(ctx, testTime, 3, DefaultWebhookProcessingTimeout)
	if err != nil || claimed.AttemptCount != 1 || claimed.Status != "processing" {
		t.Fatalf("claimed=%#v err=%v", claimed, err)
	}
	retryAt := testTime.Add(time.Minute)
	if err := store.CompleteWebhook(
		ctx, claimed.DeliveryID, *claimed.ClaimedAt, false,
		testTime.Add(time.Second), retryAt, 3, "transient",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimWebhook(
		ctx, retryAt.Add(-time.Second), 3, DefaultWebhookProcessingTimeout,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("webhook claimed before retry: %v", err)
	}
	claimed, err = store.ClaimWebhook(ctx, retryAt, 3, DefaultWebhookProcessingTimeout)
	if err != nil || claimed.AttemptCount != 2 {
		t.Fatalf("retry claim=%#v err=%v", claimed, err)
	}
	if err := store.CompleteWebhook(
		ctx, claimed.DeliveryID, *claimed.ClaimedAt, true,
		retryAt.Add(time.Second), time.Time{}, 3, "",
	); err != nil {
		t.Fatal(err)
	}
	completed, err := store.GetWebhook(ctx, claimed.DeliveryID)
	if err != nil || completed.Status != "succeeded" || completed.FinishedAt == nil {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
}

func TestWebhookMovesToDeadLetterAtAttemptLimit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, webhookFixture("delivery-3")); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimWebhook(ctx, testTime, 1, DefaultWebhookProcessingTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteWebhook(
		ctx, claimed.DeliveryID, *claimed.ClaimedAt, false,
		testTime.Add(time.Second), time.Time{}, 1, "permanent",
	); err != nil {
		t.Fatal(err)
	}
	completed, err := store.GetWebhook(ctx, claimed.DeliveryID)
	if err != nil || completed.Status != "dead-letter" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
}

func TestWebhookDeadLettersFailedDeliveryWhenAttemptLimitDecreases(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const originalMaxAttempts = 5
	if _, err := store.EnqueueWebhook(ctx, webhookFixture("delivery-limit-decrease")); err != nil {
		t.Fatal(err)
	}
	now := testTime
	for attempt := 1; attempt <= 4; attempt++ {
		claimed, err := store.ClaimWebhook(
			ctx, now, originalMaxAttempts, DefaultWebhookProcessingTimeout,
		)
		if err != nil || claimed.AttemptCount != attempt {
			t.Fatalf("attempt %d claim=%#v err=%v", attempt, claimed, err)
		}
		retryAt := now.Add(time.Minute)
		if err := store.CompleteWebhook(
			ctx, claimed.DeliveryID, *claimed.ClaimedAt, false,
			now.Add(time.Second), retryAt, originalMaxAttempts, "transient",
		); err != nil {
			t.Fatal(err)
		}
		now = retryAt
	}
	if _, err := store.ClaimWebhook(
		ctx, now, 3, DefaultWebhookProcessingTimeout,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim after limit decrease error=%v, want ErrNotFound", err)
	}
	delivery, err := store.GetWebhook(ctx, "delivery-limit-decrease")
	if err != nil || delivery.Status != "dead-letter" || delivery.AttemptCount != 4 ||
		delivery.FinishedAt == nil || !delivery.FinishedAt.Equal(now) ||
		delivery.LastError != "transient" {
		t.Fatalf("dead-letter delivery=%#v err=%v", delivery, err)
	}
	summary, err := store.WebhookQueueSummary(ctx)
	if err != nil || summary.Failed != 0 || summary.DeadLetter != 1 {
		t.Fatalf("queue summary=%#v err=%v", summary, err)
	}
}

func TestConcurrentWebhookClaimHasOneWinner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, webhookFixture("delivery-4")); err != nil {
		t.Fatal(err)
	}
	const workers = 6
	var wait sync.WaitGroup
	wait.Add(workers)
	results := make(chan error, workers)
	for range workers {
		go func() {
			defer wait.Done()
			_, err := store.ClaimWebhook(ctx, testTime, 3, DefaultWebhookProcessingTimeout)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatalf("claim error=%v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners=%d", winners)
	}
}

func TestWebhookAbandonedClaimReclaimsAtVisibilityBoundaryAfterReopen(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, webhookFixture("delivery-reopen")); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimWebhook(ctx, testTime, 3, DefaultWebhookProcessingTimeout)
	if err != nil {
		t.Fatal(err)
	}
	path := store.Path()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	boundary := testTime.Add(DefaultWebhookProcessingTimeout)
	if _, err := reopened.ClaimWebhook(
		ctx, boundary.Add(-time.Nanosecond), 3, DefaultWebhookProcessingTimeout,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("abandoned claim reclaimed before timeout: %v", err)
	}
	second, err := reopened.ClaimWebhook(ctx, boundary, 3, DefaultWebhookProcessingTimeout)
	if err != nil || second.AttemptCount != 2 || second.ClaimedAt == nil ||
		!second.ClaimedAt.Equal(boundary) {
		t.Fatalf("reclaimed=%#v err=%v", second, err)
	}
	if err := reopened.CompleteWebhook(
		ctx, first.DeliveryID, *first.ClaimedAt, true,
		boundary.Add(time.Second), time.Time{}, 3, "",
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("previous claim completion error=%v", err)
	}
	if err := reopened.CompleteWebhook(
		ctx, first.DeliveryID, *first.ClaimedAt, false,
		boundary.Add(time.Second), boundary.Add(time.Minute), 3, "expired claim",
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("previous claim failure completion error=%v", err)
	}
	if err := reopened.CompleteWebhook(
		ctx, second.DeliveryID, *second.ClaimedAt, true,
		boundary.Add(time.Second), time.Time{}, 3, "",
	); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookClaimRejectsProcessingTimeoutOutsideBounds(t *testing.T) {
	store := newTestStore(t)
	for _, processingTimeout := range []time.Duration{
		0,
		MinWebhookProcessingTimeout - time.Nanosecond,
		MaxWebhookProcessingTimeout + time.Nanosecond,
	} {
		if _, err := store.ClaimWebhook(
			context.Background(), testTime, 3, processingTimeout,
		); err == nil {
			t.Fatalf("processing timeout %s was accepted", processingTimeout)
		}
	}
}

func TestWebhookAbandonedClaimDeadLettersAtAttemptLimit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, webhookFixture("delivery-expired")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimWebhook(ctx, testTime, 1, DefaultWebhookProcessingTimeout); err != nil {
		t.Fatal(err)
	}
	expiredAt := testTime.Add(DefaultWebhookProcessingTimeout)
	if _, err := store.ClaimWebhook(
		ctx, expiredAt, 1, DefaultWebhookProcessingTimeout,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exhausted claim result=%v", err)
	}
	delivery, err := store.GetWebhook(ctx, "delivery-expired")
	if err != nil || delivery.Status != "dead-letter" || delivery.FinishedAt == nil ||
		!delivery.FinishedAt.Equal(expiredAt) || delivery.LastError == "" {
		t.Fatalf("expired delivery=%#v err=%v", delivery, err)
	}
}

func TestConcurrentWebhookReclaimHasOneWinner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, webhookFixture("delivery-reclaim")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimWebhook(ctx, testTime, 3, DefaultWebhookProcessingTimeout); err != nil {
		t.Fatal(err)
	}
	peer, err := Open(ctx, store.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	type claimResult struct {
		delivery WebhookDelivery
		err      error
	}
	results := make(chan claimResult, 2)
	boundary := testTime.Add(DefaultWebhookProcessingTimeout)
	for _, candidate := range []*Store{store, peer} {
		go func(candidate *Store) {
			delivery, claimErr := candidate.ClaimWebhook(
				ctx, boundary, 3, DefaultWebhookProcessingTimeout,
			)
			results <- claimResult{delivery: delivery, err: claimErr}
		}(candidate)
	}
	winners := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			winners++
			if result.delivery.AttemptCount != 2 || result.delivery.ClaimedAt == nil ||
				!result.delivery.ClaimedAt.Equal(boundary) {
				t.Fatalf("winner=%#v", result.delivery)
			}
		} else if !errors.Is(result.err, ErrNotFound) {
			t.Fatalf("reclaim error=%v", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("reclaim winners=%d", winners)
	}
}
