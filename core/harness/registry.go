package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

var CanonicalIDs = []string{
	"antigravity-cli",
	"claude-code",
	"cline",
	"codex",
	"cursor-cli",
	"github-copilot-cli",
	"grok-build",
	"junie-cli",
	"kilo-cli",
	"kimicode",
	"kiro-cli",
	"mimocode",
	"opencode",
	"pi",
	"qoder-cli",
	"qwen-code",
	"zcode",
}

// WorkPolicyActiveIDs is the global execution/release allowlist. Catalogue
// membership is deliberately separate: the other twelve stable identities
// remain discoverable but are on-pause.
var WorkPolicyActiveIDs = []string{"claude-code", "codex", "grok-build", "opencode", "pi"}

func ValidateDeviceSelection(selected []string) []domain.Finding {
	active := make(map[string]struct{}, len(WorkPolicyActiveIDs))
	for _, id := range WorkPolicyActiveIDs {
		active[id] = struct{}{}
	}
	known := make(map[string]struct{}, len(CanonicalIDs))
	for _, id := range CanonicalIDs {
		known[id] = struct{}{}
	}
	findings := []domain.Finding{}
	seen := map[string]struct{}{}
	for _, id := range selected {
		if _, duplicate := seen[id]; duplicate {
			findings = append(findings, harnessFinding("GDS_DEVICE_HARNESS_DUPLICATE", "Device harness selection contains a duplicate.", map[string]any{"harness": id}))
			continue
		}
		seen[id] = struct{}{}
		if _, ok := known[id]; !ok {
			findings = append(findings, harnessFinding("GDS_HARNESS_SELECTED_UNKNOWN", "Selected harness is not catalogued.", map[string]any{"harness": id}))
			continue
		}
		if _, ok := active[id]; !ok {
			findings = append(findings, harnessFinding("GDS_DEVICE_HARNESS_PAUSED", "Selected harness is catalogued but the global work-policy marks it on-pause; re-observe and create an explicit de-selection or adoption plan.", map[string]any{"harness": id, "state": "installed-paused"}))
		}
	}
	return findings
}

type RegistryDocument struct {
	SchemaVersion int             `json:"schema_version"`
	Harnesses     []RegistryEntry `json:"harnesses"`
}

type RegistryEntry struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	Profile         string   `json:"profile"`
	VerifiedAt      string   `json:"verified_at"`
	RuntimeEvidence string   `json:"runtime_evidence"`
	LegacyAliases   []string `json:"legacy_aliases"`
}

type RegistryReport struct {
	Harnesses       []Report              `json:"harnesses"`
	Aliases         map[string]string     `json:"legacy_aliases"`
	RuntimeContract RuntimeContractReport `json:"runtime_contract"`
}

func Validate(root string, harnessID string, schemas *validation.Set) (Report, []domain.Finding) {
	return validateOne(root, harnessID, schemas, true)
}

func ValidateStatic(root string, harnessID string, schemas *validation.Set) (Report, []domain.Finding) {
	return validateOne(root, harnessID, schemas, false)
}

