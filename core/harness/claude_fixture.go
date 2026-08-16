package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// ClaudeRuntimeFixture is one private, isolated Git repository carrying the
// materialized Claude Code projection (generated CLAUDE.md plus copied
// .claude/skills/*/SKILL.md) for native Claude probes.
type ClaudeRuntimeFixture struct {
	Root               string
	NestedDirectory    string
	SkillRoot          string
	CandidateDigest    string
	IncludedSkills     []string
	ImplicitSkills     []string
	ExplicitOnlySkills []string
}

// ClaudeRuntimeBaseFixture is the bare public-consumer repository used for the
// output baseline and firewall probes.
type ClaudeRuntimeBaseFixture struct {
	Root            string
	NestedDirectory string
}

// ClaudeRuntimeEnvironment isolates the Claude configuration, cache, and XDG
// state while reusing the already-authorized credential through a temporary
// copy outside the durable evidence directory.
type ClaudeRuntimeEnvironment struct {
	Variables []string
	Home      string
	cleanup   func() error
}

func (environment ClaudeRuntimeEnvironment) Cleanup() error {
	if environment.cleanup == nil {
		return nil
	}
	return environment.cleanup()
}

// PrepareClaudeRuntimeEnvironment isolates the user Claude configuration,
// plugins, skills, hooks, sessions, and caches while reusing the already
// authorized Claude credential through a private temporary copy.
func PrepareClaudeRuntimeEnvironment(sourceRoot string) (ClaudeRuntimeEnvironment, error) {
	realConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if realConfigDir == "" {
		realConfigDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	return prepareClaudeRuntimeEnvironment(sourceRoot, filepath.Join(realConfigDir, ".credentials.json"))
}

func prepareClaudeRuntimeEnvironment(sourceRoot, credential string) (ClaudeRuntimeEnvironment, error) {
	source, err := filepath.Abs(sourceRoot)
	if err != nil {
		return ClaudeRuntimeEnvironment{}, fmt.Errorf("resolve GDS estate root: %w", err)
	}
	info, err := os.Lstat(credential)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return ClaudeRuntimeEnvironment{}, fmt.Errorf("Claude credential is not one private regular file")
	}
	credentialRaw, err := os.ReadFile(credential)
	if err != nil {
		return ClaudeRuntimeEnvironment{}, fmt.Errorf("read Claude credential: %w", err)
	}
	home, err := os.MkdirTemp("", "gds-claude-runtime-home-")
	if err != nil {
		return ClaudeRuntimeEnvironment{}, fmt.Errorf("create isolated Claude home: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(home) }
	fail := func(cause error) (ClaudeRuntimeEnvironment, error) {
		_ = cleanup()
		return ClaudeRuntimeEnvironment{}, cause
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return fail(fmt.Errorf("secure isolated Claude home: %w", err))
	}
	configDir := filepath.Join(home, ".claude")
	for _, directory := range []string{
		configDir,
		filepath.Join(home, ".cache"),
		filepath.Join(home, ".config"),
		filepath.Join(home, ".local", "share"),
		filepath.Join(home, ".local", "state"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fail(fmt.Errorf("create isolated Claude directory: %w", err))
		}
	}
	// Copy, not symlink: Claude Code rewrites its credential and settings files
	// in place, and a symlink would leak those writes back into the real config.
	if err := os.WriteFile(filepath.Join(configDir, ".credentials.json"), credentialRaw, 0o600); err != nil {
		return fail(fmt.Errorf("copy Claude credential into isolation: %w", err))
	}
	settings := []byte("{\n  \"includeCoAuthoredBy\": false,\n  \"cleanupPeriodDays\": 0\n}\n")
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), settings, 0o600); err != nil {
		return fail(fmt.Errorf("write isolated Claude settings: %w", err))
	}
	variables := []string{
		"CI=1", "CLAUDE_CONFIG_DIR=" + configDir, "GDS_ESTATE_ROOT=" + source,
		"HOME=" + home, "NO_COLOR=1",
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
	}
	for _, key := range []string{"LANG", "LC_ALL", "PATH", "SHELL", "TMPDIR", "USER"} {
		if value := os.Getenv(key); value != "" {
			variables = append(variables, key+"="+value)
		}
	}
	return ClaudeRuntimeEnvironment{Variables: variables, Home: home, cleanup: cleanup}, nil
}

