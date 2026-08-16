// Package harness validates versioned agent-harness capability profiles.
package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/skills"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type ProfileDocument struct {
	SchemaVersion  int               `json:"schema_version"`
	HarnessProfile CapabilityProfile `json:"harness_profile"`
}

type CapabilityProfile struct {
	ID                string            `json:"id"`
	Product           string            `json:"product"`
	Status            string            `json:"status"`
	Aliases           []string          `json:"aliases"`
	CapabilityVersion string            `json:"capability_version"`
	VerifiedAt        string            `json:"verified_at"`
	OfficialSources   []string          `json:"official_sources"`
	Detection         DetectionConfig   `json:"detection"`
	Instructions      InstructionConfig `json:"instructions"`
	Skills            SkillConfig       `json:"skills"`
	Projection        ProjectionConfig  `json:"projection"`
	Hooks             HookConfig        `json:"hooks"`
	RuntimeTests      RuntimeTestState  `json:"runtime_tests"`
}

type DetectionConfig struct {
	CommandCandidates []string `json:"command_candidates"`
	VersionArguments  []string `json:"version_arguments"`
}

type InstructionConfig struct {
	NativeAgents      bool   `json:"native_agents"`
	NestedChain       string `json:"nested_chain"`
	Imports           bool   `json:"imports"`
	DefaultLimitBytes int    `json:"default_limit_bytes,omitempty"`
}

type SkillConfig struct {
	Standard     string             `json:"standard"`
	NativePaths  []string           `json:"native_paths"`
	Symlinks     bool               `json:"symlinks"`
	ExplicitOnly ExplicitOnlyConfig `json:"explicit_only"`
}

type ExplicitOnlyConfig struct {
	Mechanism string `json:"mechanism"`
}

type ProjectionConfig struct {
	InstructionStrategy string `json:"instruction_strategy"`
	SkillStrategy       string `json:"skill_strategy"`
}

type HookConfig struct {
	Supported bool     `json:"supported"`
	Lifecycle []string `json:"lifecycle"`
}

type RuntimeTestState struct {
	Required   bool   `json:"required"`
	LastResult string `json:"last_result"`
}

type Marketplace struct {
	Name      string             `json:"name"`
	Interface map[string]any     `json:"interface"`
	Plugins   []MarketplaceEntry `json:"plugins"`
}

type MarketplaceEntry struct {
	Name     string            `json:"name"`
	Source   MarketplaceSource `json:"source"`
	Policy   map[string]string `json:"policy"`
	Category string            `json:"category"`
}

type MarketplaceSource struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

type Report struct {
	Harness              string                    `json:"harness"`
	CapabilityStatus     string                    `json:"capability_status"`
	VerifiedAt           string                    `json:"verified_at"`
	RuntimeEvidence      string                    `json:"runtime_evidence"`
	RuntimeEvidenceOwner string                    `json:"runtime_evidence_owner,omitempty"`
	ProfilePath          string                    `json:"profile_path"`
	Aliases              []string                  `json:"aliases"`
	Plugins              []skills.PackageCandidate `json:"plugins,omitempty"`
	Instructions         *InstructionReport        `json:"instructions,omitempty"`
}

func ValidateCodex(root string, schemas *validation.Set) (Report, []domain.Finding) {
	return validateCodex(root, schemas, true)
}

func ValidateCodexStatic(root string, schemas *validation.Set) (Report, []domain.Finding) {
	return validateCodex(root, schemas, false)
}