func validateOne(
	root string,
	harnessID string,
	schemas *validation.Set,
	includeRuntime bool,
) (Report, []domain.Finding) {
	registry, findings := validateRegistry(root, schemas)
	if len(findings) != 0 {
		return Report{Harness: harnessID}, findings
	}
	delegation := resolveDelegation(root, schemas)
	_, contractFindings := ValidateRuntimeContract(root, schemas)
	if len(contractFindings) != 0 {
		return Report{Harness: harnessID}, contractFindings
	}
	if canonical, found := registryAlias(registry, harnessID); found {
		return Report{Harness: harnessID}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_LEGACY_ALIAS_UNSUPPORTED",
			fmt.Sprintf("Harness %q is a migration-only alias; use %q.", harnessID, canonical),
			map[string]any{"alias": harnessID, "canonical_harness": canonical},
		)}
	}
	entry, found := registryEntry(registry, harnessID)
	if !found {
		return Report{Harness: harnessID}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_ID_UNKNOWN", "Harness is not present in the canonical registry.",
			map[string]any{"harness": harnessID, "known": CanonicalIDs},
		)}
	}

	var report Report
	var profile CapabilityProfile
	var profileFindings []domain.Finding
	if harnessID == "codex" {
		if includeRuntime {
			report, profileFindings = ValidateCodex(root, schemas)
		} else {
			report, profileFindings = ValidateCodexStatic(root, schemas)
		}
		profile, _, _ = validateProfile(root, harnessID, schemas, false, delegation)
	} else {
		profile, report, profileFindings = validateProfile(
			root, harnessID, schemas, includeRuntime, delegation,
		)
	}
	profileFindings = append(profileFindings, compareRegistryEntry(entry, profile, report)...)
	sortFindings(profileFindings)
	return report, profileFindings
}

func ValidateAll(root string, schemas *validation.Set) (RegistryReport, []domain.Finding) {
	return validateAll(root, schemas, true, nil)
}

func ValidateStaticAll(root string, schemas *validation.Set) (RegistryReport, []domain.Finding) {
	return validateAll(root, schemas, false, nil)
}

// ValidateSelected runs the full registry and static contracts for every
// canonical harness but requires runtime evidence only for the owner-selected
// set (e.g. the device's `harnesses:` selection). Unselected provisional
// harnesses are still statically validated; they are not runtime-gated, so a
// device that runs only Codex and ZCode is not blocked by unproven catalog
// entries (RVR-P2-009).
func ValidateSelected(
	root string,
	selected []string,
	schemas *validation.Set,
) (RegistryReport, []domain.Finding) {
	set := make(map[string]bool, len(selected))
	for _, id := range selected {
		set[id] = true
	}
	return validateAll(root, schemas, true, set)
}

func validateAll(
	root string,
	schemas *validation.Set,
	includeRuntime bool,
	selected map[string]bool,
) (RegistryReport, []domain.Finding) {
	registry, findings := validateRegistry(root, schemas)
	report := RegistryReport{Harnesses: []Report{}, Aliases: map[string]string{}}
	if len(findings) != 0 {
		return report, findings
	}
	bridge, _, bridgeFindings := loadModuleBridge(root, schemas, registry)
	findings = append(findings, bridgeFindings...)
	if len(bridgeFindings) != 0 {
		return report, findings
	}
	delegation := delegationIndex(bridge)
	contractReport, contractFindings := ValidateRuntimeContract(root, schemas)
	report.RuntimeContract = contractReport
	findings = append(findings, contractFindings...)
	if len(contractFindings) != 0 {
		return report, findings
	}
	if selected != nil {
		known := map[string]struct{}{}
		for _, entry := range registry.Harnesses {
			known[entry.ID] = struct{}{}
		}
		for id := range selected {
			if _, ok := known[id]; !ok {
				findings = append(findings, harnessFinding(
					"GDS_HARNESS_SELECTED_UNKNOWN",
					"A selected harness is not present in the canonical registry.",
					map[string]any{"harness": id, "known": CanonicalIDs},
				))
			}
		}
	}
	for _, entry := range registry.Harnesses {
		for _, alias := range entry.LegacyAliases {
			report.Aliases[alias] = entry.ID
		}
		entryRuntime := includeRuntime && (selected == nil || selected[entry.ID])
		var item Report
		var profile CapabilityProfile
		var itemFindings []domain.Finding
		if entry.ID == "codex" {
			if entryRuntime {
				item, itemFindings = ValidateCodex(root, schemas)
			} else {
				item, itemFindings = ValidateCodexStatic(root, schemas)
			}
			profile, _, _ = validateProfile(root, entry.ID, schemas, false, delegation)
		} else {
			profile, item, itemFindings = validateProfile(
				root, entry.ID, schemas, entryRuntime, delegation,
			)
		}
		item.RuntimeEvidenceOwner = delegation[entry.ID]
		report.Harnesses = append(report.Harnesses, item)
		findings = append(findings, itemFindings...)
		findings = append(findings, compareRegistryEntry(entry, profile, item)...)
	}
	sort.Slice(report.Harnesses, func(left, right int) bool {
		return report.Harnesses[left].Harness < report.Harnesses[right].Harness
	})
	sortFindings(findings)
	return report, findings
}

