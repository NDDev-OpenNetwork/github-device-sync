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

func runCodexPublicFirewallProbe(
	ctx context.Context,
	request RuntimeDriverRequest,
	environment CodexRuntimeEnvironment,
) (bool, map[string]any, string, error) {
	consumer := filepath.Join(request.EvidenceDirectory, "public-consumer")
	module := filepath.Join(consumer, "modules", "public-module")
	if err := os.MkdirAll(filepath.Join(module, ".gds"), 0o700); err != nil {
		return false, nil, "", err
	}
	markerDigest := strings.TrimPrefix(bytesDigest([]byte(request.ProfileDigest)), "sha256:")
	privateMarker := "private-gds-" + markerDigest[:16]
	publicMarker := "public-gds-" + markerDigest[16:32]
	if err := os.WriteFile(filepath.Join(consumer, "AGENTS.md"), []byte(
		"# Private consumer\n\n- Scope marker: `"+privateMarker+"`.\n",
	), 0o600); err != nil {
		return false, nil, "", err
	}
	if err := os.WriteFile(filepath.Join(module, "AGENTS.md"), []byte(
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
	schema, err := ensureCodexDriverSchema(request.EvidenceDirectory, "firewall", []byte(`{
  "type":"object","additionalProperties":false,"required":["markers"],
  "properties":{"markers":{"type":"array","items":{"type":"string"}}}
}
`))
	if err != nil {
		return false, nil, "", err
	}
	result, err := RunCodexRuntime(ctx, CodexRuntimeOptions{
		Executable: request.Environment.Executable, RepositoryRoot: module,
		ModelLabel:   request.ModelLabel,
		Prompt:       "Return every Scope marker loaded into this public-module session. Do not search outside the repository or run tools.",
		OutputSchema: schema, Environment: environment.Variables, Timeout: 10 * time.Minute,
	})
	if err != nil {
		return false, nil, "", err
	}
	var output struct {
		Markers []string `json:"markers"`
	}
	if err := DecodeCodexFinalJSON(result.Observation, &output); err != nil {
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

func runCodexHookLifecycleProbe(
	ctx context.Context,
	request RuntimeDriverRequest,
	environment CodexRuntimeEnvironment,
	fixture CodexRuntimeFixture,
) (bool, map[string]any, string, error) {
	marketplaceRaw, err := runBoundedCodexCommand(
		ctx, request.Environment.Executable,
		[]string{"plugin", "marketplace", "add", request.RepositoryRoot, "--json"},
		fixture.Root, environment.Variables, nil,
	)
	if err != nil {
		return false, nil, "", err
	}
	var marketplace struct {
		Name string `json:"marketplaceName"`
	}
	if err := json.Unmarshal(marketplaceRaw, &marketplace); err != nil || marketplace.Name != "gds" {
		return false, nil, "", fmt.Errorf("Codex did not install the exact local GDS marketplace")
	}
	pluginRaw, err := runBoundedCodexCommand(
		ctx, request.Environment.Executable,
		[]string{"plugin", "add", "gds-core@gds", "--json"},
		fixture.Root, environment.Variables, nil,
	)
	if err != nil {
		return false, nil, "", err
	}
	var installed struct {
		PluginID      string `json:"pluginId"`
		InstalledPath string `json:"installedPath"`
	}
	if err := json.Unmarshal(pluginRaw, &installed); err != nil ||
		installed.PluginID != "gds-core@gds" || !filepath.IsAbs(installed.InstalledPath) {
		return false, nil, "", fmt.Errorf("Codex plugin install output is invalid")
	}
	hooksPath := filepath.Join(installed.InstalledPath, "hooks", "hooks.json")
	hookScript := filepath.Join(installed.InstalledPath, "hooks", "gds_hook.py")
	hooksRaw, err := os.ReadFile(hooksPath)
	if err != nil {
		return false, nil, "", err
	}
	sourceHooks, err := os.ReadFile(filepath.Join(request.RepositoryRoot, "plugins", "gds-core", "hooks", "hooks.json"))
	if err != nil || bytesDigest(hooksRaw) != bytesDigest(sourceHooks) {
		return false, nil, "", fmt.Errorf("installed hook configuration differs from canonical source")
	}
	var hooks struct {
		Hooks map[string][]struct {
			Handlers []struct {
				Timeout int `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(hooksRaw, &hooks); err != nil {
		return false, nil, "", err
	}
	for event, timeout := range map[string]int{"SessionStart": 5, "PreToolUse": 3, "Stop": 10} {
		groups := hooks.Hooks[event]
		if len(groups) != 1 || len(groups[0].Handlers) != 1 || groups[0].Handlers[0].Timeout != timeout {
			return false, nil, "", fmt.Errorf("hook event %s does not match the exact lifecycle contract", event)
		}
	}
	hookEnvironment := append(
		append([]string(nil), environment.Variables...), "GDS_BIN="+request.GDSExecutable,
	)
	schema, err := ensureCodexDriverSchema(request.EvidenceDirectory, "hook-session", []byte(`{
  "type":"object","additionalProperties":false,"required":["context"],
  "properties":{"context":{"type":"string"}}
}
`))
	if err != nil {
		return false, nil, "", err
	}
	session, err := RunCodexRuntime(ctx, CodexRuntimeOptions{
		Executable: request.Environment.Executable, RepositoryRoot: fixture.Root,
		ModelLabel:   request.ModelLabel,
		Prompt:       "Return the GDS SessionStart additional context exactly. Do not run tools.",
		OutputSchema: schema, Environment: hookEnvironment, Timeout: 10 * time.Minute,
		BypassHookTrust: true,
	})
	if err != nil {
		return false, nil, "", err
	}
	var sessionOutput struct {
		Context string `json:"context"`
	}
	if err := DecodeCodexFinalJSON(session.Observation, &sessionOutput); err != nil {
		return false, nil, "", err
	}
	sessionSeen := strings.Contains(sessionOutput.Context, "GDS session context") ||
		strings.Contains(sessionOutput.Context, "GDS context is NOT_PROVEN")

	preInput := []byte(`{"cwd":"` + fixture.Root + `","tool_input":{"command":"git reset --hard HEAD"}}`)
	preRaw, err := runBoundedCodexCommand(
		ctx, "python3", []string{hookScript, "pre-tool-use"}, fixture.Root, hookEnvironment, preInput,
	)
	if err != nil {
		return false, nil, "", err
	}
	var pre map[string]any
	if err := json.Unmarshal(preRaw, &pre); err != nil {
		return false, nil, "", err
	}
	preText := string(preRaw)
	preDenied := strings.Contains(preText, `"permissionDecision":"deny"`)

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
	combined := string(marketplaceRaw) + string(pluginRaw) + string(session.Transcript) +
		string(preRaw) + string(stopRaw) + string(invalidRaw)
	return passed, map[string]any{
		"trust_mode":             "externally-vetted-bypass",
		"canonical_hooks_digest": bytesDigest(sourceHooks),
		"session_start_seen":     sessionSeen, "pre_tool_use_denied": preDenied,
		"stop_completed": stopContinues, "failure_handled": invalidHandled,
	}, combined, nil
}
