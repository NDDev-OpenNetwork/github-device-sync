package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const zcodeJudgePolicy = "GDS runtime judge v1 (zcode): evaluate only supplied subject output, tool events, deterministic state evidence, and exact rubrics; never execute tools or infer missing evidence."

// ZcodeEvidenceDriverOptions bound the zcode native runtime evidence driver.
type ZcodeEvidenceDriverOptions struct {
	Concurrency int
	Now         func() time.Time
}

// RunZcodeEvidenceDriver produces native zcode runtime evidence for one harness
// by driving the zcodeAgent through the shared two-phase, checkpoint-resumable
// evaluation engine.
func RunZcodeEvidenceDriver(
	ctx context.Context,
	request RuntimeDriverRequest,
	schemas *validation.Set,
	options ZcodeEvidenceDriverOptions,
) (RuntimeEvidence, error) {
	return RunEvidenceDriver(ctx, request, schemas, &zcodeAgent{}, EvidenceDriverOptions{
		Concurrency: options.Concurrency,
		Now:         options.Now,
	})
}

// buildZcodeDriverTasks reuses the harness-agnostic task planner. Task shapes
// depend only on fixture directories and skill names, never on the harness skill
// projection path, so zcode and codex prove the identical task set. zcode omits
// no task here: the hook-lifecycle case is proven by a probe embedded in the
// enforcement task, and zcode simply does not run that probe.
func buildZcodeDriverTasks(
	request RuntimeDriverRequest,
	fixture ZcodeRuntimeFixture,
	baseline ZcodeRuntimeBaseFixture,
) ([]runtimeDriverTask, error) {
	return buildCodexDriverTasks(request, CodexRuntimeFixture{
		Root: fixture.Root, NestedDirectory: fixture.NestedDirectory,
		CandidateDigest:    fixture.CandidateDigest,
		IncludedSkills:     append([]string(nil), fixture.IncludedSkills...),
		ImplicitSkills:     append([]string(nil), fixture.ImplicitSkills...),
		ExplicitOnlySkills: append([]string(nil), fixture.ExplicitOnlySkills...),
	}, CodexRuntimeBaseFixture{Root: baseline.Root, NestedDirectory: baseline.NestedDirectory})
}

func validateZcodeDriverRequest(request RuntimeDriverRequest) error {
	if request.SchemaVersion != 1 || request.Harness != "zcode" ||
		request.HarnessVersion == "" ||
		request.ModelLabel == "" || request.ModelLabel == "not-proven" ||
		request.ExecutionProfile != "read-only" || request.SkillProfile == "" ||
		request.Environment.OS != runtime.GOOS || request.Environment.Architecture != runtime.GOARCH {
		return fmt.Errorf("runtime driver request identity is invalid")
	}
	root, err := filepath.Abs(request.RepositoryRoot)
	if err != nil || filepath.Clean(root) != filepath.Clean(request.RepositoryRoot) {
		return fmt.Errorf("runtime driver repository root must be absolute")
	}
	evidence, err := filepath.Abs(request.EvidenceDirectory)
	if err != nil || filepath.Clean(evidence) != filepath.Clean(request.EvidenceDirectory) {
		return fmt.Errorf("runtime driver evidence directory must be absolute")
	}
	expected := map[string]string{
		request.ProfilePath:       filepath.Join(root, "harnesses", "zcode", "profile.yaml"),
		request.RuntimeContract:   filepath.Join(root, "tests", "harness", "runtime-contract.yaml"),
		request.TriggerCorpus:     filepath.Join(root, "skills", "evals", "trigger", request.SkillProfile+".json"),
		request.OutputCorpus:      filepath.Join(root, "skills", "evals", "output", request.SkillProfile+".json"),
		request.EnforcementCorpus: filepath.Join(root, "skills", "evals", "enforcement", "common.json"),
		request.EvidenceSchema:    filepath.Join(root, "schemas", "v1", "harness-runtime-evidence.schema.json"),
	}
	for observed, want := range expected {
		if filepath.Clean(observed) != filepath.Clean(want) {
			return fmt.Errorf("runtime driver canonical input path mismatch")
		}
		info, err := os.Lstat(want)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime driver canonical input is not one regular file: %s", want)
		}
	}
	executable, err := filepath.Abs(request.Environment.Executable)
	if err != nil || filepath.Clean(executable) != filepath.Clean(request.Environment.Executable) {
		return fmt.Errorf("runtime driver executable must be absolute")
	}
	gdsExecutable, err := filepath.Abs(request.GDSExecutable)
	if err != nil || filepath.Clean(gdsExecutable) != filepath.Clean(request.GDSExecutable) {
		return fmt.Errorf("runtime driver GDS executable must be absolute")
	}
	info, err := os.Lstat(gdsExecutable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return fmt.Errorf("runtime driver GDS executable is not one executable regular file")
	}
	evidenceInfo, err := os.Lstat(evidence)
	if err != nil || !evidenceInfo.IsDir() || evidenceInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime driver evidence root is not one real directory")
	}
	return nil
}

// buildZcodeDriverCases proves the exact case set zcode is responsible for. The
// zcode capability profile declares no hook support (harnesses/zcode/profile.yaml
// hooks.supported=false), so per the increment-1 expectedRuntimeEvidenceCaseIDs
// mechanism zcode legitimately omits the hook-lifecycle case. It reuses the
// shared case builder and drops that single case, keeping the other six —
// including public-private-context-firewall — byte-for-byte identical to the
// codex/claude output for those cases.
func buildZcodeDriverCases(attempts []runtimeDriverAttempt) []EvalCaseResult {
	full := buildCodexDriverCases(attempts)
	cases := make([]EvalCaseResult, 0, len(full))
	for _, item := range full {
		if item.ID == "hook-lifecycle" {
			continue
		}
		cases = append(cases, item)
	}
	return cases
}