func validateRegistry(root string, schemas *validation.Set) (RegistryDocument, []domain.Finding) {
	path := filepath.Join(root, "harnesses", "capability-registry.yaml")
	findings := schemas.ValidateFile("harness-registry", path)
	if len(findings) != 0 {
		return RegistryDocument{}, findings
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return RegistryDocument{}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_REGISTRY_READ_FAILED", "Cannot read the harness registry.",
			map[string]any{"path": path, "error": err.Error()},
		)}
	}
	var document RegistryDocument
	if err := serialization.DecodeInto(path, raw, &document); err != nil {
		return RegistryDocument{}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_REGISTRY_DECODE_FAILED", "Cannot decode the harness registry.",
			map[string]any{"path": path, "error": err.Error()},
		)}
	}

	seenIDs := map[string]struct{}{}
	seenAliases := map[string]string{}
	actual := make([]string, 0, len(document.Harnesses))
	for _, entry := range document.Harnesses {
		if _, duplicate := seenIDs[entry.ID]; duplicate {
			findings = append(findings, harnessFinding(
				"GDS_HARNESS_REGISTRY_DUPLICATE_ID", "Harness ID occurs more than once.",
				map[string]any{"harness": entry.ID},
			))
		}
		seenIDs[entry.ID] = struct{}{}
		actual = append(actual, entry.ID)
		expectedPath := filepath.ToSlash(filepath.Join("harnesses", entry.ID, "profile.yaml"))
		if entry.Profile != expectedPath {
			findings = append(findings, harnessFinding(
				"GDS_HARNESS_REGISTRY_PROFILE_PATH_INVALID",
				"Harness profile path must be derived from its canonical ID.",
				map[string]any{"harness": entry.ID, "expected": expectedPath, "observed": entry.Profile},
			))
		}
		for _, alias := range entry.LegacyAliases {
			if owner, duplicate := seenAliases[alias]; duplicate {
				findings = append(findings, harnessFinding(
					"GDS_HARNESS_REGISTRY_DUPLICATE_ALIAS",
					"Legacy harness alias is owned by more than one canonical harness.",
					map[string]any{"alias": alias, "first": owner, "second": entry.ID},
				))
			}
			seenAliases[alias] = entry.ID
		}
	}
	for alias, owner := range seenAliases {
		if _, canonical := seenIDs[alias]; canonical {
			findings = append(findings, harnessFinding(
				"GDS_HARNESS_ALIAS_COLLIDES_WITH_ID",
				"A migration alias cannot also be an active harness identity.",
				map[string]any{"alias": alias, "owner": owner},
			))
		}
	}
	sort.Strings(actual)
	if fmt.Sprint(actual) != fmt.Sprint(CanonicalIDs) {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_REGISTRY_SET_INVALID",
			"Harness registry does not match the owner-selected canonical set.",
			map[string]any{"expected": CanonicalIDs, "observed": actual},
		))
	}
	sortFindings(findings)
	return document, findings
}

// delegationIndex maps each harness the module bridge covers to the repository
// that owns its runtime evidence. A harness in this index is one whose runtime
// suites live in another repository by design, so this control plane must not
// claim to have run them — and must not silently imply it did either.
//
// The index is empty when the bridge does not delegate (`runtime_tests.required`
// true), which is what makes `delegated` unusable as a blanket excuse: it is
// only accepted for a harness the bridge actually names.
func delegationIndex(bridge ModuleBridgeDocument) map[string]string {
	if bridge.RuntimeTests.Required || bridge.EvidenceOwner.Repository == "" {
		return map[string]string{}
	}
	index := make(map[string]string, len(bridge.Mappings))
	for _, mapping := range bridge.Mappings {
		index[mapping.HarnessID] = bridge.EvidenceOwner.Repository
	}
	return index
}

