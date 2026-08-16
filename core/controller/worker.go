// Package controller coordinates durable asynchronous controller work.
package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type WebhookProcessor interface {
	ProcessWebhook(context.Context, state.WebhookDelivery) error
}

type Worker struct {
	Store             *state.Store
	Processor         WebhookProcessor
	Now               func() time.Time
	MaxAttempts       int
	ProcessingTimeout time.Duration
	Backoff           func(attempt int) time.Duration
}

type WorkerResult struct {
	Processed  bool   `json:"processed"`
	DeliveryID string `json:"delivery_id,omitempty"`
	Status     string `json:"status,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
}

type PermanentError struct{ Err error }

func (permanent *PermanentError) Error() string { return permanent.Err.Error() }
func (permanent *PermanentError) Unwrap() error { return permanent.Err }

type SafeError interface {
	SafeMessage() string
}

func (worker *Worker) RunOnce(ctx context.Context) (WorkerResult, error) {
	if worker.Store == nil || worker.Processor == nil {
		return WorkerResult{}, fmt.Errorf("webhook worker requires state and processor")
	}
	maxAttempts := worker.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 5
	}
	if maxAttempts < 1 || maxAttempts > 20 {
		return WorkerResult{}, fmt.Errorf("webhook max attempts must be between 1 and 20")
	}
	processingTimeout := worker.ProcessingTimeout
	if processingTimeout == 0 {
		processingTimeout = state.DefaultWebhookProcessingTimeout
	}
	if processingTimeout < state.MinWebhookProcessingTimeout ||
		processingTimeout > state.MaxWebhookProcessingTimeout {
		return WorkerResult{}, fmt.Errorf(
			"webhook processing timeout must be between one minute and 24 hours",
		)
	}
	now := time.Now().UTC()
	if worker.Now != nil {
		now = worker.Now().UTC()
	}
	delivery, err := worker.Store.ClaimWebhook(ctx, now, maxAttempts, processingTimeout)
	if errors.Is(err, state.ErrNotFound) {
		return WorkerResult{}, nil
	}
	if err != nil {
		return WorkerResult{}, err
	}
	result := WorkerResult{
		Processed: true, DeliveryID: delivery.DeliveryID,
		Status: "processing", Attempt: delivery.AttemptCount,
	}
	if delivery.ClaimedAt == nil {
		return result, fmt.Errorf("claimed webhook is missing its claim identity")
	}
	processErr := worker.Processor.ProcessWebhook(ctx, delivery)
	completedAt := time.Now().UTC()
	if worker.Now != nil {
		completedAt = worker.Now().UTC()
	}
	completionContext := context.WithoutCancel(ctx)
	if processErr == nil {
		if err := worker.Store.CompleteWebhook(
			completionContext, delivery.DeliveryID, *delivery.ClaimedAt, true, completedAt,
			time.Time{}, maxAttempts, "",
		); err != nil {
			return result, err
		}
		result.Status = "succeeded"
		return result, nil
	}
	backoff := worker.Backoff
	if backoff == nil {
		backoff = boundedBackoff
	}
	retryAt := completedAt.Add(backoff(delivery.AttemptCount))
	var permanent *PermanentError
	completionLimit := maxAttempts
	if errors.As(processErr, &permanent) {
		completionLimit = delivery.AttemptCount
		retryAt = time.Time{}
	}
	if err := worker.Store.CompleteWebhook(
		completionContext, delivery.DeliveryID, *delivery.ClaimedAt, false, completedAt,
		retryAt, completionLimit, sanitizeWorkerError(processErr),
	); err != nil {
		return result, err
	}
	if delivery.AttemptCount >= completionLimit {
		result.Status = "dead-letter"
	} else {
		result.Status = "failed"
	}
	return result, nil
}

func boundedBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 8)
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func sanitizeWorkerError(err error) string {
	if err == nil {
		return ""
	}
	value := "webhook processor failed"
	var safe SafeError
	if errors.As(err, &safe) {
		value = safe.SafeMessage()
	}
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
