package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitauthority"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/skills"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maximumRuntimeFixtureSourceBytes = 1 << 20

type CodexRuntimeFixture struct {
	Root               string
	NestedDirectory    string
	CandidateDigest    string
	IncludedSkills     []string
	ImplicitSkills     []string
	ExplicitOnlySkills []string
}

type CodexRuntimeEnvironment struct {
	Variables []string
	Home      string
	cleanup   func() error
}

type CodexRuntimeBaseFixture struct {
	Root            string
	NestedDirectory string
}

func (environment CodexRuntimeEnvironment) Cleanup() error {
	if environment.cleanup == nil {
		return nil
	}
	return environment.cleanup()
}

// PrepareCodexRuntimeEnvironment isolates user plugins, skills, hooks,
// sessions, and caches while reusing the already-authorized Codex credential
// through a temporary symlink outside the durable evidence directory.
func PrepareCodexRuntimeEnvironment(sourceRoot string) (CodexRuntimeEnvironment, error) {
	realCodexHome := os.Getenv("CODEX_HOME")
	if realCodexHome == "" {
		realCodexHome = filepath.Join(os.Getenv("HOME"), ".codex")
	}
	return prepareCodexRuntimeEnvironment(sourceRoot, filepath.Join(realCodexHome, "auth.json"))
}

func prepareCodexRuntimeEnvironment(sourceRoot, auth string) (CodexRuntimeEnvironment, error) {
	source, err := filepath.Abs(sourceRoot)
	if err != nil {
		return CodexRuntimeEnvironment{}, fmt.Errorf("resolve GDS estate root: %w", err)
	}
	info, err := os.Lstat(auth)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return CodexRuntimeEnvironment{}, fmt.Errorf("Codex auth is not one private regular file")
	}
	home, err := os.MkdirTemp("", "gds-codex-runtime-home-")
	if err != nil {
		return CodexRuntimeEnvironment{}, fmt.Errorf("create isolated Codex home: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(home) }
	fail := func(cause error) (CodexRuntimeEnvironment, error) {
		_ = cleanup()
		return CodexRuntimeEnvironment{}, cause
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return fail(fmt.Errorf("secure isolated Codex home: %w", err))
	}
	codexHome := filepath.Join(home, ".codex")
	for _, directory := range []string{
		codexHome,
		filepath.Join(home, ".cache"),
		filepath.Join(home, ".config"),
		filepath.Join(home, ".local", "share"),
		filepath.Join(home, ".local", "state"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fail(fmt.Errorf("create isolated Codex directory: %w", err))
		}
	}
	if err := os.Symlink(auth, filepath.Join(codexHome, "auth.json")); err != nil {
		return fail(fmt.Errorf("link existing Codex authorization: %w", err))
	}
	config := []byte("check_for_update_on_startup = false\napproval_policy = \"never\"\nsandbox_mode = \"read-only\"\n\n[features]\nhooks = true\n")
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), config, 0o600); err != nil {
		return fail(fmt.Errorf("write isolated Codex config: %w", err))
	}
	variables := []string{
		"CI=1", "CODEX_HOME=" + codexHome, "GDS_ESTATE_ROOT=" + source,
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
	return CodexRuntimeEnvironment{Variables: variables, Home: home, cleanup: cleanup}, nil
}

