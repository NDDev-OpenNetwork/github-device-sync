package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const claudeJudgePolicy = "GDS runtime judge v1 (claude-code): evaluate only supplied subject output, tool events, deterministic state evidence, and exact rubrics; never execute tools or infer missing evidence."

// ClaudeEvidenceDriverOptions bound the Claude native runtime evidence driver.
type ClaudeEvidenceDriverOptions struct {
	Concurrency int
	Now         func() time.Time
}

// RunClaudeEvidenceDriver produces native Claude Code runtime evidence for one
// harness by driving the claudeAgent through the shared two-phase,
// checkpoint-resumable evaluation engine.
func RunClaudeEvidenceDriver(
	ctx context.Context,
	request RuntimeDriverRequest,
	schemas *validation.Set,
	options ClaudeEvidenceDriverOptions,
) (RuntimeEvidence, error) {
	return RunEvidenceDriver(ctx, request, schemas, &claudeAgent{}, EvidenceDriverOptions{
		Concurrency: options.Concurrency,
		Now:         options.Now,
	})
}

// buildClaudeDriverTasks reuses the harness-agnostic task planner. Task shapes
// depend only on fixture directories and skill names, never on the harness skill
// projection path, so Claude and Codex prove the identical task set.
func buildClaudeDriverTasks(
	request RuntimeDriverRequest,
	fixture ClaudeRuntimeFixture,
	baseline ClaudeRuntimeBaseFixture,
) ([]runtimeDriverTask, error) {
	return buildCodexDriverTasks(request, CodexRuntimeFixture{
		Root: fixture.Root, NestedDirectory: fixture.NestedDirectory,
		CandidateDigest:    fixture.CandidateDigest,
		IncludedSkills:     append([]string(nil), fixture.IncludedSkills...),
		ImplicitSkills:     append([]string(nil), fixture.ImplicitSkills...),
		ExplicitOnlySkills: append([]string(nil), fixture.ExplicitOnlySkills...),
	}, CodexRuntimeBaseFixture{Root: baseline.Root, NestedDirectory: baseline.NestedDirectory})
}

func validateClaudeDriverRequest(request RuntimeDriverRequest) error {
	if request.SchemaVersion != 1 || request.Harness != "claude-code" ||
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
		request.ProfilePath:       filepath.Join(root, "harnesses", "claude-code", "profile.yaml"),
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

// claudeSkillRealPaths maps the confined real SKILL.md path of every included
// skill to its name, using the fixture's materialized skill root.
func claudeSkillRealPaths(fixture ClaudeRuntimeFixture) map[string]string {
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

func claudeObservedSkills(fixture ClaudeRuntimeFixture, observation ClaudeRuntimeObservation) []string {
	included := stringSet(fixture.IncludedSkills)
	observed := map[string]struct{}{}
	for _, skill := range observation.SkillInvocations {
		if _, ok := included[skill]; ok {
			observed[skill] = struct{}{}
		}
	}
	for path, skill := range claudeSkillRealPaths(fixture) {
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

// claudePrimaryObservedSkill returns the first skill Claude actually engaged,
// preferring an explicit native Skill invocation and falling back to a confined
// SKILL.md read.
func claudePrimaryObservedSkill(fixture ClaudeRuntimeFixture, observation ClaudeRuntimeObservation) string {
	included := stringSet(fixture.IncludedSkills)
	for _, skill := range observation.SkillInvocations {
		if _, ok := included[skill]; ok {
			return skill
		}
	}
	paths := claudeSkillRealPaths(fixture)
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

type claudeDriverInputIdentity struct {
	RequestDigest           string                    `json:"request_digest"`
	DriverDigest            string                    `json:"driver_digest"`
	GDSExecutableDigest     string                    `json:"gds_executable_digest"`
	HarnessExecutableDigest string                    `json:"harness_executable_digest"`
	AdapterCandidateDigest  string                    `json:"adapter_candidate_digest"`
	Files                   []codexDriverIdentityFile `json:"files"`
}

func claudeDriverInputDigest(
	request RuntimeDriverRequest,
	requestRaw []byte,
	driverRaw []byte,
	fixture ClaudeRuntimeFixture,
) (string, error) {
	identity := claudeDriverInputIdentity{
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
		".claude/CLAUDE.md":                filepath.Join(request.RepositoryRoot, ".claude", "CLAUDE.md"),
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
			return "", fmt.Errorf("read Claude driver input %s: %w", key, err)
		}
		identity.Files = append(identity.Files, codexDriverIdentityFile{Path: key, Digest: bytesDigest(raw)})
	}
	pluginFiles, err := claudeDriverTreeIdentity(
		filepath.Join(request.RepositoryRoot, "plugins", "gds-core"), "plugins/gds-core",
	)
	if err != nil {
		return "", err
	}
	identity.Files = append(identity.Files, pluginFiles...)

	raw, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode Claude driver input identity: %w", err)
	}
	return bytesDigest(raw), nil
}

func claudeDriverTreeIdentity(root, label string) ([]codexDriverIdentityFile, error) {
	files := []codexDriverIdentityFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Claude driver input tree contains a symlink: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("Claude driver input tree contains a non-regular file: %s", path)
		}
		if len(files) >= maximumCodexDriverIdentityFiles {
			return fmt.Errorf("Claude driver input tree exceeds %d files", maximumCodexDriverIdentityFiles)
		}
		raw, err := readBoundedRegular(path, maximumRuntimeEvidenceBytes)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, codexDriverIdentityFile{
			Path: filepath.ToSlash(filepath.Join(label, relative)), Digest: bytesDigest(raw),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("digest Claude driver input tree %s: %w", label, err)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}