// zcodeSkillRealPaths maps the confined real SKILL.md path of every included
// skill to its name, using the fixture's materialized skill root.
func zcodeSkillRealPaths(fixture ZcodeRuntimeFixture) map[string]string {
	paths := map[string]string{}
	for _, skill := range fixture.IncludedSkills {
		path, err := filepath.EvalSymlinks(filepath.Join(
			fixture.Root, filepath.FromSlash(fixture.SkillRoot), skill, "SKILL.md",
		))
		if err == nil {
			paths[path] = skill
		}
	}
	return paths
}

func zcodeObservedSkills(fixture ZcodeRuntimeFixture, observation ZcodeRuntimeObservation) []string {
	included := stringSet(fixture.IncludedSkills)
	observed := map[string]struct{}{}
	for _, skill := range observation.SkillInvocations {
		if _, ok := included[skill]; ok {
			observed[skill] = struct{}{}
		}
	}
	for path, skill := range zcodeSkillRealPaths(fixture) {
		if containsCodexString(observation.SkillReads, path) {
			observed[skill] = struct{}{}
		}
	}
	result := make([]string, 0, len(observed))
	for skill := range observed {
		result = append(result, skill)
	}
	sort.Strings(result)
	return result
}

// zcodePrimaryObservedSkill returns the first skill zcode actually engaged,
// preferring an explicit native Skill invocation and falling back to a confined
// SKILL.md read.
func zcodePrimaryObservedSkill(fixture ZcodeRuntimeFixture, observation ZcodeRuntimeObservation) string {
	included := stringSet(fixture.IncludedSkills)
	for _, skill := range observation.SkillInvocations {
		if _, ok := included[skill]; ok {
			return skill
		}
	}
	paths := zcodeSkillRealPaths(fixture)
	for _, read := range observation.SkillReads {
		resolved, err := filepath.EvalSymlinks(read)
		if err != nil {
			continue
		}
		if skill, found := paths[resolved]; found {
			return skill
		}
	}
	return ""
}

type zcodeDriverInputIdentity struct {
	RequestDigest           string                    `json:"request_digest"`
	DriverDigest            string                    `json:"driver_digest"`
	GDSExecutableDigest     string                    `json:"gds_executable_digest"`
	HarnessExecutableDigest string                    `json:"harness_executable_digest"`
	AdapterCandidateDigest  string                    `json:"adapter_candidate_digest"`
	Files                   []codexDriverIdentityFile `json:"files"`
}

func zcodeDriverInputDigest(
	request RuntimeDriverRequest,
	requestRaw []byte,
	driverRaw []byte,
	fixture ZcodeRuntimeFixture,
) (string, error) {
	identity := zcodeDriverInputIdentity{
		RequestDigest: bytesDigest(requestRaw), DriverDigest: bytesDigest(driverRaw),
		AdapterCandidateDigest: fixture.CandidateDigest,
		Files:                  []codexDriverIdentityFile{},
	}
	for label, path := range map[string]string{
		"gds-executable":     request.GDSExecutable,
		"harness-executable": request.Environment.Executable,
	} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("resolve %s identity: %w", label, err)
		}
		raw, err := readBoundedRegular(resolved, 128<<20)
		if err != nil {
			return "", fmt.Errorf("read %s identity: %w", label, err)
		}
		if label == "gds-executable" {
			identity.GDSExecutableDigest = bytesDigest(raw)
		} else {
			identity.HarnessExecutableDigest = bytesDigest(raw)
		}
	}

	files := map[string]string{
		"profile":                          request.ProfilePath,
		"runtime-contract":                 request.RuntimeContract,
		"trigger-corpus":                   request.TriggerCorpus,
		"output-corpus":                    request.OutputCorpus,
		"enforcement-corpus":               request.EnforcementCorpus,
		"evidence-schema":                  request.EvidenceSchema,
		"AGENTS.md":                        filepath.Join(request.RepositoryRoot, "AGENTS.md"),
		".gds/repository.yaml":             filepath.Join(request.RepositoryRoot, ".gds", "repository.yaml"),
		".gds/bundle.lock.yaml":            filepath.Join(request.RepositoryRoot, ".gds", "bundle.lock.yaml"),
		".gds/compiled-policy.json":        filepath.Join(request.RepositoryRoot, ".gds", "compiled-policy.json"),
		"skills/registry.yaml":             filepath.Join(request.RepositoryRoot, "skills", "registry.yaml"),
		".agents/plugins/marketplace.json": filepath.Join(request.RepositoryRoot, ".agents", "plugins", "marketplace.json"),
	}
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw, err := readBoundedRegular(files[key], maximumRuntimeEvidenceBytes)
		if err != nil {
			return "", fmt.Errorf("read zcode driver input %s: %w", key, err)
		}
		identity.Files = append(identity.Files, codexDriverIdentityFile{Path: key, Digest: bytesDigest(raw)})
	}
	pluginFiles, err := codexDriverTreeIdentity(
		filepath.Join(request.RepositoryRoot, "plugins", "gds-core"), "plugins/gds-core",
	)
	if err != nil {
		return "", err
	}
	identity.Files = append(identity.Files, pluginFiles...)

	raw, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode zcode driver input identity: %w", err)
	}
	return bytesDigest(raw), nil
}
