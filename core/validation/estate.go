package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

type EstateSummary struct {
	Installations int `json:"installations"`
	Mutations     int `json:"mutations"`
	Owners        int `json:"owners"`
	Selectors     int `json:"selectors"`
	Devices       int `json:"devices"`
	// Devices carrying no `repositories` block at all. The device schema makes
	// that block optional deliberately -- "a device that has not been observed
	// must not be described as empty" -- so its absence is a real state rather
	// than a defect, and reporting it as a finding would contradict the
	// contract it comes from. Counting it is the other half of that decision:
	// a summary that says "devices: 3" while one of them has never been
	// inventoried is not wrong, but a reader cannot tell, and a report that
	// omits without saying so is how a report starts lying.
	DevicesWithoutInventory int `json:"devices_without_inventory"`
	Policies                int `json:"policies"`
	RuntimeDeps             int `json:"runtime_dependencies"`
}

type estateDocument struct {
	path  string
	value map[string]any
}

func (set *Set) ValidateEstateTree(root string) (EstateSummary, []domain.Finding) {
	estateRoot := filepath.Join(root, "estate")
	estatePath := filepath.Join(estateRoot, "estate.yaml")
	findings := set.ValidateFile("estate", estatePath)
	estateValue := loadValidatedObject(estatePath, findings)

	installations, installationFindings := set.validateEstateDirectory(
		filepath.Join(estateRoot, "installations"), "installation",
	)
	mutations, mutationFindings := set.validateEstateDirectory(
		filepath.Join(estateRoot, "mutations"), "mutation-capability",
	)
	owners, ownerFindings := set.validateEstateDirectory(
		filepath.Join(estateRoot, "owners"), "owner",
	)
	selectors, selectorFindings := set.validateEstateDirectory(
		filepath.Join(estateRoot, "selectors"), "selector",
	)
	devices, deviceFindings := set.validateEstateDirectory(
		filepath.Join(estateRoot, "devices"), "device",
	)
	policies, policyFindings := set.validatePolicyTree(filepath.Join(root, "policies"))
	runtimeDeps, runtimeDepFindings := set.validateRuntimeDependencies(root)
	findings = append(findings, installationFindings...)
	findings = append(findings, mutationFindings...)
	findings = append(findings, ownerFindings...)
	findings = append(findings, selectorFindings...)
	findings = append(findings, deviceFindings...)
	findings = append(findings, policyFindings...)
	findings = append(findings, runtimeDepFindings...)
	findings = append(findings, set.validateDeviceSubmoduleInventory(root, devices)...)
	unobserved := 0
	for _, device := range devices {
		if _, present := device.value["repositories"]; !present {
			unobserved++
		}
	}
	summary := EstateSummary{
		Installations: len(installations), Mutations: len(mutations),
		Owners: len(owners), Selectors: len(selectors),
		Devices: len(devices), DevicesWithoutInventory: unobserved,
		Policies: len(policies), RuntimeDeps: runtimeDeps,
	}
	if estateValue == nil ||
		len(installationFindings)+len(mutationFindings)+len(ownerFindings)+len(selectorFindings)+
			len(deviceFindings)+len(policyFindings) != 0 {
		return summary, findings
	}
	policyByID := map[string]estateDocument{}
	for _, document := range policies {
		identity := nestedObject(document.value, "policy")
		id := stringField(identity, "id")
		if previous, duplicate := policyByID[id]; duplicate {
			findings = append(findings, duplicateEstateFinding(
				"GDS_ESTATE_POLICY_DUPLICATE", "policy", id, previous.path, document.path,
			))
		}
		policyByID[id] = document
	}

	installationByID := map[string]estateDocument{}
	installationLogin := map[string]string{}
	for _, document := range installations {
		identity := nestedObject(document.value, "installation")
		id := stringField(identity, "id")
		if previous, duplicate := installationByID[id]; duplicate {
			findings = append(findings, duplicateEstateFinding(
				"GDS_ESTATE_INSTALLATION_DUPLICATE", "installation", id,
				previous.path, document.path,
			))
		}
		installationByID[id] = document
		installationLogin[id] = stringField(identity, "account_login")
	}
	referencedInstallations := map[string]struct{}{}
	for _, raw := range arrayField(estateValue, "installations") {
		id, _ := raw.(string)
		referencedInstallations[id] = struct{}{}
		if _, found := installationByID[id]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_INSTALLATION_MISSING", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Estate references missing installation %q.", id),
				Evidence: map[string]any{"path": estatePath, "installation": id},
			})
		}
	}
	for id, document := range installationByID {
		if _, found := referencedInstallations[id]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_INSTALLATION_UNREFERENCED", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Installation %q is not declared by estate.yaml.", id),
				Evidence: map[string]any{"path": document.path, "installation": id},
			})
		}
	}

	mutationByID := map[string]estateDocument{}
	for _, document := range mutations {
		identity := nestedObject(document.value, "mutation")
		id := stringField(identity, "id")
		if previous, duplicate := mutationByID[id]; duplicate {
			findings = append(findings, duplicateEstateFinding(
				"GDS_ESTATE_MUTATION_DUPLICATE", "mutation capability", id,
				previous.path, document.path,
			))
		}
		mutationByID[id] = document
		installationID := stringField(identity, "installation")
		if _, found := installationByID[installationID]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_MUTATION_INSTALLATION_MISSING", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf(
					"Mutation capability %q references missing installation %q.", id, installationID,
				),
				Evidence: map[string]any{"path": document.path, "mutation": id},
			})
		}
	}
	referencedMutations := map[string]struct{}{}
	for _, raw := range arrayField(estateValue, "mutation_capabilities") {
		id, _ := raw.(string)
		referencedMutations[id] = struct{}{}
		if _, found := mutationByID[id]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_MUTATION_MISSING", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Estate references missing mutation capability %q.", id),
				Evidence: map[string]any{"path": estatePath, "mutation": id},
			})
		}
	}
	for id, document := range mutationByID {
		if _, found := referencedMutations[id]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_MUTATION_UNREFERENCED", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Mutation capability %q is not declared by estate.yaml.", id),
				Evidence: map[string]any{"path": document.path, "mutation": id},
			})
		}
	}

	ownerByID := map[string]estateDocument{}
	ownerPortfolio := map[string]map[bool]string{}
	for _, document := range owners {
		identity := nestedObject(document.value, "owner")
		id := stringField(identity, "id")
		if previous, duplicate := ownerByID[id]; duplicate {
			findings = append(findings, duplicateEstateFinding(
				"GDS_ESTATE_OWNER_DUPLICATE", "owner", id, previous.path, document.path,
			))
		}
		ownerByID[id] = document
		defaultProfile := stringField(nestedObject(document.value, "defaults"), "policy_profile")
		if _, found := policyByID[defaultProfile]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_OWNER_POLICY_MISSING", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Owner %q references missing policy profile %q.", id, defaultProfile),
				Evidence: map[string]any{"path": document.path, "policy": defaultProfile},
			})
		}
		installationID := stringField(identity, "installation")
		if _, found := installationByID[installationID]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_OWNER_INSTALLATION_MISSING", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Owner %q references missing installation %q.", id, installationID),
				Evidence: map[string]any{"path": document.path, "owner": id},
			})
		}
		if expectedLogin := installationLogin[installationID]; expectedLogin != "" &&
			!strings.EqualFold(expectedLogin, stringField(identity, "provider_login")) {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_OWNER_LOGIN_MISMATCH", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Owner %q login does not match its installation account.", id),
				Evidence: map[string]any{"path": document.path, "installation": installationID},
			})
		}
		classification := nestedObject(document.value, "classification")
		ownerPortfolio[id] = map[bool]string{
			false: stringField(classification, "source_portfolio"),
			true:  stringField(classification, "fork_portfolio"),
		}
	}

	selectorIDs := map[string]estateDocument{}
	for _, document := range selectors {
		identity := nestedObject(document.value, "selector")
		id := stringField(identity, "id")
		if previous, duplicate := selectorIDs[id]; duplicate {
			findings = append(findings, duplicateEstateFinding(
				"GDS_ESTATE_SELECTOR_DUPLICATE", "selector", id, previous.path, document.path,
			))
		}
		selectorIDs[id] = document
		match := nestedObject(document.value, "match")
		ownerID := stringField(match, "owner")
		if _, found := ownerByID[ownerID]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_SELECTOR_OWNER_MISSING", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Selector %q references missing owner %q.", id, ownerID),
				Evidence: map[string]any{"path": document.path, "selector": id},
			})
			continue
		}
		fork, hasFork := match["fork"].(bool)
		for _, rawProfile := range arrayField(nestedObject(document.value, "assign"), "policy_profiles") {
			profile, _ := rawProfile.(string)
			if _, found := policyByID[profile]; !found {
				findings = append(findings, domain.Finding{
					Code: "GDS_ESTATE_SELECTOR_POLICY_MISSING", Severity: domain.SeverityHigh,
					Message:  fmt.Sprintf("Selector %q references missing policy profile %q.", id, profile),
					Evidence: map[string]any{"path": document.path, "policy": profile},
				})
			}
		}
		if !hasFork || selectorHasSpecializedMatch(match) {
			continue
		}
		expectedPortfolio := ownerPortfolio[ownerID][fork]
		assigned := arrayField(nestedObject(document.value, "assign"), "portfolios")
		if !stringArrayContains(assigned, expectedPortfolio) {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_SELECTOR_PORTFOLIO_MISMATCH", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf("Selector %q does not assign the owner's canonical portfolio.", id),
				Evidence: map[string]any{
					"path": document.path, "selector": id,
					"expected_portfolio": expectedPortfolio,
				},
			})
		}
	}

	// A policy's own `match` block decides which repositories it governs, and its
	// references were never resolved. `GDS_ESTATE_SELECTOR_OWNER_MISSING` has
	// always held selectors to declared owners; `ownerByID` was in scope here
	// and the check simply was not extended to policies.
	//
	// The failure mode is silence rather than error. A profile whose match can
	// never be satisfied is skipped, so the repositories that selected it fall
	// back to the tier below and the next rule added to that profile does not
	// apply either.
	//
	// Portfolios are checked against what selectors assign, because that is
	// where a portfolio comes into existence. Nothing else declares one, so
	// deriving the set is the only way to resolve the reference without
	// inventing a second register of it.
	assignedPortfolios := map[string]struct{}{}
	for _, document := range selectors {
		for _, raw := range arrayField(nestedObject(document.value, "assign"), "portfolios") {
			if portfolio, ok := raw.(string); ok {
				assignedPortfolios[portfolio] = struct{}{}
			}
		}
	}
	for _, document := range policies {
		id := stringField(nestedObject(document.value, "policy"), "id")
		match := nestedObject(document.value, "match")
		if owner := stringField(match, "owner"); owner != "" {
			if _, found := ownerByID[owner]; !found {
				findings = append(findings, domain.Finding{
					Code: "GDS_ESTATE_POLICY_OWNER_MISSING", Severity: domain.SeverityHigh,
					Message:  fmt.Sprintf("Policy %q matches missing owner %q.", id, owner),
					Evidence: map[string]any{"path": document.path, "policy": id, "owner": owner},
				})
			}
		}
		for _, raw := range arrayField(match, "portfolios") {
			portfolio, ok := raw.(string)
			if !ok {
				continue
			}
			if _, found := assignedPortfolios[portfolio]; !found {
				findings = append(findings, domain.Finding{
					Code: "GDS_ESTATE_POLICY_PORTFOLIO_MISSING", Severity: domain.SeverityHigh,
					Message: fmt.Sprintf(
						"Policy %q matches portfolio %q that no selector assigns.", id, portfolio,
					),
					Evidence: map[string]any{
						"path": document.path, "policy": id, "portfolio": portfolio,
					},
				})
			}
		}
	}

	deviceIDs := map[string]estateDocument{}
	deviceNames := map[string]estateDocument{}
	for _, document := range devices {
		identity := nestedObject(document.value, "device")
		id := stringField(identity, "id")
		name := stringField(identity, "name")
		if previous, duplicate := deviceIDs[id]; duplicate {
			findings = append(findings, duplicateEstateFinding(
				"GDS_ESTATE_DEVICE_DUPLICATE", "device", id, previous.path, document.path,
			))
		}
		deviceIDs[id] = document
		if previous, duplicate := deviceNames[name]; duplicate {
			findings = append(findings, duplicateEstateFinding(
				"GDS_ESTATE_DEVICE_NAME_DUPLICATE", "device name", name,
				previous.path, document.path,
			))
		}
		deviceNames[name] = document

		workspaceRoots := nestedObject(document.value, "workspace_roots")
		assignments := arrayField(nestedObject(document.value, "materialization"), "include")
		seenSelectors := map[string]struct{}{}
		for index, rawAssignment := range assignments {
			assignment, _ := rawAssignment.(map[string]any)
			selector := stringField(assignment, "selector")
			workspaceRoot := stringField(assignment, "workspace_root")
			if _, duplicate := seenSelectors[selector]; duplicate {
				findings = append(findings, domain.Finding{
					Code: "GDS_ESTATE_DEVICE_SELECTOR_DUPLICATE", Severity: domain.SeverityHigh,
					Message: fmt.Sprintf(
						"Device %q assigns selector %q more than once.", id, selector,
					),
					Evidence: map[string]any{
						"path": document.path, "device": id, "selector": selector, "index": index,
					},
				})
			}
			seenSelectors[selector] = struct{}{}
			if _, found := workspaceRoots[workspaceRoot]; !found {
				findings = append(findings, domain.Finding{
					Code: "GDS_ESTATE_DEVICE_WORKSPACE_ROOT_MISSING", Severity: domain.SeverityHigh,
					Message: fmt.Sprintf(
						"Device %q references missing workspace root %q.", id, workspaceRoot,
					),
					Evidence: map[string]any{
						"path": document.path, "device": id, "workspace_root": workspaceRoot,
					},
				})
			}
		}
	}
	sortFindingsByCodeAndEvidence(findings)
	return summary, findings
}

