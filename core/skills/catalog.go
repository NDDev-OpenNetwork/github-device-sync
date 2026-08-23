// Package skills validates canonical Agent Skills and their profiled Codex metadata.
package skills

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maxSkillFileBytes = 512 << 10

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var requiredSections = []string{
	"# Contract",
	"## Use when",
	"## Do not use when",
	"## Inputs",
	"## Preconditions",
	"## Workflow",
	"## Stop conditions",
	"## Verification",
	"## Output",
	"## References",
}

type Registry struct {
	SchemaVersion int             `json:"schema_version"`
	Namespace     string          `json:"namespace"`
	Budgets       Budgets         `json:"budgets"`
	Profiles      []Profile       `json:"profiles"`
	Plugins       []PluginProfile `json:"plugins"`
	Skills        []Definition    `json:"skills"`
}

type Budgets struct {
	InitialMetadataChars int `json:"initial_metadata_chars"`
	DescriptionChars     int `json:"description_chars"`
	SkillLines           int `json:"skill_lines"`
}

type Profile struct {
	ID     string   `json:"id"`
	Scope  string   `json:"scope"`
	Skills []string `json:"skills"`
}

type PluginProfile struct {
	ID       string   `json:"id"`
	Profiles []string `json:"profiles"`
}

type Definition struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Invocation  string    `json:"invocation"`
	Mutation    string    `json:"mutation"`
	Interface   Interface `json:"interface"`
	Description string    `json:"-"`
}

type Interface struct {
	DisplayName      string `json:"display_name"`
	ShortDescription string `json:"short_description"`
	DefaultPrompt    string `json:"default_prompt"`
}

type frontmatter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type openAIProjection struct {
	Interface Interface         `json:"interface"`
	Policy    *invocationPolicy `json:"policy,omitempty"`
}

type invocationPolicy struct {
	AllowImplicitInvocation bool `json:"allow_implicit_invocation"`
}

type BudgetUse struct {
	Plugin string `json:"plugin"`
	Skills int    `json:"skills"`
	Chars  int    `json:"initial_metadata_chars"`
	Limit  int    `json:"limit"`
}

type Report struct {
	RegistryPath string         `json:"registry_path"`
	SkillCount   int            `json:"skill_count"`
	ProfileCount int            `json:"profile_count"`
	PluginCount  int            `json:"plugin_count"`
	BudgetUse    []BudgetUse    `json:"plugin_budgets"`
	EvalCoverage []EvalCoverage `json:"eval_coverage"`
}

type Outcome struct {
	Registry Registry
	Report   Report
	Findings []domain.Finding
}

func Validate(root string, schemas *validation.Set) Outcome {
	registryPath := filepath.Join(root, "skills", "registry.yaml")
	findings := schemas.ValidateFile("skill-registry", registryPath)
	if len(findings) != 0 {
		return Outcome{Report: Report{RegistryPath: registryPath}, Findings: findings}
	}

	var registry Registry
	value, err := os.ReadFile(registryPath)
	if err != nil {
		return Outcome{Report: Report{RegistryPath: registryPath}, Findings: []domain.Finding{
			finding("GDS_SKILL_REGISTRY_READ_FAILED", "Cannot read the skill registry.", registryPath, err),
		}}
	}
	if err := serialization.DecodeInto(registryPath, value, &registry); err != nil {
		return Outcome{Report: Report{RegistryPath: registryPath}, Findings: []domain.Finding{
			finding("GDS_SKILL_REGISTRY_DECODE_FAILED", "Cannot decode the skill registry.", registryPath, err),
		}}
	}

	report, semanticFindings := validateRegistry(root, &registry)
	report.RegistryPath = registryPath
	findings = append(findings, semanticFindings...)
	evalCoverage, evalFindings := validateEvals(root, registry, schemas)
	report.EvalCoverage = evalCoverage
	findings = append(findings, evalFindings...)
	sortFindings(findings)
	return Outcome{Registry: registry, Report: report, Findings: findings}
}