// PrepareCodexRuntimeFixture creates a private, isolated Git repository for
// native Codex probes. It copies only the minimal verified control-plane
// anchors and materializes the exact canonical adapter candidate.
func PrepareCodexRuntimeFixture(
	ctx context.Context,
	sourceRoot string,
	evidenceDirectory string,
	skillProfile string,
	schemas *validation.Set,
) (CodexRuntimeFixture, error) {
	if schemas == nil || strings.TrimSpace(skillProfile) == "" {
		return CodexRuntimeFixture{}, fmt.Errorf("schema set and skill profile are required")
	}
	source, err := filepath.Abs(sourceRoot)
	if err != nil {
		return CodexRuntimeFixture{}, fmt.Errorf("resolve source root: %w", err)
	}
	evidence, err := filepath.Abs(evidenceDirectory)
	if err != nil {
		return CodexRuntimeFixture{}, fmt.Errorf("resolve evidence directory: %w", err)
	}
	base, err := prepareCodexRuntimeBaseFixture(source, evidence, "fixture")
	if err != nil {
		return CodexRuntimeFixture{}, err
	}
	fixture, nested := base.Root, base.NestedDirectory

	adapter, findings := NewAdapter(source, "codex", schemas)
	if len(findings) != 0 {
		return CodexRuntimeFixture{}, runtimeFixtureFindings("load Codex adapter", findings)
	}
	plan, findings := adapter.PlanInstall(fixture, RenderRequest{SkillProfile: skillProfile, Scope: "project"})
	if len(findings) != 0 {
		return CodexRuntimeFixture{}, runtimeFixtureFindings("plan Codex adapter fixture", findings)
	}
	materializer, err := NewAdapterMaterializer(fixture, plan.Candidate())
	if err != nil {
		return CodexRuntimeFixture{}, fmt.Errorf("prepare Codex adapter fixture: %w", err)
	}
	step := operations.Step{
		StepID: "materialize-runtime-fixture", RepositoryID: "runtime-fixture",
		Action: MaterializeAdapterAction, RequiresApproval: false,
		Compensation: operations.Compensation{Mode: "automatic"},
		Parameters:   AdapterParameters(plan),
	}
	if _, err := materializer.Apply(ctx, step); err != nil {
		return CodexRuntimeFixture{}, fmt.Errorf("materialize Codex adapter fixture: %w", err)
	}
	if err := materializer.Verify(ctx, step, json.RawMessage(`{}`)); err != nil {
		return CodexRuntimeFixture{}, fmt.Errorf("verify Codex adapter fixture: %w", err)
	}
	if err := initializeRuntimeFixtureGit(ctx, fixture); err != nil {
		return CodexRuntimeFixture{}, err
	}
	implicit, explicit, err := runtimeFixtureSkillSets(source, skillProfile, schemas)
	if err != nil {
		return CodexRuntimeFixture{}, err
	}
	return CodexRuntimeFixture{
		Root: fixture, NestedDirectory: nested, CandidateDigest: plan.CandidateDigest,
		IncludedSkills: append([]string(nil), plan.Candidate().IncludedSkills...),
		ImplicitSkills: implicit, ExplicitOnlySkills: explicit,
	}, nil
}

func PrepareCodexRuntimeBareFixture(
	ctx context.Context,
	sourceRoot string,
	evidenceDirectory string,
) (CodexRuntimeBaseFixture, error) {
	source, err := filepath.Abs(sourceRoot)
	if err != nil {
		return CodexRuntimeBaseFixture{}, fmt.Errorf("resolve source root: %w", err)
	}
	evidence, err := filepath.Abs(evidenceDirectory)
	if err != nil {
		return CodexRuntimeBaseFixture{}, fmt.Errorf("resolve evidence directory: %w", err)
	}
	fixture, err := prepareCodexRuntimeBaseFixture(source, evidence, "baseline-fixture")
	if err != nil {
		return CodexRuntimeBaseFixture{}, err
	}
	if err := initializeRuntimeFixtureGit(ctx, fixture.Root); err != nil {
		return CodexRuntimeBaseFixture{}, err
	}
	return fixture, nil
}

