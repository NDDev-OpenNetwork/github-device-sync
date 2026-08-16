package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestPrepareCodexRuntimeFixtureIsCleanAndExact(t *testing.T) {
	root := repositoryRoot(t)
	evidence := t.TempDir()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := PrepareCodexRuntimeFixture(context.Background(), root, evidence, "core", schemas)
	if err != nil {
		t.Fatalf("prepare fixture: %v", err)
	}
	if fixture.Root != filepath.Join(evidence, "fixture") || fixture.CandidateDigest == "" ||
		len(fixture.IncludedSkills) != 5 || len(fixture.ImplicitSkills) != 3 ||
		len(fixture.ExplicitOnlySkills) != 2 {
		t.Fatalf("unexpected fixture: %#v", fixture)
	}
	for _, path := range []string{
		"AGENTS.md", ".gds/repository.yaml", ".gds/bundle.lock.yaml",
		".gds/compiled-policy.json", "nested/AGENTS.md",
		".agents/skills/gds-orient/SKILL.md",
	} {
		info, err := os.Lstat(filepath.Join(fixture.Root, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("fixture path %s info=%v err=%v", path, info, err)
		}
	}
	command := exec.Command("git", "status", "--porcelain=v2")
	command.Dir = fixture.Root
	output, err := command.Output()
	if err != nil || len(output) != 0 {
		t.Fatalf("fixture Git state output=%q err=%v", output, err)
	}
}

func TestCodexRuntimeLiveExactSkillDiscovery(t *testing.T) {
	if os.Getenv("GDS_CODEX_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set GDS_CODEX_RUNTIME_INTEGRATION=1 for credentialed native Codex probes")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	evidence := t.TempDir()
	schemaPath := filepath.Join(evidence, "discovery.schema.json")
	schema := []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["skills"],
  "properties": {
    "skills": {
      "type": "array",
      "items": {"type": "string"}
    }
  }
}
`)
	if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := PrepareCodexRuntimeFixture(
		context.Background(), repositoryRoot(t), evidence, "core", schemas,
	)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := PrepareCodexRuntimeEnvironment(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	defer environment.Cleanup()
	want := append([]string(nil), fixture.ImplicitSkills...)
	sort.Strings(want)
	for _, directory := range []string{fixture.Root, fixture.NestedDirectory} {
		result, err := RunCodexRuntime(context.Background(), CodexRuntimeOptions{
			Executable: executable, RepositoryRoot: directory,
			ModelLabel:   codexRuntimeTestModel(),
			Prompt:       "Return the exact repository-scoped GDS skill names available in this session. Do not read files or run tools.",
			OutputSchema: schemaPath, Environment: environment.Variables,
		})
		if err != nil {
			t.Fatal(err)
		}
		var output struct {
			Skills []string `json:"skills"`
		}
		if err := DecodeCodexFinalJSON(result.Observation, &output); err != nil {
			t.Fatal(err)
		}
		sort.Strings(output.Skills)
		observed, _ := json.Marshal(output.Skills)
		expected, _ := json.Marshal(want)
		if string(observed) != string(expected) {
			t.Fatalf("directory=%s skills=%s want=%s", directory, observed, expected)
		}
	}
}

func TestCodexRuntimeLiveExplicitInvocation(t *testing.T) {
	if os.Getenv("GDS_CODEX_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set GDS_CODEX_RUNTIME_INTEGRATION=1 for credentialed native Codex probes")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	evidence := t.TempDir()
	schemaPath := filepath.Join(evidence, "explicit.schema.json")
	schema := []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["contract"],
  "properties": {
    "contract": {"type": "string"}
  }
}
`)
	if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := PrepareCodexRuntimeFixture(
		context.Background(), repositoryRoot(t), evidence, "core", schemas,
	)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := PrepareCodexRuntimeEnvironment(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	defer environment.Cleanup()
	for _, skill := range fixture.IncludedSkills {
		skillPath := filepath.Join(fixture.Root, ".agents", "skills", skill, "SKILL.md")
		contract, err := codexSkillContractOpening(skillPath)
		if err != nil {
			t.Fatal(err)
		}
		result, err := RunCodexRuntime(context.Background(), CodexRuntimeOptions{
			Executable: executable, RepositoryRoot: fixture.Root,
			ModelLabel:   codexRuntimeTestModel(),
			Prompt:       "Use $" + skill + ". This is a native metadata probe: do not run tools or execute the workflow. Return the exact opening statement under the loaded skill's Contract heading.",
			OutputSchema: schemaPath, Environment: environment.Variables,
		})
		if err != nil {
			t.Fatalf("skill=%s: %v", skill, err)
		}
		if len(result.Observation.Commands) != 0 {
			t.Fatalf("skill=%s metadata probe ran tools: %#v", skill, result.Observation.Commands)
		}
		var output struct {
			Contract string `json:"contract"`
		}
		if err := DecodeCodexFinalJSON(result.Observation, &output); err != nil {
			t.Fatalf("skill=%s: %v", skill, err)
		}
		if strings.Join(strings.Fields(output.Contract), " ") != contract {
			t.Fatalf("skill=%s contract=%q want=%q", skill, output.Contract, contract)
		}
	}
}