func validateRegistry(root string, registry *Registry) (Report, []domain.Finding) {
	report := Report{
		SkillCount: len(registry.Skills), ProfileCount: len(registry.Profiles),
		PluginCount: len(registry.Plugins),
	}
	findings := []domain.Finding{}
	definitions := map[string]*Definition{}
	profileMembership := map[string]int{}
	for index := range registry.Skills {
		definition := &registry.Skills[index]
		if _, duplicate := definitions[definition.Name]; duplicate {
			findings = append(findings, simpleFinding(
				"GDS_SKILL_DUPLICATE_NAME", "A canonical skill name occurs more than once.",
				map[string]any{"name": definition.Name, "index": index},
			))
			continue
		}
		definitions[definition.Name] = definition
		if definition.Mutation == "external" && definition.Invocation != "explicit-only" {
			findings = append(findings, simpleFinding(
				"GDS_SKILL_EXTERNAL_MUTATION_NOT_EXPLICIT",
				"Every externally mutating skill must be explicit-only.",
				map[string]any{"name": definition.Name},
			))
		}
		findings = append(findings, validateSkill(root, registry.Budgets, definition)...)
	}

	profiles := map[string]Profile{}
	for index, profile := range registry.Profiles {
		if _, duplicate := profiles[profile.ID]; duplicate {
			findings = append(findings, simpleFinding(
				"GDS_SKILL_PROFILE_DUPLICATE", "A skill profile ID occurs more than once.",
				map[string]any{"profile": profile.ID, "index": index},
			))
			continue
		}
		profiles[profile.ID] = profile
		for _, name := range profile.Skills {
			if _, found := definitions[name]; !found {
				findings = append(findings, simpleFinding(
					"GDS_SKILL_PROFILE_REFERENCE_UNKNOWN",
					"A skill profile references an unknown canonical skill.",
					map[string]any{"profile": profile.ID, "name": name},
				))
				continue
			}
			profileMembership[name]++
		}
	}
	for name := range definitions {
		if profileMembership[name] == 0 {
			findings = append(findings, simpleFinding(
				"GDS_SKILL_UNPROFILED", "A canonical skill belongs to no active profile.",
				map[string]any{"name": name},
			))
		}
	}

	pluginIDs := map[string]struct{}{}
	for index, plugin := range registry.Plugins {
		if _, duplicate := pluginIDs[plugin.ID]; duplicate {
			findings = append(findings, simpleFinding(
				"GDS_SKILL_PLUGIN_DUPLICATE", "A plugin ID occurs more than once.",
				map[string]any{"plugin": plugin.ID, "index": index},
			))
			continue
		}
		pluginIDs[plugin.ID] = struct{}{}
		selected := map[string]struct{}{}
		for _, profileID := range plugin.Profiles {
			profile, found := profiles[profileID]
			if !found {
				findings = append(findings, simpleFinding(
					"GDS_SKILL_PLUGIN_PROFILE_UNKNOWN",
					"A plugin references an unknown skill profile.",
					map[string]any{"plugin": plugin.ID, "profile": profileID},
				))
				continue
			}
			for _, name := range profile.Skills {
				selected[name] = struct{}{}
			}
		}
		used := 0
		for name := range selected {
			definition, found := definitions[name]
			if !found {
				continue
			}
			used += len(definition.Name) + len(definition.Description)
		}
		report.BudgetUse = append(report.BudgetUse, BudgetUse{
			Plugin: plugin.ID, Skills: len(selected), Chars: used,
			Limit: registry.Budgets.InitialMetadataChars,
		})
		if used > registry.Budgets.InitialMetadataChars {
			findings = append(findings, simpleFinding(
				"GDS_SKILL_PLUGIN_METADATA_BUDGET_EXCEEDED",
				"A plugin exceeds the configured initial skill metadata budget.",
				map[string]any{"plugin": plugin.ID, "observed": used, "limit": registry.Budgets.InitialMetadataChars},
			))
		}
	}
	sort.Slice(report.BudgetUse, func(left, right int) bool {
		return report.BudgetUse[left].Plugin < report.BudgetUse[right].Plugin
	})
	findings = append(findings, findUnregisteredSkills(root, definitions)...)
	return report, findings
}

