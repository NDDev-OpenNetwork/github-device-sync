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

// ZcodeRuntimeFixture is one private, isolated Git repository carrying the
// materialized zcode projection (workspace-root AGENTS.md plus the local-symlink
// .zcode/skills/*/SKILL.md tree) for native zcode probes.
type ZcodeRuntimeFixture struct {
	Root               string
	NestedDirectory    string
	SkillRoot          string
	CandidateDigest    string
	IncludedSkills     []string
	ImplicitSkills     []string
	ExplicitOnlySkills []string
}

// ZcodeRuntimeBaseFixture is the bare public-consumer repository used for the
// output baseline and firewall probes.
type ZcodeRuntimeBaseFixture struct {
	Root            string
	NestedDirectory string
}

// ZcodeRuntimeEnvironment isolates the zcode configuration, cache, and XDG state
// under a private temporary ZCODE_HOME. zcode is unauthenticated in this
// environment, so unlike the Claude environment there is no credential to copy;
// isolation only prevents the probe from reading or writing the real ~/.zcode.
type ZcodeRuntimeEnvironment struct {
	Variables []string
	Home      string
	cleanup   func() error
}

func (environment ZcodeRuntimeEnvironment) Cleanup() error {
	if environment.cleanup == nil {
		return nil
	}
	return environment.cleanup()
}

