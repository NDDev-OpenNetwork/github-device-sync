package harness

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const ModuleBridgeContract = "nddev-module-harness-bridge/v2"

type ModuleBridgeDocument struct {
	SchemaVersion     int                    `json:"schema_version"`
	Contract          string                 `json:"contract"`
	Consumer          BridgeConsumer         `json:"consumer"`
	RelationshipScope string                 `json:"relationship_scope"`
	EvidenceOwner     BridgeEvidenceOwner    `json:"evidence_owner"`
	RuntimeTests      BridgeRuntimeTests     `json:"runtime_tests"`
	Mappings          []ModuleHarnessMapping `json:"mappings"`
}

type ModuleHarnessMapping struct {
	ModuleID      string   `json:"module_id"`
	HarnessID     string   `json:"harness_id"`
	Lifecycle     string   `json:"lifecycle"`
	ModuleAliases []string `json:"module_aliases,omitempty"`
}

type BridgeConsumer struct {
	RepositoryID string `json:"repository_id"`
}

type BridgeEvidenceOwner struct {
	Repository string `json:"repository"`
}

type BridgeRuntimeTests struct {
	Required bool `json:"required"`
}

type ModuleBridgeReport struct {
	Contract       string `json:"contract"`
	Path           string `json:"path"`
	Mappings       int    `json:"mappings"`
	IdentityDigest string `json:"identity_digest"`
	InputDigest    string `json:"input_digest"`
}

func LoadModuleBridge(
	root string,
	schemas *validation.Set,
) (ModuleBridgeDocument, ModuleBridgeReport, []domain.Finding) {
	registry, findings := validateRegistry(root, schemas)
	if len(findings) != 0 {
		return ModuleBridgeDocument{}, ModuleBridgeReport{}, findings
	}
	return loadModuleBridge(root, schemas, registry)
}

func loadModuleBridge(
	root string,
	schemas *validation.Set,
	registry RegistryDocument,
) (ModuleBridgeDocument, ModuleBridgeReport, []domain.Finding) {
	relativePath := filepath.ToSlash(filepath.Join("harnesses", "module-bridge.yaml"))
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	report := ModuleBridgeReport{Contract: ModuleBridgeContract, Path: relativePath}
	findings := schemas.ValidateFile("module-harness-bridge", path)
	if len(findings) != 0 {
		return ModuleBridgeDocument{}, report, findings
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ModuleBridgeDocument{}, report, []domain.Finding{bridgeFinding(
			"GDS_MODULE_BRIDGE_READ_FAILED", "Cannot read the canonical module bridge.",
			map[string]any{"path": path, "error": err.Error()},
		)}
	}
	var document ModuleBridgeDocument
	if err := serialization.DecodeInto(path, raw, &document); err != nil {
		return ModuleBridgeDocument{}, report, []domain.Finding{bridgeFinding(
			"GDS_MODULE_BRIDGE_DECODE_FAILED", "Cannot decode the canonical module bridge.",
			map[string]any{"path": path, "error": err.Error()},
		)}
	}
	report.Mappings = len(document.Mappings)
	report.InputDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	report.IdentityDigest, err = moduleBridgeIdentityDigest(document)
	if err != nil {
		return ModuleBridgeDocument{}, report, []domain.Finding{bridgeFinding(
			"GDS_MODULE_BRIDGE_DIGEST_FAILED", "Cannot digest the canonical module bridge.",
			map[string]any{"path": path, "error": err.Error()},
		)}
	}
	findings = append(findings, validateModuleBridgeSemantics(root, document, registry)...)
	sortFindings(findings)
	return document, report, findings
}

func validateModuleBridgeSemantics(
	root string,
	document ModuleBridgeDocument,
	registry RegistryDocument,
) []domain.Finding {
	findings := []domain.Finding{}
	registryByID := map[string]RegistryEntry{}
	for _, entry := range registry.Harnesses {
		registryByID[entry.ID] = entry
	}
	seenModule := map[string]string{}
	seenHarness := map[string]string{}
	moduleAliases := map[string]string{}
	harnessAliases := map[string]string{}
	for _, entry := range registry.Harnesses {
		for _, alias := range entry.LegacyAliases {
			harnessAliases[alias] = entry.ID
		}
	}
	for _, mapping := range document.Mappings {
		findings = append(findings,
			claimBridgeIdentity(seenModule, mapping.ModuleID, mapping.ModuleID, "MODULE_ID")...,
		)
		findings = append(findings,
			claimBridgeIdentity(seenHarness, mapping.HarnessID, mapping.ModuleID, "HARNESS_ID")...,
		)
		_, known := registryByID[mapping.HarnessID]
		if !known {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_HARNESS_UNKNOWN",
				"Module mapping references a harness outside the canonical registry.",
				map[string]any{"module_id": mapping.ModuleID, "harness_id": mapping.HarnessID},
			))
		}
		if mapping.Lifecycle == "renamed" &&
			len(mapping.ModuleAliases) == 0 {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_RENAME_ALIAS_REQUIRED",
				"A renamed module mapping must retain at least one module alias.",
				map[string]any{"module_id": mapping.ModuleID, "harness_id": mapping.HarnessID},
			))
		}
		if mapping.Lifecycle == "active" && len(mapping.ModuleAliases) != 0 {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_ACTIVE_ALIAS_FORBIDDEN",
				"An active module mapping cannot claim historical aliases.",
				map[string]any{"module_id": mapping.ModuleID, "harness_id": mapping.HarnessID},
			))
		}
		for _, alias := range mapping.ModuleAliases {
			findings = append(findings,
				claimBridgeIdentity(moduleAliases, alias, mapping.ModuleID, "MODULE_ALIAS")...,
			)
		}
	}
	for alias, owner := range moduleAliases {
		if canonical, collision := seenModule[alias]; collision {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_ALIAS_COLLISION",
				"Module alias collides with a canonical module ID.",
				map[string]any{"alias": alias, "owner": owner, "canonical_owner": canonical},
			))
		}
		if canonical, collision := seenHarness[alias]; collision {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_ALIAS_COLLISION",
				"Module alias collides with a canonical harness ID.",
				map[string]any{"alias": alias, "owner": owner, "canonical_owner": canonical},
			))
		}
	}
	for alias, owner := range harnessAliases {
		if canonical, collision := seenModule[alias]; collision {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_ALIAS_COLLISION",
				"Harness alias collides with a canonical module ID.",
				map[string]any{"alias": alias, "owner": owner, "canonical_owner": canonical},
			))
		}
		if canonical, collision := moduleAliases[alias]; collision {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_ALIAS_COLLISION",
				"Registry-owned harness alias collides with a module alias.",
				map[string]any{"alias": alias, "owner": owner, "canonical_owner": canonical},
			))
		}
	}
	for id := range registryByID {
		if _, found := seenHarness[id]; !found {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_MAPPING_MISSING",
				"Canonical harness has no NDDev module mapping.",
				map[string]any{"harness_id": id},
			))
		}
	}
	findings = append(findings, validateBridgeDeviceSelections(root, document)...)
	return findings
}

