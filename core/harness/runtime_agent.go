package harness

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// RuntimeAgent is the per-harness seam of the native runtime-evidence driver.
//
// The generic engine (RunEvidenceDriver) owns the harness-agnostic protocol: the
// two-phase concurrent worker pool, checkpoint resume, transcript assembly, the
// judge identity, and the evidence digest. Each harness (codex today, claude
// next) supplies only its tool-specific behavior through this interface.
// Consistency comes from every harness reusing the same engine; isolation comes
// from each agent minting its own fixtures and environment in Prepare/Run.
type RuntimeAgent interface {
	// Harness is the canonical harness id this agent proves evidence for.
	Harness() string
	// JudgePolicy is the exact semantic-judge rubric prompt for this harness.
	JudgePolicy() string
	// Validate rejects a request this agent cannot serve.
	Validate(request RuntimeDriverRequest) error
	// Prepare resets and builds this harness's isolated fixtures, binds the
	// request identity digest, and returns the ordered tasks to execute. An agent
	// may store prepared state on itself for Run to consume.
	Prepare(
		ctx context.Context,
		request RuntimeDriverRequest,
		driverRaw []byte,
		schemas *validation.Set,
	) (requestDigest string, tasks []runtimeDriverTask, err error)
	// Phase returns 1 or 2. Phase-2 tasks may depend on phase-1 baselines.
	Phase(task runtimeDriverTask) int
	// Run executes one fresh task (not resumed from a checkpoint).
	Run(
		ctx context.Context,
		request RuntimeDriverRequest,
		requestDigest string,
		task runtimeDriverTask,
		dependencies map[string]runtimeDriverAttempt,
	) (runtimeDriverAttempt, error)
	// Cases and Metrics assemble the evidence case/metric sets from the ordered
	// attempts, in the exact vocabulary this harness proves.
	Cases(attempts []runtimeDriverAttempt) []EvalCaseResult
	Metrics(attempts []runtimeDriverAttempt) []EvalMetric
}

// EvidenceDriverOptions bound the generic evidence engine.
type EvidenceDriverOptions struct {
	Concurrency int
	Now         func() time.Time
}

