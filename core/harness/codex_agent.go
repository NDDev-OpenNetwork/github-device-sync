package harness

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// codexAgent implements RuntimeAgent for the Codex CLI. It carries the two
// isolated fixtures prepared for a single evidence run so Run can reuse them.
type codexAgent struct {
	fixture  CodexRuntimeFixture
	baseline CodexRuntimeBaseFixture
}

func (a *codexAgent) Harness() string { return "codex" }

func (a *codexAgent) JudgePolicy() string { return codexJudgePolicy }

func (a *codexAgent) Validate(request RuntimeDriverRequest) error {
	return validateCodexDriverRequest(request)
}

func (a *codexAgent) Prepare(
	ctx context.Context,
	request RuntimeDriverRequest,
	driverRaw []byte,
	schemas *validation.Set,
) (string, []runtimeDriverTask, error) {
	requestRaw, err := json.Marshal(request)
	if err != nil {
		return "", nil, err
	}
	for _, name := range []string{"fixture", "baseline-fixture", "public-consumer"} {
		path := filepath.Join(request.EvidenceDirectory, name)
		if err := removeOwnedCodexFixture(request.EvidenceDirectory, path); err != nil {
			return "", nil, err
		}
	}
	fixture, err := PrepareCodexRuntimeFixture(
		ctx, request.RepositoryRoot, request.EvidenceDirectory, request.SkillProfile, schemas,
	)
	if err != nil {
		return "", nil, err
	}
	baseline, err := PrepareCodexRuntimeBareFixture(
		ctx, request.RepositoryRoot, request.EvidenceDirectory,
	)
	if err != nil {
		return "", nil, err
	}
	requestDigest, err := codexDriverInputDigest(request, requestRaw, driverRaw, fixture)
	if err != nil {
		return "", nil, err
	}
	tasks, err := buildCodexDriverTasks(request, fixture, baseline)
	if err != nil {
		return "", nil, err
	}
	a.fixture, a.baseline = fixture, baseline
	return requestDigest, tasks, nil
}

func (a *codexAgent) Phase(task runtimeDriverTask) int {
	if task.Kind == codexTaskOutputSkill || task.Kind == codexTaskEnforcement {
		return 2
	}
	return 1
}

func (a *codexAgent) Run(
	ctx context.Context,
	request RuntimeDriverRequest,
	requestDigest string,
	task runtimeDriverTask,
	dependencies map[string]runtimeDriverAttempt,
) (runtimeDriverAttempt, error) {
	return runCodexDriverTask(ctx, request, requestDigest, a.fixture, a.baseline, task, dependencies)
}

func (a *codexAgent) Cases(attempts []runtimeDriverAttempt) []EvalCaseResult {
	return buildCodexDriverCases(attempts)
}

func (a *codexAgent) Metrics(attempts []runtimeDriverAttempt) []EvalMetric {
	return buildCodexDriverMetrics(attempts)
}
