package harness

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// claudeAgent implements RuntimeAgent for the Claude Code CLI. It carries the
// two isolated fixtures prepared for a single evidence run so Run can reuse
// them. Consistency with codex comes from the shared RunEvidenceDriver engine
// and the shared task/case/metric vocabulary; isolation comes from minting
// Claude-specific fixtures and a private environment per turn.
type claudeAgent struct {
	fixture  ClaudeRuntimeFixture
	baseline ClaudeRuntimeBaseFixture
}

func (a *claudeAgent) Harness() string { return "claude-code" }

func (a *claudeAgent) JudgePolicy() string { return claudeJudgePolicy }

func (a *claudeAgent) Validate(request RuntimeDriverRequest) error {
	return validateClaudeDriverRequest(request)
}

func (a *claudeAgent) Prepare(
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
	fixture, err := PrepareClaudeRuntimeFixture(
		ctx, request.RepositoryRoot, request.EvidenceDirectory, request.SkillProfile, schemas,
	)
	if err != nil {
		return "", nil, err
	}
	baseline, err := PrepareClaudeRuntimeBareFixture(
		ctx, request.RepositoryRoot, request.EvidenceDirectory,
	)
	if err != nil {
		return "", nil, err
	}
	requestDigest, err := claudeDriverInputDigest(request, requestRaw, driverRaw, fixture)
	if err != nil {
		return "", nil, err
	}
	tasks, err := buildClaudeDriverTasks(request, fixture, baseline)
	if err != nil {
		return "", nil, err
	}
	a.fixture, a.baseline = fixture, baseline
	return requestDigest, tasks, nil
}

func (a *claudeAgent) Phase(task runtimeDriverTask) int {
	if task.Kind == codexTaskOutputSkill || task.Kind == codexTaskEnforcement {
		return 2
	}
	return 1
}

func (a *claudeAgent) Run(
	ctx context.Context,
	request RuntimeDriverRequest,
	requestDigest string,
	task runtimeDriverTask,
	dependencies map[string]runtimeDriverAttempt,
) (runtimeDriverAttempt, error) {
	return runClaudeDriverTask(ctx, request, requestDigest, a.fixture, a.baseline, task, dependencies)
}

func (a *claudeAgent) Cases(attempts []runtimeDriverAttempt) []EvalCaseResult {
	return buildCodexDriverCases(attempts)
}

func (a *claudeAgent) Metrics(attempts []runtimeDriverAttempt) []EvalMetric {
	return buildCodexDriverMetrics(attempts)
}
