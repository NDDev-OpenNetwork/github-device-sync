package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestReadOnlyGitEnvironmentIsAnExactAllowlist(t *testing.T) {
	home := filepath.Join(t.TempDir(), "isolated-home")
	got := readOnlyGitEnvironment(home)
	want := []string{
		"PATH=/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=" + home,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("read-only Git environment = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{
		"GITHUB_TOKEN", "CODEX_TOKEN", "BASH_ENV",
		"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH",
		"ALL_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	} {
		for _, entry := range got {
			if entry == forbidden || len(entry) > len(forbidden) &&
				entry[:len(forbidden)+1] == forbidden+"=" {
				t.Fatalf("read-only Git environment leaked %s: %#v", forbidden, got)
			}
		}
	}
}

func TestValidateMappingContractProfileBindsCanonicalEdges(t *testing.T) {
	mapping := harness.ModuleHarnessMapping{
		ModuleID: "nddev-alpha-app", HarnessID: "alpha",
		Lifecycle: "active",
	}
	module := nddevModule{
		ID: "nddev-alpha-app", Repository: "example-org/nddev-alpha-app",
	}
	module.Expectations.Contract = map[string]any{
		"contract_version":  float64(3),
		"product_name":      "nddev-alpha-app",
		"github_repository": "example-org/nddev-alpha-app",
	}
	profileObject := map[string]any{
		"harness_profile": map[string]any{
			"id": "alpha", "capability_version": "2026-07-30",
			"runtime_tests": map[string]any{"required": false},
		},
	}
	profile := gdsProfileEdge{
		Object: profileObject, CapabilityVersion: "2026-07-30",
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	nddevRoot, gitlinkSHA := exactContractFixture(t, map[string]any{
		"contract_version":  3,
		"product_name":      "nddev-alpha-app",
		"github_repository": "example-org/nddev-alpha-app",
		"runtime":           map[string]any{"fixture": true},
	})
	module.ExpectedHead = gitlinkSHA
	edge, findings := validateMappingContractProfile(
		context.Background(), nddevRoot, mapping, module, profile,
		"repo_01JEXAMPZ0000000000000000N", module.ExpectedHead,
	)
	if len(findings) != 0 {
		t.Fatalf("valid edge findings = %#v", findings)
	}
	if edge.RegistryContractVersion != 3 ||
		edge.RegistryExpectationDigest == "" ||
		edge.PublicContractVersion != 3 ||
		edge.PublicContractDigest == "" ||
		edge.RegistryExpectationDigest == edge.PublicContractDigest ||
		edge.GDSCapabilityVersion != "2026-07-30" ||
		edge.GDSProfileDigest != profile.Digest ||
		edge.ExpectedHead != edge.GitlinkSHA {
		t.Fatalf("edge = %#v", edge)
	}

}

func TestLoadGDSProfileEdgesRejectsMalformedProfileBeforeConsumption(t *testing.T) {
	root := t.TempDir()
	profilePath := filepath.Join(root, "harnesses", "alpha", "profile.yaml")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := []byte("schema_version: 1\nharnesses:\n  - id: alpha\n    profile: harnesses/alpha/profile.yaml\n")
	if err := os.WriteFile(
		filepath.Join(root, "harnesses", "capability-registry.yaml"), registry, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		profilePath,
		[]byte("schema_version: 1\nharness_profile:\n  id: alpha\n  capability_version: 7\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	edges, findings := loadGDSProfileEdges(root, schemas)
	if len(findings) == 0 || len(edges) != 0 {
		t.Fatalf("malformed profile was consumed: edges=%#v findings=%#v", edges, findings)
	}
}

func TestValidateMappingContractProfileRejectsContractAndProfileDrift(t *testing.T) {
	mapping := harness.ModuleHarnessMapping{
		ModuleID: "nddev-alpha-app", HarnessID: "alpha",
		Lifecycle: "active",
	}
	nddevRoot, gitlinkSHA := exactContractFixture(t, map[string]any{
		"contract_version":  1,
		"product_name":      "nddev-alpha-app",
		"github_repository": "example-org/nddev-alpha-app",
	})
	baseModule := nddevModule{
		ID: "nddev-alpha-app", Repository: "example-org/nddev-alpha-app",
		ExpectedHead: gitlinkSHA,
	}
	baseModule.Expectations.Contract = map[string]any{
		"contract_version":  float64(1),
		"product_name":      "nddev-alpha-app",
		"github_repository": "example-org/nddev-alpha-app",
	}
	baseProfile := gdsProfileEdge{
		Object: map[string]any{"harness_profile": map[string]any{
			"id": "alpha", "capability_version": "2026-07-30",
			"runtime_tests": map[string]any{"required": false},
		}},
		CapabilityVersion: "2026-07-30",
		Digest:            "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	tests := []struct {
		name   string
		mutate func(*nddevModule, *gdsProfileEdge)
		detail string
	}{
		{
			name: "contract product",
			mutate: func(module *nddevModule, _ *gdsProfileEdge) {
				module.Expectations.Contract["product_name"] = "nddev-beta-app"
			},
			detail: "registry contract product_name differs",
		},
		{
			name: "contract repository",
			mutate: func(module *nddevModule, _ *gdsProfileEdge) {
				module.Expectations.Contract["github_repository"] = "example-org/other"
			},
			detail: "registry contract repository differs",
		},
		{
			name: "fractional contract version",
			mutate: func(module *nddevModule, _ *gdsProfileEdge) {
				module.Expectations.Contract["contract_version"] = float64(1.5)
			},
			detail: "registry contract version is missing",
		},
		{
			name: "string contract version",
			mutate: func(module *nddevModule, _ *gdsProfileEdge) {
				module.Expectations.Contract["contract_version"] = "1"
			},
			detail: "registry contract version is missing",
		},
		{
			name: "missing contract version",
			mutate: func(module *nddevModule, _ *gdsProfileEdge) {
				delete(module.Expectations.Contract, "contract_version")
			},
			detail: "registry contract version is missing",
		},
		{
			name: "registry contract version",
			mutate: func(module *nddevModule, _ *gdsProfileEdge) {
				module.Expectations.Contract["contract_version"] = float64(2)
			},
			detail: "public contract version differs from the NDDev registry expectation",
		},
		{
			name: "profile id",
			mutate: func(_ *nddevModule, profile *gdsProfileEdge) {
				profile.Object["harness_profile"].(map[string]any)["id"] = "beta"
			},
			detail: "GDS capability profile id differs",
		},
		{
			name: "missing capability version",
			mutate: func(_ *nddevModule, profile *gdsProfileEdge) {
				profile.CapabilityVersion = nil
			},
			detail: "GDS capability version must be a non-empty string",
		},
		{
			name: "non-string capability version",
			mutate: func(_ *nddevModule, profile *gdsProfileEdge) {
				profile.CapabilityVersion = 20260730
			},
			detail: "GDS capability version must be a non-empty string",
		},
		{
			name: "runtime proof ownership",
			mutate: func(_ *nddevModule, profile *gdsProfileEdge) {
				profile.Object["harness_profile"].(map[string]any)["runtime_tests"] =
					map[string]any{"required": true}
			},
			detail: "GDS runtime_tests.required must remain false",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module := baseModule
			module.Expectations.Contract = cloneAnyMap(baseModule.Expectations.Contract)
			profile := baseProfile
			profile.Object = cloneAnyMap(baseProfile.Object)
			test.mutate(&module, &profile)
			_, findings := validateMappingContractProfile(
				context.Background(), nddevRoot, mapping, module, profile,
				"repo_01JEXAMPZ0000000000000000N", module.ExpectedHead,
			)
			if !hasBridgeDetailContaining(findings, test.detail) {
				t.Fatalf("missing %q in %#v", test.detail, findings)
			}
		})
	}
}

func TestCompareModuleBridgeAllowsRetiredMappingAbsentCurrentModule(t *testing.T) {
	bridge := harness.ModuleBridgeDocument{
		RelationshipScope: "git-submodule-consumer",
		Mappings: []harness.ModuleHarnessMapping{{
			ModuleID: "nddev-retired-app", HarnessID: "retired", Lifecycle: "retired",
		}},
	}
	edges, findings := compareModuleBridge(
		context.Background(), t.TempDir(), bridge, nddevRegistryDocument{},
		nddevAnchorDocument{}, map[string]bool{}, map[string]string{}, map[string]gdsProfileEdge{},
	)
	if len(findings) != 0 || len(edges) != 0 {
		t.Fatalf("retired historical edge must not require a current module: edges=%#v findings=%#v", edges, findings)
	}
}

func TestCompareModuleBridgeRejectsDerivedOwnerCollisions(t *testing.T) {
	bridge := harness.ModuleBridgeDocument{
		RelationshipScope: "git-submodule-consumer",
		Mappings: []harness.ModuleHarnessMapping{
			{ModuleID: "nddev-alpha-app", HarnessID: "alpha", Lifecycle: "retired"},
			{ModuleID: "nddev-beta-app", HarnessID: "beta", Lifecycle: "retired"},
		},
	}
	registry := nddevRegistryDocument{Modules: []nddevModule{
		{ID: "nddev-alpha-app", Repository: "example-org/shared"},
		{ID: "nddev-beta-app", Repository: "example-org/SHARED"},
	}}
	anchor := nddevAnchorDocument{}
	anchor.Relationships = append(anchor.Relationships,
		struct {
			Type           string `json:"type"`
			Target         string `json:"target"`
			GitmodulesName string `json:"gitmodules_name"`
		}{Type: bridge.RelationshipScope, Target: "repo_shared", GitmodulesName: "modules/nddev-alpha-app"},
		struct {
			Type           string `json:"type"`
			Target         string `json:"target"`
			GitmodulesName string `json:"gitmodules_name"`
		}{Type: bridge.RelationshipScope, Target: "repo_shared", GitmodulesName: "modules/nddev-beta-app"},
	)
	_, findings := compareModuleBridge(
		context.Background(), t.TempDir(), bridge, registry, anchor,
		map[string]bool{}, map[string]string{}, map[string]gdsProfileEdge{},
	)
	if !hasBridgeDetailContaining(findings, "public repository is owned by more than one module") {
		t.Fatalf("missing derived public repository collision: %#v", findings)
	}
	if !hasBridgeDetailContaining(findings, "repository target is owned by more than one module path") {
		t.Fatalf("missing derived repository ID collision: %#v", findings)
	}
}

func TestPublicModuleProjectionOwnershipFailsClosedAtExactGitlink(t *testing.T) {
	tests := []struct {
		name             string
		command          string
		required         string
		generatedAgents  bool
		validatorSymlink bool
		detail           string
	}{
		{
			name:            "generated projection",
			command:         publicModuleValidatorCommand,
			required:        `["test"]`,
			generatedAgents: true,
			detail:          "agent.generated_agents must be false",
		},
		{
			name:     "echo validator path",
			command:  "echo cli-tools/validate_public_contracts.py",
			required: `["test"]`,
			detail:   "exactly match the canonical validator command",
		},
		{
			name:     "shell operator suffix",
			command:  publicModuleValidatorCommand + " && echo accepted",
			required: `["test"]`,
			detail:   "exactly match the canonical validator command",
		},
		{
			name:     "alternative validator",
			command:  "python3 cli-tools/other_validator.py",
			required: `["test"]`,
			detail:   "exactly match the canonical validator command",
		},
		{
			name:     "extra arguments",
			command:  publicModuleValidatorCommand + " --verbose",
			required: `["test"]`,
			detail:   "exactly match the canonical validator command",
		},
		{
			name:     "validator not required",
			command:  publicModuleValidatorCommand,
			required: `[]`,
			detail:   "verification.required must include test",
		},
		{
			name:             "validator symlink",
			command:          publicModuleValidatorCommand,
			required:         `["test"]`,
			validatorSymlink: true,
			detail:           "tracked regular executable or non-executable file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, _ := exactContractFixture(t, map[string]any{
				"contract_version":  1,
				"product_name":      "nddev-alpha-app",
				"github_repository": "example-org/nddev-alpha-app",
			})
			moduleRoot := filepath.Join(root, "modules", "nddev-alpha-app")
			anchor := fmt.Sprintf(`agent:
  generated_agents: %t
verification:
  commands:
    test: [%q]
  required: %s
`, test.generatedAgents, test.command, test.required)
			if err := os.WriteFile(
				filepath.Join(moduleRoot, ".gds", "repository.yaml"),
				[]byte(anchor),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			paths := []string{".gds/repository.yaml"}
			if test.validatorSymlink {
				validatorPath := filepath.Join(
					moduleRoot, filepath.FromSlash(publicModuleValidatorPath),
				)
				if err := os.Remove(validatorPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../config/nddev-contract.json", validatorPath); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, publicModuleValidatorPath)
			}
			runFixtureGit(t, moduleRoot, append([]string{"add"}, paths...)...)
			runFixtureGit(
				t, moduleRoot, "-c", "user.name=GDS Test", "-c",
				"user.email=gds-test@example.invalid", "commit", "-q", "-m", "test: mutate anchor",
			)
			sha := strings.TrimSpace(runFixtureGit(t, moduleRoot, "rev-parse", "HEAD"))
			findings := validatePublicModuleProjectionOwnershipAtGitlink(
				context.Background(), root, "nddev-alpha-app", sha,
			)
			if !hasBridgeDetailContaining(findings, test.detail) {
				t.Fatalf("missing %q in %#v", test.detail, findings)
			}
		})
	}
}

func TestPublicModuleProjectionOwnershipAcceptsExactCanonicalValidator(t *testing.T) {
	root, sha := exactContractFixture(t, map[string]any{
		"contract_version":  1,
		"product_name":      "nddev-alpha-app",
		"github_repository": "example-org/nddev-alpha-app",
	})
	findings := validatePublicModuleProjectionOwnershipAtGitlink(
		context.Background(), root, "nddev-alpha-app", sha,
	)
	if len(findings) != 0 {
		t.Fatalf("exact canonical projection ownership must pass: %#v", findings)
	}
}

func exactContractFixture(t *testing.T, contract map[string]any) (string, string) {
	t.Helper()
	root := t.TempDir()
	moduleRoot := filepath.Join(root, "modules", "nddev-alpha-app")
	if err := os.MkdirAll(filepath.Join(moduleRoot, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(moduleRoot, ".gds"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(moduleRoot, "cli-tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, moduleRoot, "init", "-q", "-b", "main")
	raw, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(moduleRoot, "config", "nddev-contract.json"), append(raw, '\n'), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	anchor := []byte(`schema_version: 1
verification:
  commands:
    test:
      - "python3 cli-tools/validate_public_contracts.py"
  required:
    - "test"
agent:
  generated_agents: false
`)
	if err := os.WriteFile(filepath.Join(moduleRoot, ".gds", "repository.yaml"), anchor, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(moduleRoot, "cli-tools", "validate_public_contracts.py"),
		[]byte("raise SystemExit(0)\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(
		t, moduleRoot, "add", "config/nddev-contract.json", ".gds/repository.yaml",
		"cli-tools/validate_public_contracts.py",
	)
	runFixtureGit(
		t, moduleRoot, "-c", "user.name=GDS Test", "-c",
		"user.email=gds-test@example.invalid", "commit", "-q", "-m", "test: fixture",
	)
	return root, strings.TrimSpace(runFixtureGit(t, moduleRoot, "rev-parse", "HEAD"))
}

func runFixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("/usr/bin/git", append([]string{"-C", root}, args...)...)
	command.Env = readOnlyGitEnvironment(t.TempDir())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func cloneAnyMap(value map[string]any) map[string]any {
	result := map[string]any{}
	for key, item := range value {
		if nested, ok := item.(map[string]any); ok {
			result[key] = cloneAnyMap(nested)
		} else {
			result[key] = item
		}
	}
	return result
}

func hasBridgeDetail(findings []domain.Finding, detail string) bool {
	for _, finding := range findings {
		if finding.Evidence["detail"] == detail {
			return true
		}
	}
	return false
}

func hasBridgeDetailContaining(findings []domain.Finding, fragment string) bool {
	for _, finding := range findings {
		detail, _ := finding.Evidence["detail"].(string)
		if strings.Contains(detail, fragment) {
			return true
		}
	}
	return false
}
