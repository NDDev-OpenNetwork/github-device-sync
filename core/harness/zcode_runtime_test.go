package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRunZcodeRuntimeUsesFixedReadOnlyInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	root := t.TempDir()
	executable := filepath.Join(t.TempDir(), "zcode-fixture")
	script := `#!/bin/sh
set -eu
test "$1" = --prompt
test "$2" = probe
test "$3" = --mode
test "$4" = plan
test "$5" = --json
test "$6" = --disallowed-tools
test "$7" = Write,Edit
printf '%s\n' \
  '{"type":"session.created","session":{"id":"s"}}' \
  '{"type":"turn.started"}' \
  '{"type":"message.upserted","message":{"role":"assistant","content":"ok"}}' \
  '{"type":"turn.completed"}'
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := RunZcodeRuntime(context.Background(), ZcodeRuntimeOptions{
		Executable: executable, RepositoryRoot: root, Prompt: "probe",
		ModelLabel: "glm-4.6", Timeout: 10 * time.Second,
		Environment: []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("run zcode fixture: %v", err)
	}
	if !result.Observation.Completed || result.Observation.ResultText != "ok" ||
		len(result.Transcript) == 0 {
		t.Fatalf("unexpected result: %#v", result.Observation)
	}
}

func TestRunZcodeRuntimeEnforcesExplicitTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	root := t.TempDir()
	executable := filepath.Join(t.TempDir(), "zcode-blocked-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := RunZcodeRuntime(context.Background(), ZcodeRuntimeOptions{
		Executable: executable, RepositoryRoot: root, Prompt: "probe",
		Timeout: 100 * time.Millisecond, Environment: []string{"PATH=/usr/bin:/bin"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestDecodeZcodeFinalJSONUsesResultText(t *testing.T) {
	type result struct {
		Skills []string `json:"skills"`
	}
	observation := ZcodeRuntimeObservation{
		Messages:   []string{"commentary"},
		ResultText: `{"skills":["gds-orient"]}`,
	}
	var decoded result
	if err := DecodeZcodeFinalJSON(observation, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Skills) != 1 || decoded.Skills[0] != "gds-orient" {
		t.Fatalf("decoded=%#v", decoded)
	}
	if err := DecodeZcodeFinalJSON(
		ZcodeRuntimeObservation{ResultText: `{"skills":[],"extra":true}`}, &decoded,
	); err == nil {
		t.Fatal("unknown final JSON field accepted")
	}
}

func TestParseZcodeStreamJSONExtractsConfinedSkillRead(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, ".zcode", "skills", "gds-orient", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"session.created"}`,
		`{"type":"turn.started"}`,
		`{"type":"message.upserted","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"` + skill + `"}}]}}`,
		`{"type":"message.upserted","message":{"role":"assistant","content":[{"type":"tool_use","name":"Skill","input":{"command":"gds-orient"}}]}}`,
		`{"type":"message.upserted","message":{"role":"assistant","content":"done"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")

	observation, err := ParseZcodeStreamJSON(strings.NewReader(transcript), root)
	if err != nil {
		t.Fatalf("parse transcript: %v", err)
	}
	want, err := filepath.EvalSymlinks(skill)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.SkillReads) != 1 || observation.SkillReads[0] != want {
		t.Fatalf("skill reads = %#v, want %q", observation.SkillReads, want)
	}
	if len(observation.SkillInvocations) != 1 || observation.SkillInvocations[0] != "gds-orient" {
		t.Fatalf("skill invocations = %#v", observation.SkillInvocations)
	}
	if !observation.Completed || observation.ResultText != "done" {
		t.Fatalf("unexpected observation: %#v", observation)
	}
}

func TestParseZcodeStreamJSONIgnoresSkillOutsideRoot(t *testing.T) {
	root := t.TempDir()
	transcript := strings.Join([]string{
		`{"type":"turn.started"}`,
		`{"type":"message.upserted","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"cat /tmp/foreign/SKILL.md"}}]}}`,
		`{"type":"turn.completed"}`,
	}, "\n")

	observation, err := ParseZcodeStreamJSON(strings.NewReader(transcript), root)
	if err != nil {
		t.Fatalf("parse transcript: %v", err)
	}
	if len(observation.SkillReads) != 0 {
		t.Fatalf("foreign skill reads accepted: %#v", observation.SkillReads)
	}
	if len(observation.Commands) != 1 {
		t.Fatalf("bash command not recorded: %#v", observation.Commands)
	}
}

