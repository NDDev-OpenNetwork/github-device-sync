package harness

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// zcodeAgent implements RuntimeAgent for the zcode CLI. It carries the two
// isolated fixtures prepared for a single evidence run so Run can reuse them.
// Consistency with codex and claude comes from the shared RunEvidenceDriver
// engine and the shared task/case/metric vocabulary; isolation comes from
// minting zcode-specific fixtures and a private ZCODE_HOME per turn. zcode
// declares no hook support, so it proves the six-case set (no hook-lifecycle).
type zcodeAgent struct {
	fixture  ZcodeRuntimeFixture
	baseline ZcodeRuntimeBaseFixture
}

func (a *zcodeAgent) Harness() string { return "zcode" }

func (a *zcodeAgent) JudgePolicy() string { return zcodeJudgePolicy }

func (a *zcodeAgent) Validate(request RuntimeDriverRequest) error {
	return validateZcodeDriverRequest(request)
}

func (a *zcodeAgent) Prepare(
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
	fixture, err := PrepareZcodeRuntimeFixture(
		ctx, request.RepositoryRoot, request.EvidenceDirectory, request.SkillProfile, schemas,
	)
	if err != nil {
		return "", nil, err
	}
	baseline, err := PrepareZcodeRuntimeBareFixture(
		ctx, request.RepositoryRoot, request.EvidenceDirectory,
	)
	if err != nil {
		return "", nil, err
	}
	requestDigest, err := zcodeDriverInputDigest(request, requestRaw, driverRaw, fixture)
	if err != nil {
		return "", nil, err
	}
	tasks, err := buildZcodeDriverTasks(request, fixture, baseline)
	if err != nil {
		return "", nil, err
	}
	a.fixture, a.baseline = fixture, baseline
	return requestDigest, tasks, nil
}

func (a *zcodeAgent) Phase(task runtimeDriverTask) int {
	if task.Kind == codexTaskOutputSkill || task.Kind == codexTaskEnforcement {
		return 2
	}
	return 1
}

func (a *zcodeAgent) Run(
	ctx context.Context,
	request RuntimeDriverRequest,
	requestDigest string,
	task runtimeDriverTask,
	dependencies map[string]runtimeDriverAttempt,
) (runtimeDriverAttempt, error) {
	return runZcodeDriverTask(ctx, request, requestDigest, a.fixture, a.baseline, task, dependencies)
}

func (a *zcodeAgent) Cases(attempts []runtimeDriverAttempt) []EvalCaseResult {
	return buildZcodeDriverCases(attempts)
}

func (a *zcodeAgent) Metrics(attempts []runtimeDriverAttempt) []EvalMetric {
	return buildCodexDriverMetrics(attempts)
}
