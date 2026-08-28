package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// liveBridgeMappingCount is the number of mappings parity produces an edge for.
// Retired mappings name modules the NDDev side no longer carries, so they are
// checked for absence and yield no edge. Deriving this keeps the assertion
// honest when a harness is retired instead of pinning a number that silently
// stops matching the file it describes.
func liveBridgeMappingCount(t *testing.T, gdsRoot string) int {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	bridge, _, findings := harness.LoadModuleBridge(gdsRoot, schemas)
	if len(findings) != 0 {
		t.Fatalf("bridge findings: %+v", findings)
	}
	live := 0
	for _, mapping := range bridge.Mappings {
		if mapping.Lifecycle != "retired" {
			live++
		}
	}
	return live
}

func TestModuleBridgeParityCLIRequiresTwoRootsAndReturnsComputedDigests(t *testing.T) {
	gdsRoot := repositoryRoot(t)
	nddevRoot := buildNDDevBridgeFixture(t, gdsRoot)
	gdsBefore := bridgeGitStatus(t, gdsRoot)
	nddevBefore := bridgeGitStatus(t, nddevRoot)

	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", gdsRoot, "harness", "bridge", "parity",
		"--gds-root", gdsRoot, "--nddev-root", nddevRoot,
	)
	if exitCode != 0 || len(envelope.Findings) != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok || data["contract"] != harness.ModuleBridgeContract ||
		data["mappings"] != float64(17) {
		t.Fatalf("data = %#v", envelope.Data)
	}
	if !strings.HasPrefix(fmt.Sprint(data["identity_digest"]), "sha256:") ||
		!strings.HasPrefix(fmt.Sprint(data["parity_digest"]), "sha256:") {
		t.Fatalf("computed digests = %#v", data)
	}
	edges, ok := data["mapping_edges"].([]any)
	if !ok || len(edges) != liveBridgeMappingCount(t, gdsRoot) {
		t.Fatalf("mapping edges = %#v", data["mapping_edges"])
	}
	for _, rawEdge := range edges {
		edge, ok := rawEdge.(map[string]any)
		if !ok ||
			edge["expected_head"] != edge["gitlink_sha"] ||
			!strings.HasPrefix(fmt.Sprint(edge["registry_expectation_digest"]), "sha256:") ||
			!strings.HasPrefix(fmt.Sprint(edge["public_contract_digest"]), "sha256:") ||
			edge["registry_expectation_digest"] == edge["public_contract_digest"] ||
			!strings.HasPrefix(fmt.Sprint(edge["gds_profile_digest"]), "sha256:") ||
			fmt.Sprint(edge["gds_capability_version"]) == "" {
			t.Fatalf("unbound mapping edge = %#v", rawEdge)
		}
	}
	inputs, ok := data["input_digests"].(map[string]any)
	if !ok || len(inputs) != 8 {
		t.Fatalf("input digests = %#v", data["input_digests"])
	}
	if _, persistedPublicDigest := inputs["public_contract"]; persistedPublicDigest {
		t.Fatalf("mutable public contract digest leaked into identity inputs: %#v", inputs)
	}
	if gdsAfter := bridgeGitStatus(t, gdsRoot); gdsAfter != gdsBefore {
		t.Fatalf("GDS status changed:\nbefore=%s\nafter=%s", gdsBefore, gdsAfter)
	}
	if nddevAfter := bridgeGitStatus(t, nddevRoot); nddevAfter != nddevBefore {
		t.Fatalf("NDDev status changed:\nbefore=%s\nafter=%s", nddevBefore, nddevAfter)
	}

	exitCode, envelope, _ = executeJSON(
		t, "--json", "--cwd", gdsRoot, "harness", "bridge", "parity",
		"--gds-root", gdsRoot,
	)
	if exitCode == 0 || !containsFinding(envelope.Findings, "GDS_MODULE_BRIDGE_ROOT_INVALID") {
		t.Fatalf("missing explicit NDDev root must fail closed: %#v", envelope)
	}
}

func TestModuleBridgeValidateCLIIsSingleRepositoryOnly(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "harness", "bridge", "validate",
		"--gds-root", root,
	)
	if exitCode != 0 || len(envelope.Findings) != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok || data["mappings"] != float64(17) {
		t.Fatalf("data = %#v", envelope.Data)
	}
}