func selectorHasSpecializedMatch(match map[string]any) bool {
	if len(arrayField(match, "name_prefixes")) != 0 || len(arrayField(match, "visibility")) != 0 {
		return true
	}
	_, constrainsArchived := match["archived"]
	return constrainsArchived
}

func (set *Set) validatePolicyTree(root string) ([]estateDocument, []domain.Finding) {
	documents := []estateDocument{}
	findings := []domain.Finding{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_SOURCE_SYMLINK", Severity: domain.SeverityHigh,
				Message:  "Policy source documents must be regular non-symlink files.",
				Evidence: map[string]any{"path": path},
			})
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") &&
			!strings.HasSuffix(entry.Name(), ".yml")) {
			return nil
		}
		fileFindings := set.ValidateFile("policy", path)
		findings = append(findings, fileFindings...)
		if value := loadValidatedObject(path, fileFindings); value != nil {
			documents = append(documents, estateDocument{path: path, value: value})
		}
		return nil
	})
	if err != nil {
		findings = append(findings, domain.Finding{
			Code: "GDS_ESTATE_POLICY_TREE_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Cannot read policy tree: %v", err),
			Evidence: map[string]any{"path": root},
		})
	}
	sort.Slice(documents, func(left, right int) bool { return documents[left].path < documents[right].path })
	return documents, findings
}