func validateSkill(root string, budgets Budgets, definition *Definition) []domain.Finding {
	findings := []domain.Finding{}
	expectedPath := filepath.ToSlash(filepath.Join("skills", "canonical", definition.Name))
	if definition.Path != expectedPath || !skillNamePattern.MatchString(definition.Name) {
		return []domain.Finding{simpleFinding(
			"GDS_SKILL_PATH_INVALID", "Skill path and name must match the canonical GDS namespace.",
			map[string]any{"name": definition.Name, "path": definition.Path, "expected": expectedPath},
		)}
	}
	skillRoot, safe := safePath(root, definition.Path)
	if !safe {
		return []domain.Finding{simpleFinding(
			"GDS_SKILL_PATH_OUTSIDE_ROOT", "Skill path escapes the estate root.",
			map[string]any{"name": definition.Name, "path": definition.Path},
		)}
	}
	if fileInfo, err := os.Lstat(skillRoot); err != nil || !fileInfo.IsDir() || fileInfo.Mode()&os.ModeSymlink != 0 {
		return []domain.Finding{finding(
			"GDS_SKILL_DIRECTORY_INVALID", "Canonical skill must be a real directory.", skillRoot, err,
		)}
	}

	skillPath := filepath.Join(skillRoot, "SKILL.md")
	raw, err := readRegularFile(skillPath)
	if err != nil {
		return []domain.Finding{finding(
			"GDS_SKILL_FILE_INVALID", "Canonical SKILL.md must be a bounded regular file.", skillPath, err,
		)}
	}
	if bytes.Contains(raw, []byte("\r")) || !bytes.HasSuffix(raw, []byte("\n")) {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_TEXT_FORMAT_INVALID", "SKILL.md must use LF and end with a newline.",
			map[string]any{"path": skillPath},
		))
	}
	if bytes.Contains(raw, []byte("[TODO:")) {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_TODO_PRESENT", "SKILL.md contains an unresolved scaffold marker.",
			map[string]any{"path": skillPath},
		))
	}
	lineCount := bytes.Count(raw, []byte("\n"))
	if lineCount > budgets.SkillLines {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_LINE_BUDGET_EXCEEDED", "SKILL.md exceeds the configured line budget.",
			map[string]any{"path": skillPath, "observed": lineCount, "limit": budgets.SkillLines},
		))
	}
	metadata, body, err := parseFrontmatter(skillPath, raw)
	if err != nil {
		findings = append(findings, finding(
			"GDS_SKILL_FRONTMATTER_INVALID", "Cannot parse SKILL.md frontmatter.", skillPath, err,
		))
		return findings
	}
	definition.Description = metadata.Description
	if metadata.Name != definition.Name {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_NAME_MISMATCH", "SKILL.md name does not match its registry and directory.",
			map[string]any{"path": skillPath, "expected": definition.Name, "observed": metadata.Name},
		))
	}
	if len(metadata.Description) > budgets.DescriptionChars {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_DESCRIPTION_BUDGET_EXCEEDED", "Skill description exceeds the internal metadata budget.",
			map[string]any{"name": definition.Name, "observed": len(metadata.Description), "limit": budgets.DescriptionChars},
		))
	}
	if !strings.HasPrefix(metadata.Description, "Use this skill") || !strings.Contains(metadata.Description, "Do not use") {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_DESCRIPTION_BOUNDARY_MISSING",
			"Skill description must state positive and negative routing boundaries.",
			map[string]any{"name": definition.Name},
		))
	}
	for _, section := range requiredSections {
		if !hasHeading(body, section) {
			findings = append(findings, simpleFinding(
				"GDS_SKILL_SECTION_MISSING", "SKILL.md is missing a required contract section.",
				map[string]any{"name": definition.Name, "section": section},
			))
		}
	}
	findings = append(findings, validateOpenAIProjection(skillRoot, definition)...)
	return findings
}