func TestModuleBridgeParityDigestsInputDriftWithoutChangingIdentity(t *testing.T) {
	gdsRoot := repositoryRoot(t)
	nddevRoot := buildNDDevBridgeFixture(t, gdsRoot)
	run := func() map[string]any {
		t.Helper()
		exitCode, envelope, stderr := executeJSON(
			t, "--json", "--cwd", gdsRoot, "harness", "bridge", "parity",
			"--gds-root", gdsRoot, "--nddev-root", nddevRoot,
		)
		if exitCode != 0 || len(envelope.Findings) != 0 {
			t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
		}
		data, ok := envelope.Data.(map[string]any)
		if !ok {
			t.Fatalf("data = %#v", envelope.Data)
		}
		return data
	}
	before := run()
	path := filepath.Join(nddevRoot, "config", "repositories.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	after := run()
	if before["identity_digest"] != after["identity_digest"] {
		t.Fatalf("NDDev formatting drift changed bridge identity: before=%#v after=%#v", before, after)
	}
	if before["parity_digest"] == after["parity_digest"] {
		t.Fatal("NDDev formatting drift did not change parity digest")
	}
	beforeInputs := before["input_digests"].(map[string]any)
	afterInputs := after["input_digests"].(map[string]any)
	if beforeInputs["nddev_module_registry"] == afterInputs["nddev_module_registry"] {
		t.Fatal("NDDev registry input digest did not bind exact input bytes")
	}
}

func TestModuleBridgeParityReadsPublicContractFromExactGitlink(t *testing.T) {
	gdsRoot := repositoryRoot(t)
	nddevRoot := buildNDDevBridgeFixture(t, gdsRoot)
	run := func() map[string]any {
		t.Helper()
		exitCode, envelope, stderr := executeJSON(
			t, "--json", "--cwd", gdsRoot, "harness", "bridge", "parity",
			"--gds-root", gdsRoot, "--nddev-root", nddevRoot,
		)
		if exitCode != 0 || len(envelope.Findings) != 0 {
			t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
		}
		data := envelope.Data.(map[string]any)
		for _, rawEdge := range data["mapping_edges"].([]any) {
			edge := rawEdge.(map[string]any)
			if edge["module_id"] == "pi-setup-system" {
				return edge
			}
		}
		t.Fatal("missing zcode mapping edge")
		return nil
	}
	before := run()
	contractPath := filepath.Join(
		nddevRoot, "modules", "pi-setup-system", "config", "nddev-contract.json",
	)
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	var contract map[string]any
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	contract["uncommitted_probe"] = true
	mutated, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeBridgeFixture(t, contractPath, append(mutated, '\n'))
	after := run()
	if before["public_contract_digest"] != after["public_contract_digest"] {
		t.Fatalf("working-tree drift changed exact-gitlink digest: before=%#v after=%#v", before, after)
	}
	if before["registry_expectation_digest"] != after["registry_expectation_digest"] {
		t.Fatalf("working-tree drift changed registry expectation digest: before=%#v after=%#v", before, after)
	}
}

func TestModuleBridgeParityRejectsAmbientGitAndRepositoryHelperInjection(t *testing.T) {
	gdsRoot := repositoryRoot(t)
	nddevRoot := buildNDDevBridgeFixture(t, gdsRoot)
	poisonRoot := t.TempDir()
	fakeGitMarker := filepath.Join(poisonRoot, "fake-git-ran")
	fsmonitorMarker := filepath.Join(poisonRoot, "fsmonitor-ran")
	bashEnvMarker := filepath.Join(poisonRoot, "bash-env-ran")
	fakeGit := filepath.Join(poisonRoot, "git")
	writeBridgeFixture(t, fakeGit, []byte(
		"#!/bin/sh\nprintf ran >"+fakeGitMarker+"\nexit 99\n",
	))
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	fsmonitor := filepath.Join(poisonRoot, "fsmonitor.sh")
	writeBridgeFixture(t, fsmonitor, []byte(
		"#!/bin/sh\nprintf '%s' \"$GITHUB_TOKEN:$CODEX_TOKEN\" >"+fsmonitorMarker+"\nexit 0\n",
	))
	if err := os.Chmod(fsmonitor, 0o700); err != nil {
		t.Fatal(err)
	}
	bashEnv := filepath.Join(poisonRoot, "bash-env.sh")
	writeBridgeFixture(t, bashEnv, []byte(
		"printf '%s' \"$GITHUB_TOKEN:$CODEX_TOKEN\" >"+bashEnvMarker+"\n",
	))
	globalConfig := filepath.Join(poisonRoot, "global.gitconfig")
	writeBridgeFixture(t, globalConfig, []byte(
		"[core]\n\tfsmonitor = "+fsmonitor+"\n\thooksPath = "+poisonRoot+"\n",
	))
	bridgeRunGit(t, nddevRoot, "config", "--local", "core.fsmonitor", fsmonitor)
	bridgeRunGit(t, nddevRoot, "config", "--local", "core.hooksPath", poisonRoot)

	t.Setenv("PATH", poisonRoot)
	t.Setenv("HOME", poisonRoot)
	t.Setenv("BASH_ENV", bashEnv)
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GITHUB_TOKEN", "provider-secret")
	t.Setenv("CODEX_TOKEN", "model-secret")

	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", gdsRoot, "harness", "bridge", "parity",
		"--gds-root", gdsRoot, "--nddev-root", nddevRoot,
	)
	if exitCode != 0 || len(envelope.Findings) != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	for _, marker := range []string{fakeGitMarker, fsmonitorMarker, bashEnvMarker} {
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Fatalf("poisoned Git boundary executed repository or parent helper %s: %v", marker, err)
		}
	}
}