func (set *Set) validateEstateDirectory(
	directory string,
	schemaName string,
) ([]estateDocument, []domain.Finding) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, []domain.Finding{{
			Code: "GDS_ESTATE_DIRECTORY_MISSING", Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Cannot read estate %s directory: %v", schemaName, err),
			Evidence: map[string]any{"path": directory},
		}}
	}
	documents := []estateDocument{}
	findings := []domain.Finding{}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") &&
			!strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_SOURCE_SYMLINK", Severity: domain.SeverityHigh,
				Message:  "Estate source documents must be regular non-symlink files.",
				Evidence: map[string]any{"path": path},
			})
			continue
		}
		fileFindings := set.ValidateFile(schemaName, path)
		findings = append(findings, fileFindings...)
		if value := loadValidatedObject(path, fileFindings); value != nil {
			documents = append(documents, estateDocument{path: path, value: value})
		}
	}
	sort.Slice(documents, func(left, right int) bool { return documents[left].path < documents[right].path })
	return documents, findings
}

func loadValidatedObject(path string, findings []domain.Finding) map[string]any {
	if len(findings) != 0 {
		return nil
	}
	value, err := serialization.DecodeFile(path)
	if err != nil {
		return nil
	}
	object, _ := value.(map[string]any)
	return object
}

func nestedObject(object map[string]any, key string) map[string]any {
	nested, _ := object[key].(map[string]any)
	return nested
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func arrayField(object map[string]any, key string) []any {
	value, _ := object[key].([]any)
	return value
}

func stringArrayContains(values []any, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func duplicateEstateFinding(code, kind, id, first, second string) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh,
		Message:  fmt.Sprintf("Duplicate %s identity %q.", kind, id),
		Evidence: map[string]any{"id": id, "first": first, "second": second},
	}
}

