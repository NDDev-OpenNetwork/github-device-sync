package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestCanonicalModuleBridgeCoversEveryHarnessAndDigestsDeterministically(t *testing.T) {
	root := repoRootForTest(t)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	document, first, findings := LoadModuleBridge(root, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	_, second, findings := LoadModuleBridge(root, schemas)
	if len(findings) != 0 {
		t.Fatalf("second findings = %#v", findings)
	}
	if len(document.Mappings) != 17 || first.IdentityDigest != second.IdentityDigest ||
		first.InputDigest != second.InputDigest {
		t.Fatalf("document = %#v, first = %#v, second = %#v", document, first, second)
	}
	mutated := document
	mutated.Mappings = append([]ModuleHarnessMapping(nil), document.Mappings...)
	mutated.Mappings[0].Lifecycle = "retired"
	changed, err := moduleBridgeIdentityDigest(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first.IdentityDigest {
		t.Fatal("identity digest did not bind lifecycle identity")
	}
}

func TestModuleBridgeIdentityDigestIgnoresOrderingButBindsIdentityDrift(t *testing.T) {
	first := moduleBridgeFixture(
		"nddev-alpha-app", "alpha", "repo_01JEXAMPZ0000000000000000N",
	)
	first.ModuleAliases = []string{"nddev-old-alpha-app", "nddev-legacy-alpha-app"}
	second := moduleBridgeFixture(
		"nddev-beta-app", "beta", "repo_01JEXAMPZ0000000000000000Q",
	)
	document := bridgeDocumentFixture(first, second)
	baseline, err := moduleBridgeIdentityDigest(document)
	if err != nil {
		t.Fatal(err)
	}
	reordered := cloneBridgeDocument(t, document)
	reordered.Mappings[0].ModuleAliases[0], reordered.Mappings[0].ModuleAliases[1] =
		reordered.Mappings[0].ModuleAliases[1], reordered.Mappings[0].ModuleAliases[0]
	reordered.Mappings[0], reordered.Mappings[1] = reordered.Mappings[1], reordered.Mappings[0]
	stable, err := moduleBridgeIdentityDigest(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if stable != baseline {
		t.Fatalf("ordering changed identity digest: baseline=%s reordered=%s", baseline, stable)
	}
	drifted := cloneBridgeDocument(t, document)
	drifted.Mappings[0].HarnessID = "renamed-alpha"
	changed, err := moduleBridgeIdentityDigest(drifted)
	if err != nil {
		t.Fatal(err)
	}
	if changed == baseline {
		t.Fatal("public repository drift did not change identity digest")
	}
}

func TestModuleBridgeRejectsCollisionsAndUnsafeRename(t *testing.T) {
	root := emptyBridgeDeviceRoot(t)
	registry := RegistryDocument{Harnesses: []RegistryEntry{
		{ID: "alpha", LegacyAliases: []string{"legacy-alpha"}},
		{ID: "beta", LegacyAliases: []string{}},
	}}
	base := bridgeDocumentFixture(
		moduleBridgeFixture("nddev-alpha-app", "alpha", "repo_01JEXAMPZ0000000000000000N"),
		moduleBridgeFixture("nddev-beta-app", "beta", "repo_01JEXAMPZ0000000000000000Q"),
	)
	tests := []struct {
		name   string
		mutate func(*ModuleBridgeDocument)
		code   string
	}{
		{
			name: "module collision",
			mutate: func(document *ModuleBridgeDocument) {
				document.Mappings[1].ModuleID = document.Mappings[0].ModuleID
			},
			code: "GDS_MODULE_BRIDGE_MODULE_ID_COLLISION",
		},
		{
			name: "harness collision",
			mutate: func(document *ModuleBridgeDocument) {
				document.Mappings[1].HarnessID = document.Mappings[0].HarnessID
			},
			code: "GDS_MODULE_BRIDGE_HARNESS_ID_COLLISION",
		},
		{
			name: "alias collision",
			mutate: func(document *ModuleBridgeDocument) {
				document.Mappings[1].ModuleAliases = []string{"alpha"}
			},
			code: "GDS_MODULE_BRIDGE_ALIAS_COLLISION",
		},
		{
			name: "harness legacy alias collision",
			mutate: func(document *ModuleBridgeDocument) {
				document.Mappings[1].ModuleAliases = []string{"legacy-alpha"}
			},
			code: "GDS_MODULE_BRIDGE_ALIAS_COLLISION",
		},
		{
			name: "rename without alias",
			mutate: func(document *ModuleBridgeDocument) {
				document.Mappings[0].Lifecycle = "renamed"
			},
			code: "GDS_MODULE_BRIDGE_RENAME_ALIAS_REQUIRED",
		},
		{
			name: "active arbitrary alias",
			mutate: func(document *ModuleBridgeDocument) {
				document.Mappings[0].ModuleAliases = []string{"nddev-invented-alpha-app"}
			},
			code: "GDS_MODULE_BRIDGE_ACTIVE_ALIAS_FORBIDDEN",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := cloneBridgeDocument(t, base)
			test.mutate(&document)
			findings := validateModuleBridgeSemantics(root, document, registry)
			if !containsBridgeFinding(findings, test.code) {
				t.Fatalf("missing %s in %#v", test.code, findings)
			}
		})
	}
}

func TestModuleBridgeRejectsAddedHarnessWithoutMappingAndUnknownHarnessMapping(t *testing.T) {
	root := emptyBridgeDeviceRoot(t)
	registry := RegistryDocument{Harnesses: []RegistryEntry{
		{ID: "alpha"}, {ID: "beta"},
	}}
	document := bridgeDocumentFixture(
		moduleBridgeFixture("nddev-alpha-app", "alpha", "repo_01JEXAMPZ0000000000000000N"),
		moduleBridgeFixture("nddev-gamma-app", "gamma", "repo_01JEXAMPZ0000000000000000Q"),
	)
	findings := validateModuleBridgeSemantics(root, document, registry)
	if !containsBridgeFinding(findings, "GDS_MODULE_BRIDGE_MAPPING_MISSING") {
		t.Fatalf("missing added-harness finding in %#v", findings)
	}
	if !containsBridgeFinding(findings, "GDS_MODULE_BRIDGE_HARNESS_UNKNOWN") {
		t.Fatalf("missing unknown-harness finding in %#v", findings)
	}
}

func TestModuleBridgeAllowsRenameAliasAndRejectsRetiredDeviceSelection(t *testing.T) {
	root := emptyBridgeDeviceRoot(t)
	registry := RegistryDocument{Harnesses: []RegistryEntry{
		{ID: "alpha", LegacyAliases: []string{"old-alpha"}},
	}}
	mapping := moduleBridgeFixture(
		"nddev-alpha-app", "alpha", "repo_01JEXAMPZ0000000000000000N",
	)
	mapping.Lifecycle = "renamed"
	mapping.ModuleAliases = []string{"nddev-old-alpha-app"}
	document := bridgeDocumentFixture(mapping)
	if findings := validateModuleBridgeSemantics(root, document, registry); len(findings) != 0 {
		t.Fatalf("valid rename findings = %#v", findings)
	}
	document.Mappings[0].Lifecycle = "retired"
	device := []byte("device:\n  id: device_01JEXAMPZ00000000000000004\nharnesses:\n  - alpha\n")
	if err := os.WriteFile(
		filepath.Join(root, "estate", "devices", "retired.yaml"), device, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	findings := validateModuleBridgeSemantics(root, document, registry)
	if !containsBridgeFinding(findings, "GDS_MODULE_BRIDGE_RETIRED_DEVICE_SELECTION") {
		t.Fatalf("retired selection findings = %#v", findings)
	}
}

func moduleBridgeFixture(moduleID, harnessID, _ string) ModuleHarnessMapping {
	return ModuleHarnessMapping{
		ModuleID: moduleID, HarnessID: harnessID,
		Lifecycle: "active",
	}
}

func bridgeDocumentFixture(mappings ...ModuleHarnessMapping) ModuleBridgeDocument {
	return ModuleBridgeDocument{
		SchemaVersion: 1, Contract: ModuleBridgeContract,
		Consumer: BridgeConsumer{
			RepositoryID: "repo_01JEXAMPZ0000000000000000M",
		},
		RelationshipScope: "git-submodule-consumer",
		EvidenceOwner: BridgeEvidenceOwner{
			Repository: "example-org/example-harnesses",
		},
		RuntimeTests: BridgeRuntimeTests{Required: false},
		Mappings:     mappings,
	}
}

func cloneBridgeDocument(t *testing.T, document ModuleBridgeDocument) ModuleBridgeDocument {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var clone ModuleBridgeDocument
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func emptyBridgeDeviceRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "estate", "devices"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func containsBridgeFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

// The release gate calls `validate harnesses --harness selected --runtime`. It
// used to exit 0 while all seventeen profiles reported `not-proven`, because
// GDS_HARNESS_RUNTIME_NOT_PROVEN only fires when a profile declares
// `runtime_tests.required: true` and none does — the subject of the check
// decided whether the check applied. Meanwhile the README described that gate as
// requiring passing runtime contracts for all seventeen.
//
// The honest model is delegation, and this pins both halves of it: the report
// names the owner instead of implying a failed attempt, and a profile that
// claims delegation the bridge does not back is rejected.
func TestSelectedRuntimeValidationReportsDelegationRatherThanSilentSuccess(t *testing.T) {
	root := repoRootForTest(t)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	report, findings := ValidateSelected(root, CanonicalIDs, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if len(report.Harnesses) != len(CanonicalIDs) {
		t.Fatalf("harness count = %d, want %d", len(report.Harnesses), len(CanonicalIDs))
	}
	for _, item := range report.Harnesses {
		if item.RuntimeEvidence != "delegated" {
			t.Fatalf("%s runtime evidence = %q, want delegated", item.Harness, item.RuntimeEvidence)
		}
		if item.RuntimeEvidenceOwner != "example-org/example-harnesses" {
			t.Fatalf("%s evidence owner = %q", item.Harness, item.RuntimeEvidenceOwner)
		}
		if item.CapabilityStatus != "provisional" {
			t.Fatalf("%s status = %q; delegation registers a harness as available, not supported",
				item.Harness, item.CapabilityStatus)
		}
	}
}

// A profile may only claim `delegated` while an evidence owner actually covers
// it. Without this, `delegated` would be a self-issued exemption — the same
// defect as the old `required: false`, wearing an honest-looking word.
func TestDelegatedEvidenceIsRejectedWhenNoOwnerCoversTheHarness(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	root := repoRootForTest(t)
	profile, report, findings := validateProfile(root, "pi", schemas, true, map[string]string{})
	if profile.RuntimeTests.LastResult != "delegated" {
		t.Fatalf("fixture profile no longer claims delegation: %q", profile.RuntimeTests.LastResult)
	}
	if report.RuntimeEvidenceOwner != "" {
		t.Fatalf("owner = %q, want empty when nothing delegates", report.RuntimeEvidenceOwner)
	}
	codes := map[string]bool{}
	for _, finding := range findings {
		codes[finding.Code] = true
	}
	for _, want := range []string{
		"GDS_HARNESS_RUNTIME_DELEGATION_UNDECLARED",
		"GDS_HARNESS_RUNTIME_UNOWNED",
	} {
		if !codes[want] {
			t.Fatalf("missing %s; findings = %#v", want, findings)
		}
	}
}