// PrepareZcodeRuntimeEnvironment isolates the user zcode configuration, skills,
// and caches under a private temporary ZCODE_HOME. No credential is required or
// copied because zcode is unauthenticated in this environment.
func PrepareZcodeRuntimeEnvironment(sourceRoot string) (ZcodeRuntimeEnvironment, error) {
	source, err := filepath.Abs(sourceRoot)
	if err != nil {
		return ZcodeRuntimeEnvironment{}, fmt.Errorf("resolve GDS estate root: %w", err)
	}
	home, err := os.MkdirTemp("", "gds-zcode-runtime-home-")
	if err != nil {
		return ZcodeRuntimeEnvironment{}, fmt.Errorf("create isolated zcode home: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(home) }
	fail := func(cause error) (ZcodeRuntimeEnvironment, error) {
		_ = cleanup()
		return ZcodeRuntimeEnvironment{}, cause
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return fail(fmt.Errorf("secure isolated zcode home: %w", err))
	}
	configDir := filepath.Join(home, ".zcode")
	for _, directory := range []string{
		configDir,
		filepath.Join(home, ".cache"),
		filepath.Join(home, ".config"),
		filepath.Join(home, ".local", "share"),
		filepath.Join(home, ".local", "state"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fail(fmt.Errorf("create isolated zcode directory: %w", err))
		}
	}
	variables := []string{
		"CI=1", "GDS_ESTATE_ROOT=" + source,
		"HOME=" + home, "NO_COLOR=1", "ZCODE_HOME=" + configDir,
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
	return ZcodeRuntimeEnvironment{Variables: variables, Home: home, cleanup: cleanup}, nil
}

// PrepareZcodeRuntimeFixture creates a private, isolated Git repository for
// native zcode probes. It copies only the minimal verified control-plane anchors
// and materializes the exact canonical adapter candidate.
func PrepareZcodeRuntimeFixture(
	ctx context.Context,
	sourceRoot string,
	evidenceDirectory string,
	skillProfile string,
	schemas *validation.Set,
) (ZcodeRuntimeFixture, error) {
	if schemas == nil || strings.TrimSpace(skillProfile) == "" {
		return ZcodeRuntimeFixture{}, fmt.Errorf("schema set and skill profile are required")
	}
	source, err := filepath.Abs(sourceRoot)
	if err != nil {
		return ZcodeRuntimeFixture{}, fmt.Errorf("resolve source root: %w", err)
	}
	evidence, err := filepath.Abs(evidenceDirectory)
	if err != nil {
		return ZcodeRuntimeFixture{}, fmt.Errorf("resolve evidence directory: %w", err)
	}
	base, err := prepareZcodeRuntimeBaseFixture(source, evidence, "fixture")
	if err != nil {
		return ZcodeRuntimeFixture{}, err
	}
	fixture, nested := base.Root, base.NestedDirectory

	adapter, findings := NewAdapter(source, "zcode", schemas)
	if len(findings) != 0 {
		return ZcodeRuntimeFixture{}, runtimeFixtureFindings("load zcode adapter", findings)
	}
	plan, findings := adapter.PlanInstall(fixture, RenderRequest{SkillProfile: skillProfile, Scope: "project"})
	if len(findings) != 0 {
		return ZcodeRuntimeFixture{}, runtimeFixtureFindings("plan zcode adapter fixture", findings)
	}
	materializer, err := NewAdapterMaterializer(fixture, plan.Candidate())
	if err != nil {
		return ZcodeRuntimeFixture{}, fmt.Errorf("prepare zcode adapter fixture: %w", err)
	}
	step := operations.Step{
		StepID: "materialize-runtime-fixture", RepositoryID: "runtime-fixture",
		Action: MaterializeAdapterAction, RequiresApproval: false,
		Compensation: operations.Compensation{Mode: "automatic"},
		Parameters:   AdapterParameters(plan),
	}
	if _, err := materializer.Apply(ctx, step); err != nil {
		return ZcodeRuntimeFixture{}, fmt.Errorf("materialize zcode adapter fixture: %w", err)
	}
	if err := materializer.Verify(ctx, step, json.RawMessage(`{}`)); err != nil {
		return ZcodeRuntimeFixture{}, fmt.Errorf("verify zcode adapter fixture: %w", err)
	}
	if err := initializeRuntimeFixtureGit(ctx, fixture); err != nil {
		return ZcodeRuntimeFixture{}, err
	}
	implicit, explicit, err := runtimeFixtureSkillSets(source, skillProfile, schemas)
	if err != nil {
		return ZcodeRuntimeFixture{}, err
	}
	return ZcodeRuntimeFixture{
		Root: fixture, NestedDirectory: nested, SkillRoot: plan.Candidate().SkillRoot,
		CandidateDigest: plan.CandidateDigest,
		IncludedSkills:  append([]string(nil), plan.Candidate().IncludedSkills...),
		ImplicitSkills:  implicit, ExplicitOnlySkills: explicit,
	}, nil
}

// PrepareZcodeRuntimeBareFixture materializes the bare public-consumer fixture
// used for output baseline and firewall probes.
func PrepareZcodeRuntimeBareFixture(
	ctx context.Context,
	sourceRoot string,
	evidenceDirectory string,
) (ZcodeRuntimeBaseFixture, error) {
	source, err := filepath.Abs(sourceRoot)
	if err != nil {
		return ZcodeRuntimeBaseFixture{}, fmt.Errorf("resolve source root: %w", err)
	}
	evidence, err := filepath.Abs(evidenceDirectory)
	if err != nil {
		return ZcodeRuntimeBaseFixture{}, fmt.Errorf("resolve evidence directory: %w", err)
	}
	fixture, err := prepareZcodeRuntimeBaseFixture(source, evidence, "baseline-fixture")
	if err != nil {
		return ZcodeRuntimeBaseFixture{}, err
	}
	if err := initializeRuntimeFixtureGit(ctx, fixture.Root); err != nil {
		return ZcodeRuntimeBaseFixture{}, err
	}
	return fixture, nil
}

// prepareZcodeRuntimeBaseFixture copies the zcode-visible instruction anchor
// (workspace-root AGENTS.md) and .gds facts into a fresh fixture, mirroring the
// codex baseline but for the workspace-root-flattened file zcode actually loads.
func prepareZcodeRuntimeBaseFixture(
	sourceRoot, evidenceDirectory, name string,
) (ZcodeRuntimeBaseFixture, error) {
	fixture := filepath.Join(evidenceDirectory, name)
	if err := os.Mkdir(fixture, 0o700); err != nil {
		return ZcodeRuntimeBaseFixture{}, fmt.Errorf("create empty runtime fixture: %w", err)
	}
	for _, path := range []string{
		"AGENTS.md",
		filepath.Join(".gds", "repository.yaml"),
		filepath.Join(".gds", "bundle.lock.yaml"),
		filepath.Join(".gds", "compiled-policy.json"),
	} {
		if err := copyRuntimeFixtureFile(sourceRoot, fixture, path); err != nil {
			return ZcodeRuntimeBaseFixture{}, err
		}
	}
	nested := filepath.Join(fixture, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		return ZcodeRuntimeBaseFixture{}, fmt.Errorf("create nested runtime fixture: %w", err)
	}
	nestedInstructions := []byte("# GDS runtime nested fixture\n\n- Scope marker: `gds-runtime-nested`.\n- Preserve all broader repository safety rules.\n")
	if err := os.WriteFile(filepath.Join(nested, "AGENTS.md"), nestedInstructions, 0o600); err != nil {
		return ZcodeRuntimeBaseFixture{}, fmt.Errorf("write nested runtime instructions: %w", err)
	}
	return ZcodeRuntimeBaseFixture{Root: fixture, NestedDirectory: nested}, nil
}