// resolveDelegation is the index for callers that validate a single profile
// outside validateAll and so have not already loaded the bridge. An
// unreadable registry or bridge yields an empty index, which fails closed: a
// profile claiming `delegated` is then reported as undeclared rather than
// accepted on the strength of a document nobody could read.
func resolveDelegation(root string, schemas *validation.Set) map[string]string {
	registry, findings := validateRegistry(root, schemas)
	if len(findings) != 0 {
		return map[string]string{}
	}
	bridge, _, bridgeFindings := loadModuleBridge(root, schemas, registry)
	if len(bridgeFindings) != 0 {
		return map[string]string{}
	}
	return delegationIndex(bridge)
}

func validateProfile(
	root string,
	harnessID string,
	schemas *validation.Set,
	includeRuntime bool,
	delegation map[string]string,
) (CapabilityProfile, Report, []domain.Finding) {
	relativePath := filepath.ToSlash(filepath.Join("harnesses", harnessID, "profile.yaml"))
	profilePath := filepath.Join(root, filepath.FromSlash(relativePath))
	report := Report{Harness: harnessID, ProfilePath: relativePath, Aliases: []string{}}
	findings := schemas.ValidateFile("harness-profile", profilePath)
	if len(findings) != 0 {
		return CapabilityProfile{}, report, findings
	}
	raw, err := os.ReadFile(profilePath)
	if err != nil {
		return CapabilityProfile{}, report, []domain.Finding{harnessFinding(
			"GDS_HARNESS_PROFILE_READ_FAILED", "Cannot read the harness capability profile.",
			map[string]any{"path": profilePath, "error": err.Error()},
		)}
	}
	var document ProfileDocument
	if err := serialization.DecodeInto(profilePath, raw, &document); err != nil {
		return CapabilityProfile{}, report, []domain.Finding{harnessFinding(
			"GDS_HARNESS_PROFILE_DECODE_FAILED", "Cannot decode the harness capability profile.",
			map[string]any{"path": profilePath, "error": err.Error()},
		)}
	}
	profile := document.HarnessProfile
	report.CapabilityStatus = profile.Status
	report.VerifiedAt = profile.VerifiedAt
	report.RuntimeEvidence = profile.RuntimeTests.LastResult
	report.Aliases = append([]string(nil), profile.Aliases...)
	if profile.ID != harnessID || profile.Product != harnessID {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_IDENTITY_INVALID", "Harness profile identity is inconsistent.",
			map[string]any{"path": relativePath, "expected": harnessID, "id": profile.ID, "product": profile.Product},
		))
	}
	for _, source := range profile.OfficialSources {
		if !strings.HasPrefix(source, "https://") {
			findings = append(findings, harnessFinding(
				"GDS_HARNESS_SOURCE_UNSAFE", "Harness source must use HTTPS.",
				map[string]any{"harness": harnessID, "source": source},
			))
		}
	}
	owner, delegated := delegation[harnessID]
	report.RuntimeEvidenceOwner = owner
	// `delegated` is a claim about another repository, so it has to be backed by
	// the module bridge that names it. Without that, it is indistinguishable
	// from a profile marking itself exempt.
	if profile.RuntimeTests.LastResult == "delegated" && !delegated {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_RUNTIME_DELEGATION_UNDECLARED",
			"A harness claims delegated runtime evidence with no module bridge naming an owner.",
			map[string]any{"harness": harnessID, "path": relativePath},
		))
	}
	if profile.Status == "supported" && profile.RuntimeTests.LastResult != "pass" {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_STATUS_UNPROVEN",
			"A harness cannot be supported before its required runtime suite passes.",
			map[string]any{"harness": harnessID, "path": relativePath},
		))
	}
	if profile.Projection.InstructionStrategy == "generated-first-class" && profile.Instructions.NativeAgents {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_PROJECTION_INVALID",
			"A first-class non-AGENTS projection cannot also claim native AGENTS discovery.",
			map[string]any{"harness": harnessID},
		))
	}
	if profile.Skills.Standard == "none" && len(profile.Skills.NativePaths) != 0 {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_SKILL_CONTRACT_INVALID",
			"A harness with no proven skill standard cannot declare native skill paths.",
			map[string]any{"harness": harnessID},
		))
	}
	if includeRuntime && profile.RuntimeTests.Required && profile.RuntimeTests.LastResult != "pass" {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_RUNTIME_NOT_PROVEN",
			"The exact harness version has not passed discovery, invocation, hook, and visibility fixtures.",
			map[string]any{
				"harness":            harnessID,
				"capability_version": profile.CapabilityVersion,
				"last_result":        profile.RuntimeTests.LastResult,
			},
		))
	}
	// A harness that is neither runtime-gated here nor delegated to an owner is
	// simply unproven, and a runtime run that reports success over it would be
	// asserting something nobody established. This is the case the old code
	// could not express: with `required: false` on every profile, the gate above
	// was unreachable and `--runtime` passed over seventeen `not-proven`
	// profiles while the README described it as requiring passing contracts.
	if includeRuntime && !profile.RuntimeTests.Required && !delegated &&
		profile.RuntimeTests.LastResult != "pass" {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_RUNTIME_UNOWNED",
			"Runtime evidence was requested for a harness that neither proves it here nor delegates it to an owner.",
			map[string]any{
				"harness":     harnessID,
				"path":        relativePath,
				"last_result": profile.RuntimeTests.LastResult,
			},
		))
	}
	sortFindings(findings)
	return profile, report, findings
}

