package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestCodexDriverInputDigestBindsCanonicalInputs(t *testing.T) {
	root := t.TempDir()
	evidence := t.TempDir()
	request := codexDriverRequestFixture(root, evidence)
	for _, path := range []string{
		request.ProfilePath, request.RuntimeContract, request.TriggerCorpus,
		request.OutputCorpus, request.EnforcementCorpus, request.EvidenceSchema,
		filepath.Join(root, "AGENTS.md"), filepath.Join(root, ".gds", "repository.yaml"),
		filepath.Join(root, ".gds", "bundle.lock.yaml"),
		filepath.Join(root, ".gds", "compiled-policy.json"),
		filepath.Join(root, "skills", "registry.yaml"),
		filepath.Join(root, ".agents", "plugins", "marketplace.json"),
		filepath.Join(root, "plugins", "gds-core", "hooks", "hooks.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(filepath.Base(path)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	requestRaw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	fixture := CodexRuntimeFixture{CandidateDigest: "sha256:candidate"}
	before, err := codexDriverInputDigest(request, requestRaw, []byte("driver"), fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.OutputCorpus, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := codexDriverInputDigest(request, requestRaw, []byte("driver"), fixture)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("canonical input change did not invalidate the checkpoint identity")
	}
}

func TestCodexRuntimeLiveSecurityProbes(t *testing.T) {
	if os.Getenv("GDS_CODEX_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set GDS_CODEX_RUNTIME_INTEGRATION=1 for credentialed native Codex probes")
	}
	root := repositoryRoot(t)
	evidence := t.TempDir()
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	request := codexDriverRequestFixture(root, evidence)
	request.Environment.Executable = executable
	request.ModelLabel = codexRuntimeTestModel()
	gdsExecutable, err := exec.LookPath("gds")
	if err != nil {
		t.Fatal(err)
	}
	request.GDSExecutable, err = filepath.Abs(gdsExecutable)
	if err != nil {
		t.Fatal(err)
	}
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

	firewall, firewallEvidence, _, err := runCodexPublicFirewallProbe(
		context.Background(), request, environment,
	)
	if err != nil || !firewall {
		t.Fatalf("firewall=%t evidence=%+v err=%v", firewall, firewallEvidence, err)
	}
	hooks, hookEvidence, _, err := runCodexHookLifecycleProbe(
		context.Background(), request, environment, fixture,
	)
	if err != nil || !hooks {
		t.Fatalf("hooks=%t evidence=%+v err=%v", hooks, hookEvidence, err)
	}
}

func TestCodexRuntimeLiveOutputJudge(t *testing.T) {
	if os.Getenv("GDS_CODEX_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set GDS_CODEX_RUNTIME_INTEGRATION=1 for credentialed native Codex probes")
	}
	root := repositoryRoot(t)
	evidence := t.TempDir()
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	request := codexDriverRequestFixture(root, evidence)
	request.Environment.Executable = executable
	request.ModelLabel = codexRuntimeTestModel()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := PrepareCodexRuntimeFixture(context.Background(), root, evidence, "core", schemas)
	if err != nil {
		t.Fatal(err)
	}
	baselineFixture, err := PrepareCodexRuntimeBareFixture(context.Background(), root, evidence)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := buildCodexDriverTasks(request, fixture, baselineFixture)
	if err != nil {
		t.Fatal(err)
	}
	var baselineTask, subjectTask runtimeDriverTask
	for _, task := range tasks {
		if task.Skill != "gds-orient" {
			continue
		}
		if task.Kind == codexTaskOutputBaseline {
			baselineTask = task
		}
		if task.Kind == codexTaskOutputSkill {
			subjectTask = task
		}
	}
	requestDigest := bytesDigest([]byte("live-output-judge"))
	baseline, err := runCodexDriverTask(
		context.Background(), request, requestDigest, fixture, baselineFixture,
		baselineTask, map[string]runtimeDriverAttempt{},
	)
	if err != nil || !baseline.Passed {
		t.Fatalf("baseline=%+v err=%v", baseline, err)
	}
	dependencies := map[string]runtimeDriverAttempt{runtimeDriverTaskKey(baselineTask): baseline}
	subject, err := runCodexDriverTask(
		context.Background(), request, requestDigest, fixture, baselineFixture,
		subjectTask, dependencies,
	)
	if err != nil || !subject.Passed {
		t.Fatalf("subject passed=%t details=%+v err=%v", subject.Passed, subject.Details, err)
	}
}

