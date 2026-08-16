package github

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Scheduler struct {
	mu          sync.Mutex
	concurrency int
	now         func() time.Time
	buckets     map[string]*schedulerBucket
}

type schedulerBucket struct {
	semaphore    chan struct{}
	blockedUntil time.Time
}

func NewScheduler(concurrency int, now func() time.Time) (*Scheduler, error) {
	if concurrency < 1 || concurrency > 16 {
		return nil, fmt.Errorf("GitHub read concurrency must be between 1 and 16")
	}
	if now == nil {
		now = time.Now
	}
	return &Scheduler{
		concurrency: concurrency, now: now, buckets: map[string]*schedulerBucket{},
	}, nil
}

func (scheduler *Scheduler) Acquire(
	ctx context.Context,
	installationID string,
) (func(), error) {
	if installationID == "" {
		return nil, fmt.Errorf("GitHub installation identity is empty")
	}
	bucket := scheduler.bucket(installationID)
	for {
		if err := scheduler.waitUntilUnblocked(ctx, bucket); err != nil {
			return nil, err
		}
		select {
		case bucket.semaphore <- struct{}{}:
			if scheduler.blockDelay(bucket) > 0 {
				<-bucket.semaphore
				continue
			}
			var once sync.Once
			return func() { once.Do(func() { <-bucket.semaphore }) }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (scheduler *Scheduler) waitUntilUnblocked(ctx context.Context, bucket *schedulerBucket) error {
	for {
		delay := scheduler.blockDelay(bucket)
		if delay <= 0 {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (scheduler *Scheduler) blockDelay(bucket *schedulerBucket) time.Duration {
	scheduler.mu.Lock()
	blockedUntil := bucket.blockedUntil
	scheduler.mu.Unlock()
	return blockedUntil.Sub(scheduler.now())
}

// maxSchedulerBlock caps how long a single backoff signal may stall an
// installation bucket. GitHub rate-limit reset windows are typically well
// under an hour; a value above that indicates a misconfigured or hostile
// upstream and is clamped to avoid stalling reconciliation indefinitely.
const maxSchedulerBlock = time.Hour

func (scheduler *Scheduler) Observe(
	installationID string,
	statusCode int,
	meta ResponseMeta,
) {
	bucket := scheduler.bucket(installationID)
	now := scheduler.now()
	blockCeiling := now.Add(maxSchedulerBlock)
	blockedUntil := time.Time{}
	if meta.RetryAfter > 0 {
		blockedUntil = now.Add(meta.RetryAfter)
	} else if meta.Rate.Known && meta.Rate.Remaining == 0 && !meta.Rate.ResetAt.IsZero() {
		blockedUntil = meta.Rate.ResetAt
	} else if statusCode == 429 {
		blockedUntil = now.Add(time.Minute)
	}
	if blockedUntil.IsZero() {
		return
	}
	if blockedUntil.After(blockCeiling) {
		blockedUntil = blockCeiling
	}
	scheduler.mu.Lock()
	if blockedUntil.After(bucket.blockedUntil) {
		bucket.blockedUntil = blockedUntil
	}
	scheduler.mu.Unlock()
}

func (scheduler *Scheduler) bucket(installationID string) *schedulerBucket {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	bucket := scheduler.buckets[installationID]
	if bucket == nil {
		bucket = &schedulerBucket{semaphore: make(chan struct{}, scheduler.concurrency)}
		scheduler.buckets[installationID] = bucket
	}
	return bucket
}
