package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunClaudeRuntimeUsesFixedReadOnlyInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	root := t.TempDir()
	executable := filepath.Join(t.TempDir(), "claude-fixture")
	script := `#!/bin/sh
set -eu
test "$1" = -p
test "$2" = --output-format
test "$3" = stream-json
test "$4" = --verbose
test "$5" = --permission-mode
test "$6" = plan
test "$7" = --disallowed-tools
test "$8" = Write,Edit,MultiEdit,NotebookEdit
test "$9" = --model
test "${10}" = claude-fable-5
test "${11}" = probe
printf '%s\n' \
  '{"type":"system","subtype":"init","session_id":"s"}' \
  '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}' \
  '{"type":"result","subtype":"success","is_error":false,"result":"ok"}'
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := RunClaudeRuntime(context.Background(), ClaudeRuntimeOptions{
		Executable: executable, RepositoryRoot: root, Prompt: "probe",
		ModelLabel: "claude-fable-5", Timeout: 10 * time.Second,
		Environment: []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("run claude fixture: %v", err)
	}
	if !result.Observation.Completed || result.Observation.ResultText != "ok" ||
		len(result.Transcript) == 0 {
		t.Fatalf("unexpected result: %#v", result.Observation)
	}
}

func TestRunClaudeRuntimeEnforcesExplicitTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	root := t.TempDir()
	executable := filepath.Join(t.TempDir(), "claude-blocked-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := RunClaudeRuntime(context.Background(), ClaudeRuntimeOptions{
		Executable: executable, RepositoryRoot: root, Prompt: "probe",
		Timeout: 100 * time.Millisecond, Environment: []string{"PATH=/usr/bin:/bin"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestDecodeClaudeFinalJSONUsesResultText(t *testing.T) {
	type result struct {
		Skills []string `json:"skills"`
	}
	observation := ClaudeRuntimeObservation{
		Messages:   []string{"commentary"},
		ResultText: `{"skills":["gds-orient"]}`,
	}
	var decoded result
	if err := DecodeClaudeFinalJSON(observation, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Skills) != 1 || decoded.Skills[0] != "gds-orient" {
		t.Fatalf("decoded=%#v", decoded)
	}
	if err := DecodeClaudeFinalJSON(
		ClaudeRuntimeObservation{ResultText: `{"skills":[],"extra":true}`}, &decoded,
	); err == nil {
		t.Fatal("unknown final JSON field accepted")
	}
}

func TestParseClaudeStreamJSONExtractsConfinedSkillRead(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, ".claude", "skills", "gds-orient", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"` + skill + `"}}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Skill","input":{"command":"gds-orient"}}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"result","is_error":false,"result":"done"}`,
	}, "\n")

	observation, err := ParseClaudeStreamJSON(strings.NewReader(transcript), root)
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
	if !observation.Completed || len(observation.Messages) != 1 {
		t.Fatalf("unexpected observation: %#v", observation)
	}
}

func TestParseClaudeStreamJSONIgnoresSkillOutsideRoot(t *testing.T) {
	root := t.TempDir()
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"cat /tmp/foreign/SKILL.md"}}]}}`,
		`{"type":"result","is_error":false,"result":"x"}`,
	}, "\n")

	observation, err := ParseClaudeStreamJSON(strings.NewReader(transcript), root)
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

func TestParseClaudeStreamJSONDetectsMutatingToolAndCommand(t *testing.T) {
	root := t.TempDir()
	editTranscript := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"x"}}]}}`,
		`{"type":"result","is_error":false,"result":"x"}`,
	}, "\n")
	editObservation, err := ParseClaudeStreamJSON(strings.NewReader(editTranscript), root)
	if err != nil {
		t.Fatal(err)
	}
	if !claudeCommandsAttemptMutation(editObservation) {
		t.Fatal("native Edit tool not flagged as mutation attempt")
	}
	pushTranscript := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"git push origin main"}}]}}`,
		`{"type":"result","is_error":false,"result":"x"}`,
	}, "\n")
	pushObservation, err := ParseClaudeStreamJSON(strings.NewReader(pushTranscript), root)
	if err != nil {
		t.Fatal(err)
	}
	if !claudeCommandsAttemptMutation(pushObservation) {
		t.Fatal("mutating shell command not flagged")
	}
}

func TestParseClaudeStreamJSONRejectsIncompleteLifecycle(t *testing.T) {
	_, err := ParseClaudeStreamJSON(strings.NewReader(
		"{\"type\":\"system\",\"subtype\":\"init\"}\n",
	), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "lifecycle completion") {
		t.Fatalf("error = %v, want lifecycle completion failure", err)
	}
}

func TestParseClaudeStreamJSONRejectsErrorResult(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"result","is_error":true,"result":"boom"}`,
	}, "\n")
	_, err := ParseClaudeStreamJSON(strings.NewReader(transcript), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "failed turn") {
		t.Fatalf("error = %v, want failed-turn rejection", err)
	}
}

func TestParseClaudeStreamJSONRejectsOversizedLine(t *testing.T) {
	oversized := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` +
		strings.Repeat("x", maximumClaudeRuntimeLineBytes) + `"}}}`
	_, err := ParseClaudeStreamJSON(strings.NewReader(oversized), t.TempDir())
	if err == nil {
		t.Fatal("oversized line accepted")
	}
}
