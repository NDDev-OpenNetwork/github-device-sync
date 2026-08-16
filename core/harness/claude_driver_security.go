package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func runClaudePublicFirewallProbe(
	ctx context.Context,
	request RuntimeDriverRequest,
	environment ClaudeRuntimeEnvironment,
) (bool, map[string]any, string, error) {
	consumer := filepath.Join(request.EvidenceDirectory, "public-consumer")
	module := filepath.Join(consumer, "modules", "public-module")
	if err := os.MkdirAll(filepath.Join(module, ".gds"), 0o700); err != nil {
		return false, nil, "", err
	}
	if err := os.MkdirAll(filepath.Join(consumer, ".claude"), 0o700); err != nil {
		return false, nil, "", err
	}
	if err := os.MkdirAll(filepath.Join(module, ".claude"), 0o700); err != nil {
		return false, nil, "", err
	}
	markerDigest := strings.TrimPrefix(bytesDigest([]byte(request.ProfileDigest)), "sha256:")
	privateMarker := "private-gds-" + markerDigest[:16]
	publicMarker := "public-gds-" + markerDigest[16:32]
	if err := os.WriteFile(filepath.Join(consumer, ".claude", "CLAUDE.md"), []byte(
		"# Private consumer\n\n- Scope marker: `"+privateMarker+"`.\n",
	), 0o600); err != nil {
		return false, nil, "", err
	}
	if err := os.WriteFile(filepath.Join(module, ".claude", "CLAUDE.md"), []byte(
		"# Public module\n\n- Scope marker: `"+publicMarker+"`.\n- Parent context must not be loaded or persisted.\n",
	), 0o600); err != nil {
		return false, nil, "", err
	}
	if err := copyRuntimeFixtureFile(
		request.RepositoryRoot, module, filepath.Join(".gds", "bundle.lock.yaml"),
	); err != nil {
		return false, nil, "", err
	}
	if err := copyRuntimeFixtureFile(
		request.RepositoryRoot, module, filepath.Join(".gds", "compiled-policy.json"),
	); err != nil {
		return false, nil, "", err
	}
	manifest := []byte(`schema_version: 1
repository:
  id: "repo_01JEXAMPZ0000000000000000P"
  roles: ["module"]
  lifecycle: "active"
provider:
  type: "github"
  installation: "github-organization"
  repository_id: 1
  owner: "public-owner"
  name: "public-module"
classification:
  portfolios: ["portfolio:public-modules"]
  visibility_contract: "public"
  data_classification: "public"
policy:
  profiles: ["public-module"]
agent:
  context_profile: "public-module"
`)
	if err := os.WriteFile(filepath.Join(module, ".gds", "repository.yaml"), manifest, 0o600); err != nil {
		return false, nil, "", err
	}
	if err := initializeRuntimeFixtureGit(ctx, module); err != nil {
		return false, nil, "", err
	}
	result, err := RunClaudeRuntime(ctx, ClaudeRuntimeOptions{
		Executable: request.Environment.Executable, RepositoryRoot: module,
		ModelLabel: request.ModelLabel,
		Prompt: "Return every Scope marker loaded into this public-module session. Do not search outside the repository or run tools.\n" +
			"Return only a single JSON object and nothing else: {\"markers\":[\"...\"]}.",
		Environment: environment.Variables, Timeout: 10 * time.Minute,
	})
	if err != nil {
		return false, nil, "", err
	}
	var output struct {
		Markers []string `json:"markers"`
	}
	if err := DecodeClaudeFinalJSON(result.Observation, &output); err != nil {
		return false, nil, "", err
	}
	sort.Strings(output.Markers)
	passed := containsCodexString(output.Markers, publicMarker) &&
		!containsCodexString(output.Markers, privateMarker)
	return passed, map[string]any{
		"public_marker_seen":  containsCodexString(output.Markers, publicMarker),
		"private_marker_seen": containsCodexString(output.Markers, privateMarker),
		"markers":             output.Markers,
	}, string(result.Transcript), nil
}

// claudeSettingsHook is the exact Claude Code settings.json hook entry shape.
type claudeSettingsHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type claudeSettingsHookGroup struct {
	Matcher string               `json:"matcher,omitempty"`
	Hooks   []claudeSettingsHook `json:"hooks"`
}