// validateDeviceSubmoduleInventory rejects a device inventory that records a
// consumer checkout and only some of the submodules declared under it.
//
// A device inventory is observed evidence, and an entry that disagrees with the
// filesystem is already a finding. An entry that is simply absent was not,
// which is how `modules/github-actions` came to be a declared
// `git-submodule-consumer`, physically present, and missing from all three
// device inventories while `gds validate estate` reported success.
//
// The check is structural on purpose. It compares the recorded consumer path
// against the declared `gitmodules_name` values and needs neither the module
// anchors nor a checkout, so it holds in a CI job that clones the superproject
// alone. What it asserts is internal consistency: an inventory that says the
// consumer is checked out at a path, and records some of that path's declared
// submodules, cannot omit the rest and still be describing what it observed.
//
// A device with no `repositories` block at all is deliberately out of scope.
// There is nothing for its inventory to be inconsistent with, and whether the
// absence means "not observed" or "nothing materialized" is a product decision
// rather than something to infer here. Tracked in
// example-org/github-device-sync#183.
func (set *Set) validateDeviceSubmoduleInventory(
	root string,
	devices []estateDocument,
) []domain.Finding {
	anchor, err := serialization.DecodeFile(filepath.Join(root, ".gds", "repository.yaml"))
	if err != nil {
		return nil
	}
	object, ok := anchor.(map[string]any)
	if !ok {
		return nil
	}
	provider := nestedObject(object, "provider")
	consumer := stringField(provider, "owner") + "/" + stringField(provider, "name")
	declared := []string{}
	for _, raw := range arrayField(object, "relationships") {
		relationship, ok := raw.(map[string]any)
		if !ok || stringField(relationship, "type") != "git-submodule-consumer" {
			continue
		}
		if name := stringField(relationship, "gitmodules_name"); name != "" {
			declared = append(declared, name)
		}
	}
	if consumer == "/" || len(declared) == 0 {
		return nil
	}
	sort.Strings(declared)

	findings := []domain.Finding{}
	for _, device := range devices {
		entries := arrayField(device.value, "repositories")
		if len(entries) == 0 {
			continue
		}
		recorded := map[string]string{}
		consumerPaths := []string{}
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			path := stringField(entry, "path")
			recorded[path] = stringField(entry, "materialization")
			if stringField(entry, "provider") == consumer &&
				stringField(entry, "materialization") == "checkout" {
				consumerPaths = append(consumerPaths, path)
			}
		}
		sort.Strings(consumerPaths)
		deviceID := stringField(nestedObject(device.value, "device"), "name")
		for _, consumerPath := range consumerPaths {
			for _, name := range declared {
				expected := consumerPath + "/" + name
				materialization, present := recorded[expected]
				switch {
				case !present:
					findings = append(findings, domain.Finding{
						Code:     "GDS_DEVICE_INVENTORY_SUBMODULE_MISSING",
						Severity: domain.SeverityHigh,
						Message: "Device inventory records the consumer checkout but omits a " +
							"declared submodule beneath it.",
						Evidence: map[string]any{
							"device": deviceID, "consumer": consumer,
							"gitmodules_name": name, "expected_path": expected,
							"source": device.path,
						},
					})
				case materialization != "git-submodule":
					findings = append(findings, domain.Finding{
						Code:     "GDS_DEVICE_INVENTORY_SUBMODULE_MATERIALIZATION_INVALID",
						Severity: domain.SeverityHigh,
						Message: "A declared submodule is recorded with a materialization other " +
							"than git-submodule.",
						Evidence: map[string]any{
							"device": deviceID, "gitmodules_name": name,
							"path": expected, "materialization": materialization,
							"source": device.path,
						},
					})
				}
			}
		}
	}
	return findings
}