// RunEvidenceDriver produces native runtime evidence for one harness by driving
// its RuntimeAgent through the deterministic two-phase, checkpoint-resumable
// evaluation protocol. It is harness-agnostic: every tool-specific behavior lives
// behind the agent.
func RunEvidenceDriver(
	ctx context.Context,
	request RuntimeDriverRequest,
	schemas *validation.Set,
	agent RuntimeAgent,
	options EvidenceDriverOptions,
) (RuntimeEvidence, error) {
	if schemas == nil {
		return RuntimeEvidence{}, fmt.Errorf("schema set is required")
	}
	if err := agent.Validate(request); err != nil {
		return RuntimeEvidence{}, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	started := now().UTC()
	driverExecutable, err := os.Executable()
	if err != nil {
		return RuntimeEvidence{}, fmt.Errorf("resolve runtime driver executable: %w", err)
	}
	driverRaw, err := readBoundedRegular(driverExecutable, 128<<20)
	if err != nil {
		return RuntimeEvidence{}, fmt.Errorf("read runtime driver executable: %w", err)
	}
	requestDigest, tasks, err := agent.Prepare(ctx, request, driverRaw, schemas)
	if err != nil {
		return RuntimeEvidence{}, err
	}

	attempts := map[string]runtimeDriverAttempt{}
	var attemptsMu sync.Mutex
	runPhase := func(selected []runtimeDriverTask, dependencies map[string]runtimeDriverAttempt) error {
		concurrency := options.Concurrency
		if concurrency <= 0 {
			concurrency = 2
		}
		if concurrency > 4 {
			return fmt.Errorf("runtime evidence driver concurrency cannot exceed four")
		}
		jobs := make(chan runtimeDriverTask)
		errs := make(chan error, len(selected))
		var workers sync.WaitGroup
		for worker := 0; worker < concurrency; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for task := range jobs {
					attempt, found, err := loadRuntimeDriverAttempt(
						request.EvidenceDirectory, requestDigest, task,
					)
					if err == nil && !found {
						attempt, err = agent.Run(ctx, request, requestDigest, task, dependencies)
						if err == nil {
							attempt, err = persistRuntimeDriverAttempt(request.EvidenceDirectory, attempt)
						}
					}
					if err != nil {
						errs <- fmt.Errorf("%s/%s/%d: %w", task.MetricID, task.SampleID, task.RunIndex, err)
						continue
					}
					attempt.Task = &task
					attemptsMu.Lock()
					attempts[runtimeDriverTaskKey(task)] = attempt
					attemptsMu.Unlock()
				}
			}()
		}
		for _, task := range selected {
			jobs <- task
		}
		close(jobs)
		workers.Wait()
		close(errs)
		for err := range errs {
			return err
		}
		return nil
	}

	phaseOne := []runtimeDriverTask{}
	phaseTwo := []runtimeDriverTask{}
	for _, task := range tasks {
		if agent.Phase(task) == 2 {
			phaseTwo = append(phaseTwo, task)
		} else {
			phaseOne = append(phaseOne, task)
		}
	}
	if err := runPhase(phaseOne, map[string]runtimeDriverAttempt{}); err != nil {
		return RuntimeEvidence{}, err
	}
	dependencies := make(map[string]runtimeDriverAttempt, len(attempts))
	for key, attempt := range attempts {
		dependencies[key] = attempt
	}
	if err := runPhase(phaseTwo, dependencies); err != nil {
		return RuntimeEvidence{}, err
	}

	orderedAttempts := make([]runtimeDriverAttempt, 0, len(tasks))
	for _, task := range tasks {
		attempt, found := attempts[runtimeDriverTaskKey(task)]
		if !found {
			return RuntimeEvidence{}, fmt.Errorf("runtime attempt is missing after execution: %s", runtimeDriverTaskKey(task))
		}
		orderedAttempts = append(orderedAttempts, attempt)
	}
	transcripts := make([]EvalTranscript, 0, len(orderedAttempts))
	for _, attempt := range orderedAttempts {
		metricID, sampleID, runIndex := attempt.MetricID, attempt.SampleID, attempt.RunIndex
		passed := attempt.Passed
		mutationAttempted, mutationCompleted := attempt.MutationAttempted, attempt.MutationCompleted
		transcripts = append(transcripts, EvalTranscript{
			CaseID: attempt.CaseID, MetricID: &metricID, SampleID: &sampleID,
			RunIndex: &runIndex, Passed: &passed,
			MutationAttempted: &mutationAttempted, MutationCompleted: &mutationCompleted,
			Reference: attempt.Reference, Digest: attempt.Digest, Bytes: attempt.Bytes,
		})
	}
	judgeID, err := identity.New("judge", started, rand.Reader)
	if err != nil {
		return RuntimeEvidence{}, err
	}
	evidence := RuntimeEvidence{
		SchemaVersion: 1, ContractVersion: request.ContractVersion,
		Harness: request.Harness, HarnessVersion: request.HarnessVersion,
		ModelLabel: request.ModelLabel, ExecutionProfile: request.ExecutionProfile,
		Tools: append([]string(nil), request.Tools...), Environment: request.Environment,
		Judge: RuntimeEvidenceJudge{
			RunID: judgeID, Harness: agent.Harness(), HarnessVersion: request.HarnessVersion,
			ModelLabel: request.ModelLabel, ExecutionProfile: request.ExecutionProfile,
			Tools: append([]string(nil), request.Tools...), PromptDigest: bytesDigest([]byte(agent.JudgePolicy())),
		},
		ProfileDigest: request.ProfileDigest, StartedAt: started, FinishedAt: now().UTC(),
		Cases:   agent.Cases(orderedAttempts),
		Metrics: agent.Metrics(orderedAttempts), Transcripts: transcripts,
	}
	evidence.ResultDigest, err = runtimeEvidenceDigest(evidence)
	if err != nil {
		return RuntimeEvidence{}, err
	}
	return evidence, nil
}