func TestParseZcodeStreamJSONDetectsMutatingToolAndCommand(t *testing.T) {
	root := t.TempDir()
	// Content-block Edit exercises the array-content path.
	editTranscript := strings.Join([]string{
		`{"type":"turn.started"}`,
		`{"type":"message.upserted","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"x"}}]}}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	editObservation, err := ParseZcodeStreamJSON(strings.NewReader(editTranscript), root)
	if err != nil {
		t.Fatal(err)
	}
	if !zcodeCommandsAttemptMutation(editObservation) {
		t.Fatal("native Edit tool not flagged as mutation attempt")
	}
	// Inlined message with a toolCalls array exercises the non-nested envelope path.
	writeTranscript := strings.Join([]string{
		`{"type":"turn.started"}`,
		`{"type":"message.upserted","role":"assistant","content":"","toolCalls":[{"toolName":"Write","input":{"file_path":"y"}}]}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	writeObservation, err := ParseZcodeStreamJSON(strings.NewReader(writeTranscript), root)
	if err != nil {
		t.Fatal(err)
	}
	if !zcodeCommandsAttemptMutation(writeObservation) {
		t.Fatal("inlined toolCalls Write not flagged as mutation attempt")
	}
	pushTranscript := strings.Join([]string{
		`{"type":"turn.started"}`,
		`{"type":"message.upserted","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"git push origin main"}}]}}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	pushObservation, err := ParseZcodeStreamJSON(strings.NewReader(pushTranscript), root)
	if err != nil {
		t.Fatal(err)
	}
	if !zcodeCommandsAttemptMutation(pushObservation) {
		t.Fatal("mutating shell command not flagged")
	}
}

func TestParseZcodeStreamJSONRejectsIncompleteLifecycle(t *testing.T) {
	_, err := ParseZcodeStreamJSON(strings.NewReader(
		"{\"type\":\"turn.started\"}\n",
	), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "lifecycle completion") {
		t.Fatalf("error = %v, want lifecycle completion failure", err)
	}
}

func TestParseZcodeStreamJSONRejectsFailedTurn(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"turn.started"}`,
		`{"type":"turn.failed","error":{"message":"boom"}}`,
	}, "\n")
	_, err := ParseZcodeStreamJSON(strings.NewReader(transcript), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "failed turn") {
		t.Fatalf("error = %v, want failed-turn rejection", err)
	}
}

func TestParseZcodeStreamJSONRejectsOversizedLine(t *testing.T) {
	oversized := `{"type":"message.upserted","message":{"role":"assistant","content":"` +
		strings.Repeat("x", maximumZcodeRuntimeLineBytes) + `"}}`
	_, err := ParseZcodeStreamJSON(strings.NewReader(oversized), t.TempDir())
	if err == nil {
		t.Fatal("oversized line accepted")
	}
}

func TestZcodeAgentProvesSixCaseSetWithoutHookLifecycle(t *testing.T) {
	agent := &zcodeAgent{}
	if agent.Harness() != "zcode" {
		t.Fatalf("harness id = %q", agent.Harness())
	}
	cases := agent.Cases([]runtimeDriverAttempt{})
	observed := make([]string, 0, len(cases))
	for _, item := range cases {
		observed = append(observed, item.ID)
	}
	sort.Strings(observed)
	want := expectedRuntimeEvidenceCaseIDs(false)
	if len(observed) != len(want) {
		t.Fatalf("case set = %#v, want %#v", observed, want)
	}
	for index := range want {
		if observed[index] != want[index] {
			t.Fatalf("case set = %#v, want %#v", observed, want)
		}
	}
	for _, id := range observed {
		if id == "hook-lifecycle" {
			t.Fatal("zcode proved the hook-lifecycle case despite declaring no hook support")
		}
	}
	if !containsCodexString(observed, "public-private-context-firewall") {
		t.Fatalf("zcode omitted the public-private-context-firewall case: %#v", observed)
	}
	// The shared codex builder must still emit the full seven-case set unchanged.
	if len(buildCodexDriverCases([]runtimeDriverAttempt{})) != len(runtimeEvidenceCaseIDs) {
		t.Fatal("shared codex case builder no longer emits the full case set")
	}
}