func TestCodexRuntimeLiveTriggerSample(t *testing.T) {
	if os.Getenv("GDS_CODEX_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set GDS_CODEX_RUNTIME_INTEGRATION=1 for credentialed native Codex probes")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	root := repositoryRoot(t)
	evidence := t.TempDir()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := PrepareCodexRuntimeFixture(context.Background(), root, evidence, "core", schemas)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := PrepareCodexRuntimeEnvironment(root)
	if err != nil {
		t.Fatal(err)
	}
	defer environment.Cleanup()
	var corpus evalTriggerCorpus
	if err := decodeRuntimeEvalSource(
		filepath.Join(root, "skills", "evals", "trigger", "core.json"), &corpus,
	); err != nil {
		t.Fatal(err)
	}
	for _, skill := range corpus.Skills {
		if !containsString(fixture.ImplicitSkills, skill.Name) {
			continue
		}
		for _, sample := range []struct {
			query       evalTriggerQuery
			shouldMatch bool
		}{{skill.Positive[0], true}, {skill.Negative[0], false}} {
			result, err := RunCodexRuntime(context.Background(), CodexRuntimeOptions{
				Executable: executable, RepositoryRoot: fixture.Root,
				ModelLabel: codexRuntimeTestModel(),
				Prompt:     sample.query.Query, Environment: environment.Variables,
			})
			if err != nil {
				t.Fatalf("sample=%s: %v", sample.query.ID, err)
			}
			want, err := filepath.EvalSymlinks(filepath.Join(
				fixture.Root, ".agents", "skills", skill.Name, "SKILL.md",
			))
			if err != nil {
				t.Fatal(err)
			}
			matched := containsString(result.Observation.SkillReads, want)
			if matched != sample.shouldMatch {
				t.Fatalf(
					"sample=%s skill=%s matched=%t want=%t observation=%#v",
					sample.query.ID, skill.Name, matched, sample.shouldMatch, result.Observation,
				)
			}
		}
	}
}

func TestPrepareCodexRuntimeEnvironmentIsIsolated(t *testing.T) {
	auth := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(auth, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment, err := prepareCodexRuntimeEnvironment(repositoryRoot(t), auth)
	if err != nil {
		t.Fatal(err)
	}
	home := environment.Home
	defer func() {
		if err := environment.Cleanup(); err != nil {
			t.Errorf("cleanup environment: %v", err)
		}
	}()
	if home == "" || home == os.Getenv("HOME") {
		t.Fatalf("runtime home is not isolated: %q", home)
	}
	authLink := filepath.Join(home, ".codex", "auth.json")
	info, err := os.Lstat(authLink)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("isolated auth link info=%v err=%v", info, err)
	}
	configInfo, err := os.Lstat(filepath.Join(home, ".codex", "config.toml"))
	if err != nil || !configInfo.Mode().IsRegular() || configInfo.Mode().Perm() != 0o600 {
		t.Fatalf("isolated config info=%v err=%v", configInfo, err)
	}
}

func TestCodexRuntimeLiveOrientProbe(t *testing.T) {
	if os.Getenv("GDS_CODEX_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set GDS_CODEX_RUNTIME_INTEGRATION=1 for a credentialed native Codex probe")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	evidence := t.TempDir()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := PrepareCodexRuntimeFixture(
		context.Background(), repositoryRoot(t), evidence, "core", schemas,
	)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := PrepareCodexRuntimeEnvironment(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	defer environment.Cleanup()
	result, err := RunCodexRuntime(context.Background(), CodexRuntimeOptions{
		Executable: executable, RepositoryRoot: fixture.Root,
		ModelLabel:  codexRuntimeTestModel(),
		Prompt:      "Explain the current verified GDS context, independent Git boundaries, evidence gaps, and safest next workflow.",
		Environment: environment.Variables,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(fixture.Root, ".agents", "skills", "gds-orient", "SKILL.md")
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, path := range result.Observation.SkillReads {
		if path == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("gds-orient was not read: %#v", result.Observation)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func codexRuntimeTestModel() string {
	if model := strings.TrimSpace(os.Getenv("GDS_CODEX_RUNTIME_MODEL")); model != "" {
		return model
	}
	return "gpt-5.5"
}