func prepareCodexRuntimeBaseFixture(
	sourceRoot, evidenceDirectory, name string,
) (CodexRuntimeBaseFixture, error) {
	fixture := filepath.Join(evidenceDirectory, name)
	if err := os.Mkdir(fixture, 0o700); err != nil {
		return CodexRuntimeBaseFixture{}, fmt.Errorf("create empty runtime fixture: %w", err)
	}
	for _, path := range []string{
		"AGENTS.md",
		filepath.Join(".gds", "repository.yaml"),
		filepath.Join(".gds", "bundle.lock.yaml"),
		filepath.Join(".gds", "compiled-policy.json"),
	} {
		if err := copyRuntimeFixtureFile(sourceRoot, fixture, path); err != nil {
			return CodexRuntimeBaseFixture{}, err
		}
	}
	nested := filepath.Join(fixture, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		return CodexRuntimeBaseFixture{}, fmt.Errorf("create nested runtime fixture: %w", err)
	}
	nestedInstructions := []byte("# GDS runtime nested fixture\n\n- Scope marker: `gds-runtime-nested`.\n- Preserve all broader repository safety rules.\n")
	if err := os.WriteFile(filepath.Join(nested, "AGENTS.md"), nestedInstructions, 0o600); err != nil {
		return CodexRuntimeBaseFixture{}, fmt.Errorf("write nested runtime instructions: %w", err)
	}
	return CodexRuntimeBaseFixture{Root: fixture, NestedDirectory: nested}, nil
}

func runtimeFixtureSkillSets(
	sourceRoot, profileID string,
	schemas *validation.Set,
) ([]string, []string, error) {
	outcome := skills.Validate(sourceRoot, schemas)
	if len(outcome.Findings) != 0 {
		return nil, nil, runtimeFixtureFindings("load runtime skill registry", outcome.Findings)
	}
	selected := map[string]struct{}{}
	for _, profile := range outcome.Registry.Profiles {
		if profile.ID == profileID {
			for _, name := range profile.Skills {
				selected[name] = struct{}{}
			}
		}
	}
	implicit := []string{}
	explicit := []string{}
	for _, definition := range outcome.Registry.Skills {
		if _, found := selected[definition.Name]; !found {
			continue
		}
		if definition.Invocation == "explicit-only" {
			explicit = append(explicit, definition.Name)
		} else {
			implicit = append(implicit, definition.Name)
		}
	}
	if len(implicit)+len(explicit) != len(selected) {
		return nil, nil, fmt.Errorf("runtime skill profile does not resolve every selected definition")
	}
	sort.Strings(implicit)
	sort.Strings(explicit)
	return implicit, explicit, nil
}

func copyRuntimeFixtureFile(sourceRoot, targetRoot, relative string) error {
	source := filepath.Join(sourceRoot, relative)
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > maximumRuntimeFixtureSourceBytes {
		return fmt.Errorf("runtime fixture source is not one bounded regular file: %s", relative)
	}
	content, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read runtime fixture source %s: %w", relative, err)
	}
	target := filepath.Join(targetRoot, relative)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create runtime fixture directory for %s: %w", relative, err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		return fmt.Errorf("write runtime fixture source %s: %w", relative, err)
	}
	return nil
}

func initializeRuntimeFixtureGit(ctx context.Context, root string) error {
	authority, err := gitauthority.Discover()
	if err != nil {
		return fmt.Errorf("initialize runtime fixture Git authority: %w", err)
	}
	commands := [][]string{
		{"-c", "init.defaultBranch=main", "init", "-q"},
		{"add", "--all"},
		{"-c", "user.name=GDS Runtime Eval", "-c", "user.email=gds-runtime@example.invalid",
			"-c", "commit.gpgsign=false", "commit", "-qm", "runtime fixture"},
	}
	for _, arguments := range commands {
		var output bytes.Buffer
		_, err := authority.Run(ctx, gitauthority.RunRequest{
			Directory: root, Arguments: arguments, Stdout: &output, Stderr: &output,
		})
		if err != nil {
			return fmt.Errorf("initialize runtime fixture Git state: %w: %s", err, strings.TrimSpace(output.String()))
		}
	}
	return nil
}

func runtimeFixtureFindings(action string, findings []domain.Finding) error {
	if len(findings) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s: %s", action, findings[0].Code, findings[0].Message)
}