func moduleBridgeIdentityDigest(document ModuleBridgeDocument) (string, error) {
	mappings := append([]ModuleHarnessMapping(nil), document.Mappings...)
	for index := range mappings {
		mappings[index].ModuleAliases = append([]string(nil), mappings[index].ModuleAliases...)
		sort.Strings(mappings[index].ModuleAliases)
	}
	sort.Slice(mappings, func(left, right int) bool {
		return mappings[left].ModuleID < mappings[right].ModuleID
	})
	return canonicaljson.Digest(struct {
		Contract          string                 `json:"contract"`
		Consumer          BridgeConsumer         `json:"consumer"`
		RelationshipScope string                 `json:"relationship_scope"`
		EvidenceOwner     BridgeEvidenceOwner    `json:"evidence_owner"`
		RuntimeTests      BridgeRuntimeTests     `json:"runtime_tests"`
		Mappings          []ModuleHarnessMapping `json:"mappings"`
	}{
		Contract: document.Contract, Consumer: document.Consumer,
		RelationshipScope: document.RelationshipScope, EvidenceOwner: document.EvidenceOwner,
		RuntimeTests: document.RuntimeTests, Mappings: mappings,
	})
}

func validateBridgeDeviceSelections(
	root string,
	document ModuleBridgeDocument,
) []domain.Finding {
	mappings := map[string]ModuleHarnessMapping{}
	for _, mapping := range document.Mappings {
		mappings[mapping.HarnessID] = mapping
	}
	paths, err := filepath.Glob(filepath.Join(root, "estate", "devices", "*.yaml"))
	if err != nil {
		return []domain.Finding{bridgeFinding(
			"GDS_MODULE_BRIDGE_DEVICE_DISCOVERY_FAILED",
			"Cannot enumerate canonical device descriptors.",
			map[string]any{"root": root, "error": err.Error()},
		)}
	}
	sort.Strings(paths)
	findings := []domain.Finding{}
	for _, path := range paths {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_DEVICE_READ_FAILED", "Cannot read a canonical device descriptor.",
				map[string]any{"path": path, "error": readErr.Error()},
			))
			continue
		}
		value, decodeErr := serialization.Decode(path, raw)
		if decodeErr != nil {
			findings = append(findings, bridgeFinding(
				"GDS_MODULE_BRIDGE_DEVICE_DECODE_FAILED", "Cannot decode a canonical device descriptor.",
				map[string]any{"path": path, "error": decodeErr.Error()},
			))
			continue
		}
		object, _ := value.(map[string]any)
		device, _ := object["device"].(map[string]any)
		deviceID, _ := device["id"].(string)
		selected, _ := object["harnesses"].([]any)
		for _, rawHarnessID := range selected {
			harnessID, _ := rawHarnessID.(string)
			mapping, found := mappings[harnessID]
			if !found {
				findings = append(findings, bridgeFinding(
					"GDS_MODULE_BRIDGE_DEVICE_MAPPING_MISSING",
					"Device-selected harness has no module mapping.",
					map[string]any{"path": path, "device_id": deviceID, "harness_id": harnessID},
				))
				continue
			}
			if mapping.Lifecycle == "retired" {
				findings = append(findings, bridgeFinding(
					"GDS_MODULE_BRIDGE_RETIRED_DEVICE_SELECTION",
					"Retired module mapping cannot remain selected by a device.",
					map[string]any{"path": path, "device_id": deviceID, "harness_id": harnessID},
				))
			}
		}
	}
	return findings
}

func claimBridgeIdentity(
	seen map[string]string,
	value string,
	owner string,
	kind string,
) []domain.Finding {
	if prior, duplicate := seen[value]; duplicate {
		return []domain.Finding{bridgeFinding(
			"GDS_MODULE_BRIDGE_"+kind+"_COLLISION",
			"Module bridge identity is owned by more than one mapping.",
			map[string]any{"identity": value, "first": prior, "second": owner},
		)}
	}
	seen[value] = owner
	return nil
}

func bridgeFinding(code, message string, evidence map[string]any) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence,
	}
}