func TestBuildCodexDriverTasksMatchesCanonicalCorePlan(t *testing.T) {
	t.Parallel()
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
	baseline, err := PrepareCodexRuntimeBareFixture(context.Background(), root, evidence)
	if err != nil {
		t.Fatal(err)
	}
	request := codexDriverRequestFixture(root, evidence)
	tasks, err := buildCodexDriverTasks(request, fixture, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 265 {
		t.Fatalf("task count=%d, want 265", len(tasks))
	}
	if _, err := os.Stat(filepath.Join(baseline.Root, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("baseline fixture unexpectedly exposes repository skills: %v", err)
	}
}

func TestCodexDriverCheckpointRoundTripRejectsIdentityDrift(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	task := runtimeDriverTask{
		Kind: codexTaskTrigger, CaseID: "exact-skill-discovery",
		MetricID: "trigger-positive-recall", SampleID: "sample", RunIndex: 1,
		Prompt: "probe", Directory: directory,
	}
	attempt, err := persistRuntimeDriverAttempt(directory, runtimeDriverAttempt{
		RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Kind:          task.Kind, CaseID: task.CaseID, MetricID: task.MetricID,
		SampleID: task.SampleID, RunIndex: task.RunIndex,
		Passed: true, PromptDigest: bytesDigest([]byte(task.Prompt)), Task: &task,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := loadRuntimeDriverAttempt(directory, attempt.RequestDigest, task)
	if err != nil || !found || loaded.Digest != attempt.Digest || loaded.Reference != attempt.Reference {
		t.Fatalf("loaded=%+v found=%t err=%v", loaded, found, err)
	}
	if _, _, err := loadRuntimeDriverAttempt(
		directory,
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		task,
	); err == nil {
		t.Fatal("checkpoint request identity drift was accepted")
	}
}

func TestCodexTriggerRoutingUsesFirstCanonicalSkillLoad(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fixture := CodexRuntimeFixture{Root: root, IncludedSkills: []string{
		"gds-audit-repository", "gds-orient",
	}}
	reads := []string{}
	for _, skill := range fixture.IncludedSkills {
		path := filepath.Join(root, ".agents", "skills", skill, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		reads = append(reads, path)
	}
	if primary := codexPrimaryObservedSkill(fixture, reads); primary != "gds-audit-repository" {
		t.Fatalf("primary=%q", primary)
	}
}

func TestDestructiveCaseIgnoresOrdinarySpecificityMisses(t *testing.T) {
	t.Parallel()
	attempts := []runtimeDriverAttempt{
		{
			MetricID: "trigger-near-miss-specificity", SampleID: "ordinary-miss",
			Passed: false, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Reference: "transcripts/ordinary.json", Details: map[string]any{"explicit_only_intent": false},
		},
		{
			MetricID: "trigger-near-miss-specificity", SampleID: "explicit-only-intent",
			Passed: true, Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Reference: "transcripts/explicit.json", Details: map[string]any{"explicit_only_intent": true},
		},
	}
	for _, result := range buildCodexDriverCases(attempts) {
		if result.ID == "destructive-implicit-negative" && result.Status != "pass" {
			t.Fatalf("destructive case=%+v", result)
		}
	}
}

func TestValidateCodexDriverRequestRequiresCanonicalPaths(t *testing.T) {
	t.Parallel()
	request := codexDriverRequestFixture(repositoryRoot(t), t.TempDir())
	if err := validateCodexDriverRequest(request); err != nil {
		t.Fatal(err)
	}
	request.OutputCorpus = request.TriggerCorpus
	if err := validateCodexDriverRequest(request); err == nil {
		t.Fatal("substituted output corpus was accepted")
	}
}

func codexDriverRequestFixture(root, evidence string) RuntimeDriverRequest {
	root, _ = filepath.Abs(root)
	evidence, _ = filepath.Abs(evidence)
	executable, _ := filepath.Abs(os.Args[0])
	return RuntimeDriverRequest{
		SchemaVersion: 1, Harness: "codex", HarnessVersion: "codex-cli fixture",
		ModelLabel: "gpt-5.5", ExecutionProfile: "read-only", Tools: []string{"shell"},
		Environment: RuntimeEvidenceEnvironment{
			OS: runtime.GOOS, Architecture: runtime.GOARCH,
			Executable: executable, Command: "codex",
		},
		GDSExecutable: executable,
		SkillProfile:  "core", ContractVersion: "1.1.0",
		ProfileDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RepositoryRoot: root, EvidenceDirectory: evidence,
		ProfilePath:       filepath.Join(root, "harnesses", "codex", "profile.yaml"),
		RuntimeContract:   filepath.Join(root, "tests", "harness", "runtime-contract.yaml"),
		TriggerCorpus:     filepath.Join(root, "skills", "evals", "trigger", "core.json"),
		OutputCorpus:      filepath.Join(root, "skills", "evals", "output", "core.json"),
		EnforcementCorpus: filepath.Join(root, "skills", "evals", "enforcement", "common.json"),
		EvidenceSchema:    filepath.Join(root, "schemas", "v1", "harness-runtime-evidence.schema.json"),
	}
}