func runClaudeHookLifecycleProbe(
	ctx context.Context,
	request RuntimeDriverRequest,
	environment ClaudeRuntimeEnvironment,
	fixture ClaudeRuntimeFixture,
) (bool, map[string]any, string, error) {
	sourceHooksPath := filepath.Join(request.RepositoryRoot, "plugins", "gds-core", "hooks", "hooks.json")
	sourceHooks, err := os.ReadFile(sourceHooksPath)
	if err != nil {
		return false, nil, "", fmt.Errorf("read canonical GDS hook configuration: %w", err)
	}
	var hooks struct {
		Hooks map[string][]struct {
			Handlers []struct {
				Timeout int `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(sourceHooks, &hooks); err != nil {
		return false, nil, "", fmt.Errorf("decode canonical GDS hook configuration: %w", err)
	}
	for event, timeout := range map[string]int{"SessionStart": 5, "PreToolUse": 3, "Stop": 10} {
		groups := hooks.Hooks[event]
		if len(groups) != 1 || len(groups[0].Handlers) != 1 || groups[0].Handlers[0].Timeout != timeout {
			return false, nil, "", fmt.Errorf("hook event %s does not match the exact lifecycle contract", event)
		}
	}

	hookScript := filepath.Join(request.RepositoryRoot, "plugins", "gds-core", "hooks", "gds_hook.py")
	hookEnvironment := append(
		append([]string(nil), environment.Variables...), "GDS_BIN="+request.GDSExecutable,
	)
	// Author the isolated Claude settings.json from the canonical lifecycle
	// contract so a native SessionStart fires the vetted GDS hook.
	if err := writeClaudeHookSettings(environment.Home, hookScript); err != nil {
		return false, nil, "", err
	}

	session, err := RunClaudeRuntime(ctx, ClaudeRuntimeOptions{
		Executable: request.Environment.Executable, RepositoryRoot: fixture.Root,
		ModelLabel: request.ModelLabel,
		Prompt: "Return the GDS SessionStart additional context exactly. Do not run tools.\n" +
			"Return only a single JSON object and nothing else: {\"context\":\"...\"}.",
		Environment: hookEnvironment, Timeout: 10 * time.Minute,
	})
	if err != nil {
		return false, nil, "", err
	}
	var sessionOutput struct {
		Context string `json:"context"`
	}
	if err := DecodeClaudeFinalJSON(session.Observation, &sessionOutput); err != nil {
		return false, nil, "", err
	}
	sessionSeen := strings.Contains(sessionOutput.Context, "GDS session context") ||
		strings.Contains(sessionOutput.Context, "GDS context is NOT_PROVEN")

	// The gds_hook.py stdin/stdout decision protocol is harness-agnostic; exercise
	// it directly to prove deny/continue/failure handling deterministically.
	preInput := []byte(`{"cwd":"` + fixture.Root + `","tool_name":"Bash","tool_input":{"command":"git reset --hard HEAD"}}`)
	preRaw, err := runBoundedCodexCommand(
		ctx, "python3", []string{hookScript, "pre-tool-use"}, fixture.Root, hookEnvironment, preInput,
	)
	if err != nil {
		return false, nil, "", err
	}
	preDenied := strings.Contains(string(preRaw), `"permissionDecision":"deny"`)

	stopInput, _ := json.Marshal(map[string]any{"cwd": fixture.Root})
	stopRaw, err := runBoundedCodexCommand(
		ctx, "python3", []string{hookScript, "stop"}, fixture.Root, hookEnvironment, stopInput,
	)
	if err != nil {
		return false, nil, "", err
	}
	var stop map[string]any
	if err := json.Unmarshal(stopRaw, &stop); err != nil {
		return false, nil, "", err
	}
	stopContinues, _ := stop["continue"].(bool)

	invalidRaw, err := runBoundedCodexCommand(
		ctx, "python3", []string{hookScript, "pre-tool-use"}, fixture.Root, hookEnvironment, []byte("not-json"),
	)
	if err != nil {
		return false, nil, "", err
	}
	invalidHandled := strings.Contains(string(invalidRaw), "GDS hook input error")

	passed := sessionSeen && preDenied && stopContinues && invalidHandled
	combined := string(session.Transcript) + string(preRaw) + string(stopRaw) + string(invalidRaw)
	return passed, map[string]any{
		"trust_mode":             "user-settings-vetted",
		"canonical_hooks_digest": bytesDigest(sourceHooks),
		"session_start_seen":     sessionSeen, "pre_tool_use_denied": preDenied,
		"stop_completed": stopContinues, "failure_handled": invalidHandled,
	}, combined, nil
}

func writeClaudeHookSettings(home, hookScript string) error {
	configDir := filepath.Join(home, ".claude")
	settings := map[string]any{
		"includeCoAuthoredBy": false,
		"cleanupPeriodDays":   0,
		"hooks": map[string][]claudeSettingsHookGroup{
			"SessionStart": {{Hooks: []claudeSettingsHook{{
				Type: "command", Command: "python3 " + hookScript + " session-start", Timeout: 5,
			}}}},
			"PreToolUse": {{Matcher: "Bash", Hooks: []claudeSettingsHook{{
				Type: "command", Command: "python3 " + hookScript + " pre-tool-use", Timeout: 3,
			}}}},
			"Stop": {{Hooks: []claudeSettingsHook{{
				Type: "command", Command: "python3 " + hookScript + " stop", Timeout: 10,
			}}}},
		},
	}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode isolated Claude hook settings: %w", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write isolated Claude hook settings: %w", err)
	}
	return nil
}