func validateCodex(
	root string,
	schemas *validation.Set,
	includeRuntime bool,
) (Report, []domain.Finding) {
	profile, report, findings := validateProfile(root, "codex", schemas, includeRuntime, resolveDelegation(root, schemas))
	profilePath := filepath.Join(root, filepath.FromSlash(report.ProfilePath))
	if profile.ID == "" {
		return report, findings
	}
	if profile.ID != "codex" || profile.Product != "codex" {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_IDENTITY_INVALID", "Codex profile identity is inconsistent.",
			map[string]any{"path": profilePath, "id": profile.ID, "product": profile.Product},
		))
	}
	if !profile.Instructions.NativeAgents || profile.Instructions.NestedChain != "root-to-cwd" ||
		profile.Instructions.DefaultLimitBytes != 32768 || profile.Instructions.Imports {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_INSTRUCTION_CONTRACT_INVALID",
			"Codex instruction capability does not match current verified behavior.",
			map[string]any{"path": profilePath},
		))
	}
	if profile.Skills.Standard != "agent-skills" || !profile.Skills.Symlinks ||
		fmt.Sprint(profile.Skills.NativePaths) != fmt.Sprint([]string{".agents/skills"}) ||
		profile.Skills.ExplicitOnly.Mechanism != "agents-openai-yaml" {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_SKILL_CONTRACT_INVALID",
			"Codex skill capability does not match the canonical projection contract.",
			map[string]any{"path": profilePath},
		))
	}
	expectedLifecycle := []string{"session-start", "pre-tool-use", "stop"}
	if !profile.Hooks.Supported || fmt.Sprint(profile.Hooks.Lifecycle) != fmt.Sprint(expectedLifecycle) {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_HOOK_CONTRACT_INVALID",
			"Codex lifecycle ownership differs from the canonical core plugin contract.",
			map[string]any{"path": profilePath},
		))
	}
	pluginFindings := validateMarketplaceAndPackages(root, schemas, &report)
	findings = append(findings, pluginFindings...)
	instructionReport, instructionFindings := inspectCodexInstructions(root)
	report.Instructions = &instructionReport
	findings = append(findings, instructionFindings...)
	sort.SliceStable(findings, func(left, right int) bool {
		return findings[left].Code < findings[right].Code
	})
	return report, findings
}

func validateMarketplaceAndPackages(
	root string,
	schemas *validation.Set,
	report *Report,
) []domain.Finding {
	marketplacePath := filepath.Join(root, ".agents", "plugins", "marketplace.json")
	raw, err := os.ReadFile(marketplacePath)
	if err != nil {
		return []domain.Finding{harnessFinding(
			"GDS_PLUGIN_MARKETPLACE_READ_FAILED", "Cannot read the repository plugin marketplace.",
			map[string]any{"path": marketplacePath, "error": err.Error()},
		)}
	}
	var marketplace Marketplace
	if err := serialization.DecodeInto(marketplacePath, raw, &marketplace); err != nil {
		return []domain.Finding{harnessFinding(
			"GDS_PLUGIN_MARKETPLACE_INVALID", "Cannot decode the repository plugin marketplace.",
			map[string]any{"path": marketplacePath, "error": err.Error()},
		)}
	}
	expected := map[string]string{
		"gds-core":         "./plugins/gds-core",
		"gds-estate-admin": "./plugins/gds-estate-admin",
		"gds-module":       "./plugins/gds-module",
	}
	findings := []domain.Finding{}
	if marketplace.Name != "gds" {
		findings = append(findings, harnessFinding(
			"GDS_PLUGIN_MARKETPLACE_IDENTITY_INVALID",
			"Repository plugin marketplace has an unexpected identity.",
			map[string]any{"name": marketplace.Name},
		))
	}
	seen := map[string]struct{}{}
	for _, entry := range marketplace.Plugins {
		path, found := expected[entry.Name]
		if !found || entry.Source.Source != "local" || entry.Source.Path != path ||
			entry.Policy["installation"] != "AVAILABLE" ||
			entry.Policy["authentication"] != "ON_INSTALL" ||
			entry.Category != "Developer Tools" {
			findings = append(findings, harnessFinding(
				"GDS_PLUGIN_MARKETPLACE_ENTRY_INVALID",
				"Plugin marketplace entry has an unexpected identity or source path.",
				map[string]any{"name": entry.Name, "path": entry.Source.Path},
			))
			continue
		}
		if _, duplicate := seen[entry.Name]; duplicate {
			findings = append(findings, harnessFinding(
				"GDS_PLUGIN_MARKETPLACE_DUPLICATE", "Plugin marketplace name occurs more than once.",
				map[string]any{"name": entry.Name},
			))
		}
		seen[entry.Name] = struct{}{}
	}
	for plugin := range expected {
		if _, found := seen[plugin]; !found {
			findings = append(findings, harnessFinding(
				"GDS_PLUGIN_MARKETPLACE_ENTRY_MISSING", "Required GDS plugin is absent from the marketplace.",
				map[string]any{"name": plugin},
			))
			continue
		}
		candidate, packageFindings := skills.BuildPackage(root, plugin, schemas)
		report.Plugins = append(report.Plugins, candidate)
		findings = append(findings, packageFindings...)
	}
	sort.Slice(report.Plugins, func(left, right int) bool {
		return report.Plugins[left].Plugin < report.Plugins[right].Plugin
	})
	return findings
}

func harnessFinding(code, message string, evidence map[string]any) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence,
	}
}