func TestModuleBridgeParityRejectsStaleGitlinkAgainstExpectedHead(t *testing.T) {
	gdsRoot := repositoryRoot(t)
	nddevRoot := buildNDDevBridgeFixture(t, gdsRoot)
	bridgeRunGit(
		t, nddevRoot, "update-index", "--add", "--cacheinfo",
		"160000,2222222222222222222222222222222222222222,modules/pi-setup-system",
	)
	exitCode, envelope, _ := executeJSON(
		t, "--json", "--cwd", gdsRoot, "harness", "bridge", "parity",
		"--gds-root", gdsRoot, "--nddev-root", nddevRoot,
	)
	if exitCode == 0 || !containsFinding(
		envelope.Findings, "GDS_MODULE_BRIDGE_PARITY_MISMATCH",
	) {
		t.Fatalf("stale gitlink must fail closed: %#v", envelope)
	}
	found := false
	for _, finding := range envelope.Findings {
		if finding.Code == "GDS_MODULE_BRIDGE_PARITY_MISMATCH" &&
			finding.Evidence["identity"] == "pi-setup-system" &&
			finding.Evidence["detail"] == "stage-zero gitlink differs from expected_head" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing exact stale-gitlink evidence: %#v", envelope.Findings)
	}
}

func buildNDDevBridgeFixture(t *testing.T, gdsRoot string) string {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	bridge, _, findings := harness.LoadModuleBridge(gdsRoot, schemas)
	if len(findings) != 0 {
		t.Fatalf("bridge findings = %#v", findings)
	}
	root := t.TempDir()
	bridgeRunGit(t, root, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".gds"), 0o700); err != nil {
		t.Fatal(err)
	}
	modules := make([]map[string]any, 0, len(bridge.Mappings))
	var gitmodules strings.Builder
	var anchor strings.Builder
	// The NDDev anchor must claim the same consumer identity the bridge names;
	// parity compares exactly that. Deriving it keeps the fixture honest when
	// the bridge names a real repository instead of a placeholder.
	anchor.WriteString(fmt.Sprintf(
		"repository:\n  id: %s\nrelationships:\n", bridge.Consumer.RepositoryID,
	))
	sorted := append([]harness.ModuleHarnessMapping(nil), bridge.Mappings...)
	sort.Slice(sorted, func(left, right int) bool {
		return sorted[left].ModuleID < sorted[right].ModuleID
	})
	for index, mapping := range sorted {
		// A retired mapping names a module the NDDev side no longer carries.
		// Materializing one here would build a fixture that contradicts the
		// contract parity enforces -- and parity would be right to fail it.
		if mapping.Lifecycle == "retired" {
			continue
		}
		manifest := filepath.ToSlash(filepath.Join(
			"validation", mapping.ModuleID, "harness.json",
		))
		modulePath := filepath.ToSlash(filepath.Join("modules", mapping.ModuleID))
		publicRepository := "example-org/" + mapping.ModuleID
		moduleRoot := filepath.Join(root, filepath.FromSlash(modulePath))
		if err := os.MkdirAll(filepath.Join(moduleRoot, "config"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(moduleRoot, ".gds"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(moduleRoot, "cli-tools"), 0o700); err != nil {
			t.Fatal(err)
		}
		bridgeRunGit(t, moduleRoot, "init", "-q", "-b", "main")
		publicContract := map[string]any{
			"contract_version":  1,
			"product_name":      mapping.ModuleID,
			"github_repository": publicRepository,
			"runtime_compatibility": map[string]any{
				"fixture": true,
			},
		}
		publicContractRaw, err := json.MarshalIndent(publicContract, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		writeBridgeFixture(
			t, filepath.Join(moduleRoot, "config", "nddev-contract.json"),
			append(publicContractRaw, '\n'),
		)
		writeBridgeFixture(
			t, filepath.Join(moduleRoot, ".gds", "repository.yaml"),
			[]byte(
				"agent:\n  generated_agents: false\n"+
					"verification:\n  commands:\n"+
					"    test:\n      - python3 cli-tools/validate_public_contracts.py\n"+
					"  required:\n    - test\n",
			),
		)
		writeBridgeFixture(
			t, filepath.Join(moduleRoot, "cli-tools", "validate_public_contracts.py"),
			[]byte("raise SystemExit(0)\n"),
		)
		bridgeRunGit(
			t, moduleRoot, "add", "config/nddev-contract.json",
			".gds/repository.yaml", "cli-tools/validate_public_contracts.py",
		)
		bridgeRunGit(
			t, moduleRoot, "-c", "user.name=GDS Test", "-c",
			"user.email=gds-test@example.invalid", "commit", "-q", "-m", "test: fixture",
		)
		gitlinkSHA := strings.TrimSpace(bridgeRunGit(t, moduleRoot, "rev-parse", "HEAD"))
		modules = append(modules, map[string]any{
			"id": mapping.ModuleID, "repository": publicRepository,
			"path":                modulePath,
			"expected_head":       gitlinkSHA,
			"validation_manifest": manifest,
			"expectations": map[string]any{
				"contract": map[string]any{
					"contract_version":  1,
					"product_name":      mapping.ModuleID,
					"github_repository": publicRepository,
				},
			},
		})
		gitmodules.WriteString(fmt.Sprintf(
			"[submodule %q]\n\tpath = %s\n\turl = https://example.invalid/%s.git\n",
			modulePath, modulePath, mapping.ModuleID,
		))
		anchor.WriteString(fmt.Sprintf(
			"  - type: git-submodule-consumer\n    target: %s\n    gitmodules_name: %s\n",
			fmt.Sprintf("repo_fixture_%02d", index), modulePath,
		))
		manifestPath := filepath.Join(root, filepath.FromSlash(manifest))
		if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		bridgeRunGit(
			t, root, "update-index", "--add", "--cacheinfo",
			"160000,"+gitlinkSHA+","+modulePath,
		)
	}
	registryRaw, err := json.MarshalIndent(map[string]any{"modules": modules}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeBridgeFixture(t, filepath.Join(root, "config", "repositories.json"), append(registryRaw, '\n'))
	writeBridgeFixture(t, filepath.Join(root, ".gds", "repository.yaml"), []byte(anchor.String()))
	writeBridgeFixture(t, filepath.Join(root, ".gitmodules"), []byte(gitmodules.String()))
	return root
}

func writeBridgeFixture(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func bridgeRunGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	home := t.TempDir()
	hooksPath := filepath.Join(home, "hooks-disabled")
	if err := os.Mkdir(hooksPath, 0o700); err != nil {
		t.Fatal(err)
	}
	commandArgs := []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + hooksPath,
		"-C", root,
	}
	commandArgs = append(commandArgs, args...)
	command := exec.Command("/usr/bin/git", commandArgs...)
	command.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + home,
		"LANG=C", "LC_ALL=C",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return stdout.String()
}

func bridgeGitStatus(t *testing.T, root string) string {
	t.Helper()
	return bridgeRunGit(t, root, "status", "--short")
}