// PrepareClaudeRuntimeFixture creates a private, isolated Git repository for
// native Claude probes. It copies only the minimal verified control-plane
// anchors and materializes the exact canonical adapter candidate.
func PrepareClaudeRuntimeFixture(
	ctx context.Context,
	sourceRoot string,
	evidenceDirectory string,
	skillProfile string,
	schemas *validation.Set,
) (ClaudeRuntimeFixture, error) {
	if schemas == nil || strings.TrimSpace(skillProfile) == "" {
		return ClaudeRuntimeFixture{}, fmt.Errorf("schema set and skill profile are required")
	}
	source, err := filepath.Abs(sourceRoot)
	if err != nil {
		return ClaudeRuntimeFixture{}, fmt.Errorf("resolve source root: %w", err)
	}
	evidence, err := filepath.Abs(evidenceDirectory)
	if err != nil {
		return ClaudeRuntimeFixture{}, fmt.Errorf("resolve evidence directory: %w", err)
	}
	base, err := prepareClaudeRuntimeBaseFixture(source, evidence, "fixture")
	if err != nil {
		return ClaudeRuntimeFixture{}, err
	}
	fixture, nested := base.Root, base.NestedDirectory

	adapter, findings := NewAdapter(source, "claude-code", schemas)
	if len(findings) != 0 {
		return ClaudeRuntimeFixture{}, runtimeFixtureFindings("load Claude adapter", findings)
	}
	plan, findings := adapter.PlanInstall(fixture, RenderRequest{SkillProfile: skillProfile, Scope: "project"})
	if len(findings) != 0 {
		return ClaudeRuntimeFixture{}, runtimeFixtureFindings("plan Claude adapter fixture", findings)
	}
	materializer, err := NewAdapterMaterializer(fixture, plan.Candidate())
	if err != nil {
		return ClaudeRuntimeFixture{}, fmt.Errorf("prepare Claude adapter fixture: %w", err)
	}
	step := operations.Step{
		StepID: "materialize-runtime-fixture", RepositoryID: "runtime-fixture",
		Action: MaterializeAdapterAction, RequiresApproval: false,
		Compensation: operations.Compensation{Mode: "automatic"},
		Parameters:   AdapterParameters(plan),
	}
	if _, err := materializer.Apply(ctx, step); err != nil {
		return ClaudeRuntimeFixture{}, fmt.Errorf("materialize Claude adapter fixture: %w", err)
	}
	if err := materializer.Verify(ctx, step, json.RawMessage(`{}`)); err != nil {
		return ClaudeRuntimeFixture{}, fmt.Errorf("verify Claude adapter fixture: %w", err)
	}
	if err := initializeRuntimeFixtureGit(ctx, fixture); err != nil {
		return ClaudeRuntimeFixture{}, err
	}
	implicit, explicit, err := runtimeFixtureSkillSets(source, skillProfile, schemas)
	if err != nil {
		return ClaudeRuntimeFixture{}, err
	}
	return ClaudeRuntimeFixture{
		Root: fixture, NestedDirectory: nested, SkillRoot: plan.Candidate().SkillRoot,
		CandidateDigest: plan.CandidateDigest,
		IncludedSkills:  append([]string(nil), plan.Candidate().IncludedSkills...),
		ImplicitSkills:  implicit, ExplicitOnlySkills: explicit,
	}, nil
}

// PrepareClaudeRuntimeBareFixture materializes the bare public-consumer fixture
// used for output baseline and firewall probes.
func PrepareClaudeRuntimeBareFixture(
	ctx context.Context,
	sourceRoot string,
	evidenceDirectory string,
) (ClaudeRuntimeBaseFixture, error) {
	source, err := filepath.Abs(sourceRoot)
	if err != nil {
		return ClaudeRuntimeBaseFixture{}, fmt.Errorf("resolve source root: %w", err)
	}
	evidence, err := filepath.Abs(evidenceDirectory)
	if err != nil {
		return ClaudeRuntimeBaseFixture{}, fmt.Errorf("resolve evidence directory: %w", err)
	}
	fixture, err := prepareClaudeRuntimeBaseFixture(source, evidence, "baseline-fixture")
	if err != nil {
		return ClaudeRuntimeBaseFixture{}, err
	}
	if err := initializeRuntimeFixtureGit(ctx, fixture.Root); err != nil {
		return ClaudeRuntimeBaseFixture{}, err
	}
	return fixture, nil
}

// prepareClaudeRuntimeBaseFixture copies the Claude-visible instruction anchor
// (.claude/CLAUDE.md) and .gds facts into a fresh fixture, mirroring the codex
// baseline but for the file Claude Code actually loads.
func prepareClaudeRuntimeBaseFixture(
	sourceRoot, evidenceDirectory, name string,
) (ClaudeRuntimeBaseFixture, error) {
	fixture := filepath.Join(evidenceDirectory, name)
	if err := os.Mkdir(fixture, 0o700); err != nil {
		return ClaudeRuntimeBaseFixture{}, fmt.Errorf("create empty runtime fixture: %w", err)
	}
	for _, path := range []string{
		filepath.Join(".claude", "CLAUDE.md"),
		filepath.Join(".gds", "repository.yaml"),
		filepath.Join(".gds", "bundle.lock.yaml"),
		filepath.Join(".gds", "compiled-policy.json"),
	} {
		if err := copyRuntimeFixtureFile(sourceRoot, fixture, path); err != nil {
			return ClaudeRuntimeBaseFixture{}, err
		}
	}
	nested := filepath.Join(fixture, "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".claude"), 0o700); err != nil {
		return ClaudeRuntimeBaseFixture{}, fmt.Errorf("create nested runtime fixture: %w", err)
	}
	nestedInstructions := []byte("# GDS runtime nested fixture\n\n- Scope marker: `gds-runtime-nested`.\n- Preserve all broader repository safety rules.\n")
	if err := os.WriteFile(filepath.Join(nested, ".claude", "CLAUDE.md"), nestedInstructions, 0o600); err != nil {
		return ClaudeRuntimeBaseFixture{}, fmt.Errorf("write nested runtime instructions: %w", err)
	}
	return ClaudeRuntimeBaseFixture{Root: fixture, NestedDirectory: nested}, nil
}