func compareRegistryEntry(
	entry RegistryEntry,
	profile CapabilityProfile,
	report Report,
) []domain.Finding {
	if profile.ID == "" {
		return nil
	}
	findings := []domain.Finding{}
	if entry.Status != profile.Status || entry.VerifiedAt != profile.VerifiedAt ||
		entry.RuntimeEvidence != profile.RuntimeTests.LastResult {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_REGISTRY_PROFILE_DRIFT",
			"Harness registry metadata differs from its capability profile.",
			map[string]any{"harness": entry.ID, "profile": report.ProfilePath},
		))
	}
	profileAliases := append([]string(nil), profile.Aliases...)
	entryAliases := append([]string(nil), entry.LegacyAliases...)
	sort.Strings(profileAliases)
	sort.Strings(entryAliases)
	if fmt.Sprint(profileAliases) != fmt.Sprint(entryAliases) {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_REGISTRY_ALIAS_DRIFT",
			"Harness registry aliases differ from its capability profile.",
			map[string]any{"harness": entry.ID, "registry": entryAliases, "profile": profileAliases},
		))
	}
	return findings
}

func registryEntry(document RegistryDocument, id string) (RegistryEntry, bool) {
	for _, entry := range document.Harnesses {
		if entry.ID == id {
			return entry, true
		}
	}
	return RegistryEntry{}, false
}

func registryAlias(document RegistryDocument, alias string) (string, bool) {
	for _, entry := range document.Harnesses {
		for _, candidate := range entry.LegacyAliases {
			if candidate == alias {
				return entry.ID, true
			}
		}
	}
	return "", false
}

func sortFindings(findings []domain.Finding) {
	sort.SliceStable(findings, func(left, right int) bool {
		if findings[left].Code == findings[right].Code {
			return fmt.Sprint(findings[left].Evidence) < fmt.Sprint(findings[right].Evidence)
		}
		return findings[left].Code < findings[right].Code
	})
}