func validateOpenAIProjection(skillRoot string, definition *Definition) []domain.Finding {
	path := filepath.Join(skillRoot, "agents", "openai.yaml")
	agentsInfo, agentsErr := os.Lstat(filepath.Dir(path))
	if agentsErr != nil || !agentsInfo.IsDir() || agentsInfo.Mode()&os.ModeSymlink != 0 {
		return []domain.Finding{finding(
			"GDS_SKILL_OPENAI_DIRECTORY_INVALID",
			"The Codex sidecar directory must be a real directory.", filepath.Dir(path), agentsErr,
		)}
	}
	raw, err := readRegularFile(path)
	if err != nil {
		return []domain.Finding{finding(
			"GDS_SKILL_OPENAI_PROJECTION_INVALID",
			"The Codex skill sidecar must be a bounded regular file.", path, err,
		)}
	}
	var projection openAIProjection
	if err := serialization.DecodeInto(path, raw, &projection); err != nil {
		return []domain.Finding{finding(
			"GDS_SKILL_OPENAI_PROJECTION_INVALID", "Cannot decode the Codex skill sidecar.", path, err,
		)}
	}
	findings := []domain.Finding{}
	if projection.Interface != definition.Interface {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_OPENAI_INTERFACE_DRIFT",
			"The Codex sidecar interface differs from the canonical skill registry.",
			map[string]any{"name": definition.Name, "path": path},
		))
	}
	explicit := definition.Invocation == "explicit-only"
	if explicit && (projection.Policy == nil || projection.Policy.AllowImplicitInvocation) {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_EXPLICIT_ONLY_PROJECTION_MISSING",
			"An explicit-only skill must disable implicit Codex invocation.",
			map[string]any{"name": definition.Name, "path": path},
		))
	}
	if !explicit && projection.Policy != nil && !projection.Policy.AllowImplicitInvocation {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_IMPLICIT_PROJECTION_DISABLED",
			"An implicitly routable skill is disabled in its Codex sidecar.",
			map[string]any{"name": definition.Name, "path": path},
		))
	}
	if !strings.Contains(projection.Interface.DefaultPrompt, "$"+definition.Name) {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_DEFAULT_PROMPT_INVALID",
			"The Codex default prompt must explicitly reference the skill name.",
			map[string]any{"name": definition.Name, "path": path},
		))
	}
	return findings
}

func parseFrontmatter(path string, raw []byte) (frontmatter, []byte, error) {
	if !bytes.HasPrefix(raw, []byte("---\n")) {
		return frontmatter{}, nil, fmt.Errorf("frontmatter must start at byte zero")
	}
	remainder := raw[len("---\n"):]
	closing := bytes.Index(remainder, []byte("\n---\n"))
	if closing < 0 {
		return frontmatter{}, nil, fmt.Errorf("frontmatter closing delimiter is missing")
	}
	var metadata frontmatter
	frontmatterPath := path + ".yaml"
	if err := serialization.DecodeInto(frontmatterPath, remainder[:closing], &metadata); err != nil {
		return frontmatter{}, nil, err
	}
	if metadata.Name == "" || metadata.Description == "" {
		return frontmatter{}, nil, fmt.Errorf("name and description are required")
	}
	return metadata, remainder[closing+len("\n---\n"):], nil
}

func hasHeading(body []byte, heading string) bool {
	for _, line := range strings.Split(string(body), "\n") {
		if line == heading {
			return true
		}
	}
	return false
}

func findUnregisteredSkills(root string, definitions map[string]*Definition) []domain.Finding {
	directory := filepath.Join(root, "skills", "canonical")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return []domain.Finding{finding(
			"GDS_SKILL_CANONICAL_DIRECTORY_INVALID", "Cannot enumerate canonical skills.", directory, err,
		)}
	}
	findings := []domain.Finding{}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "gds-") {
			continue
		}
		if _, registered := definitions[entry.Name()]; !registered {
			findings = append(findings, simpleFinding(
				"GDS_SKILL_UNREGISTERED", "A canonical GDS skill is not registered.",
				map[string]any{"name": entry.Name(), "path": filepath.Join(directory, entry.Name())},
			))
		}
	}
	return findings
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("expected a regular non-symlink file")
	}
	if info.Size() > maxSkillFileBytes {
		return nil, fmt.Errorf("file exceeds %d-byte limit", maxSkillFileBytes)
	}
	return os.ReadFile(path)
}

func safePath(root, relative string) (string, bool) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	target, err := filepath.Abs(filepath.Join(rootAbsolute, filepath.FromSlash(relative)))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbsolute, target)
	return target, err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func simpleFinding(code, message string, evidence map[string]any) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence}
}

func finding(code, message, path string, err error) domain.Finding {
	evidence := map[string]any{"path": path}
	if err != nil {
		evidence["error"] = err.Error()
	}
	return simpleFinding(code, message, evidence)
}

func sortFindings(findings []domain.Finding) {
	sort.SliceStable(findings, func(left, right int) bool {
		if findings[left].Code != findings[right].Code {
			return findings[left].Code < findings[right].Code
		}
		leftEvidence, _ := json.Marshal(findings[left].Evidence)
		rightEvidence, _ := json.Marshal(findings[right].Evidence)
		return bytes.Compare(leftEvidence, rightEvidence) < 0
	})
}
