package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type concurrentChecker struct {
	observation Observation
}

func (checker concurrentChecker) Observe(
	_ context.Context,
	_ string,
) (Observation, error) {
	return checker.observation, nil
}

type concurrentHandler struct {
	applyCalls atomic.Int32
}

func (handler *concurrentHandler) Apply(
	context.Context,
	Step,
) (ApplyEvidence, error) {
	handler.applyCalls.Add(1)
	return ApplyEvidence{Before: map[string]any{"state": "before"}, After: map[string]any{"state": "after"}}, nil
}

func (*concurrentHandler) Verify(context.Context, Step, json.RawMessage) error { return nil }

func TestConcurrentApplyHasOneMutationWinner(t *testing.T) {
	base, store, _, _, plan := testEngine(t)
	expected := plan.Preconditions[0]
	checker := concurrentChecker{observation: Observation{
		RepositoryID: expected.RepositoryID, HeadOID: expected.HeadOID,
		WorktreeFingerprint: expected.WorktreeFingerprint,
		IndexTreeOID:        expected.IndexTreeOID, UpstreamOID: expected.UpstreamOID,
		RemoteDefaultOID:     expected.RemoteDefaultOID,
		RemoteEvidenceDigest: expected.RemoteEvidenceDigest,
		ManifestDigest:       expected.ManifestDigest, PolicyDigest: expected.PolicyDigest,
	}}
	handler := &concurrentHandler{}
	engines := []*Engine{}
	for index := 0; index < 2; index++ {
		engine := *base
		engine.Checker = checker
		engine.Handlers = map[string]ActionHandler{"fixture-action": handler}
		engine.SessionID = fmt.Sprintf("concurrent-session-%d", index)
		operationSuffix := index
		engine.NewID = func(prefix string, _ time.Time) (string, error) {
			return fmt.Sprintf("%s_concurrent_%d", prefix, operationSuffix), nil
		}
		engines = append(engines, &engine)
	}
	start := make(chan struct{})
	results := make([]ApplyResult, len(engines))
	errorsSeen := make([]error, len(engines))
	var wait sync.WaitGroup
	for index, engine := range engines {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results[index], errorsSeen[index] = engine.Apply(
				context.Background(), plan.PlanID, "approval:owner:concurrent",
			)
		}()
	}
	close(start)
	wait.Wait()
	if handler.applyCalls.Load() != 1 {
		t.Fatalf("handler apply calls=%d results=%#v errors=%v", handler.applyCalls.Load(), results, errorsSeen)
	}
	operation, err := store.GetOperationByPlan(context.Background(), plan.PlanID)
	if err != nil || operation.Status != "succeeded" {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
	for index, result := range results {
		if errorsSeen[index] == nil && result.OperationID != operation.OperationID {
			t.Fatalf("successful result escaped winning operation: %#v", result)
		}
	}
}
