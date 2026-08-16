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

func TestRunCodexRuntimeUsesFixedReadOnlyInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	root := t.TempDir()
	schema := filepath.Join(root, "output.schema.json")
	if err := os.WriteFile(schema, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "codex-fixture")
	script := `#!/bin/sh
set -eu
test "$1" = exec
test "$2" = --json
test "$3" = --sandbox
test "$4" = read-only
test "$5" = -c
test "$6" = 'approval_policy="never"'
test "$7" = -C
test "$(cd "$8" && pwd -P)" = "$(pwd -P)"
test "$9" = --model
test "${10}" = gpt-5.5
test "${11}" = --dangerously-bypass-hook-trust
test "${12}" = --output-schema
test "${13}" = "` + schema + `"
test "${14}" = probe
printf '%s\n' \
  '{"type":"thread.started"}' \
  '{"type":"turn.started"}' \
  '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}' \
  '{"type":"turn.completed"}'
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := RunCodexRuntime(context.Background(), CodexRuntimeOptions{
		Executable: executable, RepositoryRoot: root, Prompt: "probe",
		ModelLabel:   "gpt-5.5",
		OutputSchema: schema, Timeout: 10 * time.Second, Environment: []string{"PATH=/usr/bin:/bin"},
		BypassHookTrust: true,
	})
	if err != nil {
		t.Fatalf("run codex fixture: %v", err)
	}
	if !result.Observation.TurnCompleted || len(result.Transcript) == 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestRunCodexRuntimeEnforcesExplicitTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	root := t.TempDir()
	executable := filepath.Join(t.TempDir(), "codex-blocked-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := RunCodexRuntime(context.Background(), CodexRuntimeOptions{
		Executable: executable, RepositoryRoot: root, Prompt: "probe",
		Timeout: 100 * time.Millisecond, Environment: []string{"PATH=/usr/bin:/bin"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestDecodeCodexFinalJSONUsesLastStrictMessage(t *testing.T) {
	type result struct {
		Skills []string `json:"skills"`
	}
	observation := CodexRuntimeObservation{Messages: []string{
		"commentary", `{"skills":["gds-orient"]}`,
	}}
	var decoded result
	if err := DecodeCodexFinalJSON(observation, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Skills) != 1 || decoded.Skills[0] != "gds-orient" {
		t.Fatalf("decoded=%#v", decoded)
	}
	if err := DecodeCodexFinalJSON(
		CodexRuntimeObservation{Messages: []string{`{"skills":[],"extra":true}`}}, &decoded,
	); err == nil {
		t.Fatal("unknown final JSON field accepted")
	}
}

func TestParseCodexRuntimeJSONLExtractsConfinedSkillRead(t *testing.T) {
	root := t.TempDir()
	skill := root + "/.agents/skills/gds-orient/SKILL.md"
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"rtk read ` + skill + `","status":"completed"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")

	observation, err := ParseCodexRuntimeJSONL(strings.NewReader(transcript), root)
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
	if len(observation.Commands) != 1 || len(observation.Messages) != 1 ||
		!observation.TurnCompleted {
		t.Fatalf("unexpected observation: %#v", observation)
	}
}

func TestParseCodexRuntimeJSONLIgnoresSkillOutsideRoot(t *testing.T) {
	root := t.TempDir()
	transcript := strings.Join([]string{
		`{"type":"thread.started"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"cat /tmp/foreign/SKILL.md","status":"completed"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")

	observation, err := ParseCodexRuntimeJSONL(strings.NewReader(transcript), root)
	if err != nil {
		t.Fatalf("parse transcript: %v", err)
	}
	if len(observation.SkillReads) != 0 {
		t.Fatalf("foreign skill reads accepted: %#v", observation.SkillReads)
	}
}

func TestParseCodexRuntimeJSONLNormalizesRelativeSkillRead(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, ".agents", "skills", "gds-orient", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"thread.started"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"rtk read .agents/skills/gds-orient/SKILL.md","status":"completed"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")

	observation, err := ParseCodexRuntimeJSONL(strings.NewReader(transcript), root)
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
}

func TestParseCodexRuntimeJSONLRejectsIncompleteLifecycle(t *testing.T) {
	_, err := ParseCodexRuntimeJSONL(strings.NewReader(
		"{\"type\":\"thread.started\"}\n{\"type\":\"turn.started\"}\n",
	), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "lifecycle completion") {
		t.Fatalf("error = %v, want lifecycle completion failure", err)
	}
}

func TestParseCodexRuntimeJSONLRejectsOversizedLine(t *testing.T) {
	oversized := `{"type":"item.completed","item":{"type":"agent_message","text":"` +
		strings.Repeat("x", maximumCodexRuntimeLineBytes) + `"}}`
	_, err := ParseCodexRuntimeJSONL(strings.NewReader(oversized), t.TempDir())
	if err == nil {
		t.Fatal("oversized line accepted")
	}
}

func TestParseCodexRuntimeJSONLAcceptsOneBoundedLargeEvent(t *testing.T) {
	message := strings.Repeat("x", 2<<20)
	transcript := strings.Join([]string{
		`{"type":"thread.started"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"` + message + `"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")

	observation, err := ParseCodexRuntimeJSONL(strings.NewReader(transcript), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Messages) != 1 || observation.Messages[0] != message {
		t.Fatal("bounded large event was not preserved")
	}
}
