package validation

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalEstateTreePasses(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	summary, findings := set.ValidateEstateTree(repositoryRoot(t))
	if len(findings) != 0 || summary.Installations != 5 || summary.Mutations != 4 ||
		summary.Owners != 5 ||
		summary.Selectors != 9 || summary.Devices != 3 {
		t.Fatalf("summary=%#v findings=%#v", summary, findings)
	}
}

func TestEstateTreeRejectsUnknownDeviceWorkspaceRoot(t *testing.T) {
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sourceRoot := repositoryRoot(t)
	copyEstateTree(t, filepath.Join(sourceRoot, "estate"), filepath.Join(root, "estate"))
	copyEstateTree(t, filepath.Join(sourceRoot, "policies"), filepath.Join(root, "policies"))
	devicePath := filepath.Join(root, "estate", "devices", "example-user-mac2.yaml")
	raw, err := os.ReadFile(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "workspace_root: \"projects\"", "workspace_root: \"missing\"", 1))
	if err := os.WriteFile(devicePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, findings := set.ValidateEstateTree(root)
	for _, finding := range findings {
		if finding.Code == "GDS_DEVICE_WORKSPACE_ROOT_UNKNOWN" {
			return
		}
	}
	t.Fatalf("findings=%#v", findings)
}

func TestEstateTreeRejectsCanonicalSelectorPortfolioMismatch(t *testing.T) {
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sourceRoot := repositoryRoot(t)
	copyEstateTree(t, filepath.Join(sourceRoot, "estate"), filepath.Join(root, "estate"))
	copyEstateTree(t, filepath.Join(sourceRoot, "policies"), filepath.Join(root, "policies"))
	selectorPath := filepath.Join(root, "estate", "selectors", "personal-forks.yaml")
	raw, err := os.ReadFile(selectorPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(
		string(raw), "portfolio:forks", "portfolio:personal-servers", 1,
	))
	if err := os.WriteFile(selectorPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, findings := set.ValidateEstateTree(root)
	for _, finding := range findings {
		if finding.Code == "GDS_ESTATE_SELECTOR_PORTFOLIO_MISMATCH" {
			return
		}
	}
	t.Fatalf("findings=%#v", findings)
}

func TestEstateTreeRejectsUnknownSelectorOwner(t *testing.T) {
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sourceRoot := repositoryRoot(t)
	copyEstateTree(t, filepath.Join(sourceRoot, "estate"), filepath.Join(root, "estate"))
	copyEstateTree(t, filepath.Join(sourceRoot, "policies"), filepath.Join(root, "policies"))
	selectorPath := filepath.Join(root, "estate", "selectors", "personal-forks.yaml")
	raw, err := os.ReadFile(selectorPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "owner:example-user", "owner:missing", 1))
	if err := os.WriteFile(selectorPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, findings := set.ValidateEstateTree(root)
	found := false
	for _, finding := range findings {
		if finding.Code == "GDS_ESTATE_SELECTOR_OWNER_MISSING" {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings=%#v", findings)
	}
}

func copyEstateTree(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, raw, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A device inventory that records a consumer checkout and only some of the
// submodules declared beneath it is describing something it did not observe.
// `modules/github-actions` was in exactly that position -- declared, physically
// present, absent from all three device inventories -- while estate validation
// reported success.
func TestDeviceInventoryMustRecordEveryDeclaredSubmodule(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor := `schema_version: 1
provider:
  owner: "example-org"
  name: "github-device-sync"
relationships:
  - type: "git-submodule-consumer"
    target: "repo_A"
    gitmodules_name: "modules/alpha"
  - type: "git-submodule-consumer"
    target: "repo_B"
    gitmodules_name: "modules/beta"
  - type: "workflow-module-consumer"
    target: "repo_A"
`
	if err := os.WriteFile(
		filepath.Join(root, ".gds", "repository.yaml"), []byte(anchor), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	device := func(entries ...map[string]any) estateDocument {
		repositories := make([]any, 0, len(entries)+1)
		repositories = append(repositories, map[string]any{
			"provider": "example-org/github-device-sync",
			"path":     "control-plane/github-device-sync", "materialization": "checkout",
		})
		for _, entry := range entries {
			repositories = append(repositories, entry)
		}
		return estateDocument{path: "device.yaml", value: map[string]any{
			"device":       map[string]any{"name": "fixture-device"},
			"repositories": repositories,
		}}
	}
	submodule := func(name string, materialization string) map[string]any {
		return map[string]any{
			"provider":        "example-org/" + name,
			"path":            "control-plane/github-device-sync/modules/" + name,
			"materialization": materialization,
		}
	}

	t.Run("complete inventory passes", func(t *testing.T) {
		findings := set.validateDeviceSubmoduleInventory(root, []estateDocument{
			device(submodule("alpha", "git-submodule"), submodule("beta", "git-submodule")),
		})
		if len(findings) != 0 {
			t.Fatalf("findings = %#v", findings)
		}
	})

	t.Run("one omitted submodule fails", func(t *testing.T) {
		findings := set.validateDeviceSubmoduleInventory(root, []estateDocument{
			device(submodule("alpha", "git-submodule")),
		})
		if len(findings) != 1 || findings[0].Code != "GDS_DEVICE_INVENTORY_SUBMODULE_MISSING" {
			t.Fatalf("findings = %#v", findings)
		}
		if findings[0].Evidence["gitmodules_name"] != "modules/beta" {
			t.Fatalf("evidence = %#v", findings[0].Evidence)
		}
	})

	t.Run("a submodule recorded as a checkout fails", func(t *testing.T) {
		findings := set.validateDeviceSubmoduleInventory(root, []estateDocument{
			device(submodule("alpha", "git-submodule"), submodule("beta", "checkout")),
		})
		if len(findings) != 1 ||
			findings[0].Code != "GDS_DEVICE_INVENTORY_SUBMODULE_MATERIALIZATION_INVALID" {
			t.Fatalf("findings = %#v", findings)
		}
	})

	// A device with no inventory has nothing to be inconsistent with, and
	// whether the absence means "not observed" or "nothing materialized" is a
	// product decision rather than something to infer. Excluded on purpose.
	t.Run("a device without an inventory is out of scope", func(t *testing.T) {
		findings := set.validateDeviceSubmoduleInventory(root, []estateDocument{{
			path:  "device.yaml",
			value: map[string]any{"device": map[string]any{"name": "unobserved"}},
		}})
		if len(findings) != 0 {
			t.Fatalf("findings = %#v", findings)
		}
	})

	// A device that does not materialize the consumer cannot be missing its
	// submodules, so the check must not fire on every device in the estate.
	t.Run("a device without the consumer is out of scope", func(t *testing.T) {
		findings := set.validateDeviceSubmoduleInventory(root, []estateDocument{{
			path: "device.yaml",
			value: map[string]any{
				"device": map[string]any{"name": "elsewhere"},
				"repositories": []any{map[string]any{
					"provider": "example-org/unrelated",
					"path":     "nddev/unrelated", "materialization": "checkout",
				}},
			},
		}})
		if len(findings) != 0 {
			t.Fatalf("findings = %#v", findings)
		}
	})
}

// The device schema makes `repositories` optional so that "never observed" and
// "observed, holds nothing" stay distinct. Counting the first is what keeps the
// distinction visible to a reader of the summary rather than only to someone
// who opens each descriptor.
func TestEstateSummaryCountsUnobservedDevices(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	summary, findings := set.ValidateEstateTree(repositoryRoot(t))
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if summary.Devices == 0 {
		t.Fatal("no devices were validated")
	}
	if summary.DevicesWithoutInventory < 0 || summary.DevicesWithoutInventory > summary.Devices {
		t.Fatalf("unobserved %d is not within devices %d",
			summary.DevicesWithoutInventory, summary.Devices)
	}
	// Derived from the tree so onboarding or inventorying a device does not
	// edit this test.
	expected := 0
	entries, err := os.ReadDir(filepath.Join(repositoryRoot(t), "estate", "devices"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "estate", "devices", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "\nrepositories:") {
			expected++
		}
	}
	if summary.DevicesWithoutInventory != expected {
		t.Fatalf("unobserved = %d, tree says %d", summary.DevicesWithoutInventory, expected)
	}
}

// A policy's own `match` block decides which repositories it governs, and its
// references were never resolved. `policies/owners/opennetwork-default.yaml`
// had to be written with `owner:nddev-opennetwork` -- the GitHub login
// lowercased -- because the compiler synthesised the id that way, so the estate
// carried a reference to an owner nothing declares and every validator passed.
func TestEstateTreeRejectsPolicyReferencesThatResolveToNothing(t *testing.T) {
	for _, testCase := range []struct {
		name string
		old  string
		new  string
		code string
	}{
		{
			name: "owner",
			old:  `  owner: "owner:acme"`,
			new:  `  owner: "owner:nothing-declares-this"`,
			code: "GDS_ESTATE_POLICY_OWNER_MISSING",
		},
		{
			name: "portfolio",
			old:  `    - "portfolio:forks"`,
			new:  `    - "portfolio:no-selector-assigns-this"`,
			code: "GDS_ESTATE_POLICY_PORTFOLIO_MISSING",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			set, err := NewSchemaSet()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			sourceRoot := repositoryRoot(t)
			copyEstateTree(t, filepath.Join(sourceRoot, "estate"), filepath.Join(root, "estate"))
			copyEstateTree(t, filepath.Join(sourceRoot, "policies"), filepath.Join(root, "policies"))

			target := ""
			walkErr := filepath.WalkDir(filepath.Join(root, "policies"),
				func(path string, entry fs.DirEntry, err error) error {
					if err != nil || entry.IsDir() || target != "" {
						return err
					}
					raw, readErr := os.ReadFile(path)
					if readErr != nil {
						return readErr
					}
					if strings.Contains(string(raw), testCase.old) {
						target = path
					}
					return nil
				})
			if walkErr != nil || target == "" {
				t.Fatalf("no policy carries %q: %v", testCase.old, walkErr)
			}
			raw, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			mutated := strings.Replace(string(raw), testCase.old, testCase.new, 1)
			if mutated == string(raw) {
				t.Fatalf("fixture mutation did not apply to %s", target)
			}
			if err := os.WriteFile(target, []byte(mutated), 0o600); err != nil {
				t.Fatal(err)
			}

			_, findings := set.ValidateEstateTree(root)
			for _, finding := range findings {
				if finding.Code == testCase.code {
					return
				}
			}
			t.Fatalf("findings=%#v", findings)
		})
	}
}
