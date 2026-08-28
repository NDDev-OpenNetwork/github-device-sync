package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type ModuleBridgeParityOptions struct {
	GDSRoot   string
	NDDevRoot string
}

const (
	publicModuleValidatorCommand = "python3 cli-tools/validate_public_contracts.py"
	publicModuleValidatorPath    = "cli-tools/validate_public_contracts.py"
)

type ModuleBridgeParityData struct {
	Contract       string                    `json:"contract"`
	Mappings       int                       `json:"mappings"`
	MappingEdges   []ModuleBridgeMappingEdge `json:"mapping_edges"`
	IdentityDigest string                    `json:"identity_digest"`
	InputDigests   map[string]string         `json:"input_digests"`
	ParityDigest   string                    `json:"parity_digest"`
}

type ModuleBridgeMappingEdge struct {
	ModuleID                  string `json:"module_id"`
	HarnessID                 string `json:"harness_id"`
	PublicRepository          string `json:"public_repository"`
	GDSRepositoryID           string `json:"gds_repository_id"`
	ExpectedHead              string `json:"expected_head"`
	GitlinkSHA                string `json:"gitlink_sha"`
	RegistryContractVersion   int    `json:"registry_contract_version"`
	RegistryExpectationDigest string `json:"registry_expectation_digest"`
	PublicContractVersion     int    `json:"public_contract_version"`
	PublicContractDigest      string `json:"public_contract_digest"`
	GDSCapabilityVersion      string `json:"gds_capability_version"`
	GDSProfileDigest          string `json:"gds_profile_digest"`
}

type nddevRegistryDocument struct {
	Modules []nddevModule `json:"modules"`
}

type nddevModule struct {
	ID                 string `json:"id"`
	Repository         string `json:"repository"`
	Path               string `json:"path"`
	ExpectedHead       string `json:"expected_head"`
	ValidationManifest string `json:"validation_manifest"`
	Expectations       struct {
		Contract map[string]any `json:"contract"`
	} `json:"expectations"`
}

type gdsProfileEdge struct {
	Object            map[string]any
	CapabilityVersion any
	Digest            string
}

type nddevAnchorDocument struct {
	Repository struct {
		ID string `json:"id"`
	} `json:"repository"`
	Relationships []struct {
		Type           string `json:"type"`
		Target         string `json:"target"`
		GitmodulesName string `json:"gitmodules_name"`
	} `json:"relationships"`
}

func (services *Services) ValidateModuleBridge(
	ctx context.Context,
	gdsRoot string,
) domain.Envelope {
	const command = "gds harness bridge validate"
	root, finding := exactCheckoutRoot(ctx, gdsRoot, "gds")
	if finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	_, report, findings := harness.LoadModuleBridge(root, services.Schemas)
	return domain.NewEnvelope(command, classifyFindings(findings), report, findings...)
}

func (services *Services) ValidateModuleBridgeParity(
	ctx context.Context,
	options ModuleBridgeParityOptions,
) domain.Envelope {
	const command = "gds harness bridge parity"
	gdsRoot, finding := exactCheckoutRoot(ctx, options.GDSRoot, "gds")
	if finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	nddevRoot, finding := exactCheckoutRoot(ctx, options.NDDevRoot, "nddev")
	if finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	document, report, findings := harness.LoadModuleBridge(gdsRoot, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), report, findings...)
	}
	registryPath := filepath.Join(nddevRoot, "config", "repositories.json")
	anchorPath := filepath.Join(nddevRoot, ".gds", "repository.yaml")
	registryRaw, registry, registryFindings := loadNDDevRegistry(registryPath)
	anchorRaw, anchor, anchorFindings := loadNDDevAnchor(anchorPath)
	gitmodulesRaw, gitmodulePaths, gitmodulesFindings := readGitmodulePaths(ctx, nddevRoot)
	indexRaw, gitlinks, indexFindings := readGitlinks(ctx, nddevRoot)
	findings = append(findings, registryFindings...)
	findings = append(findings, anchorFindings...)
	findings = append(findings, gitmodulesFindings...)
	findings = append(findings, indexFindings...)
	gdsInputs, gdsInputFindings := gdsBridgeInputDigests(gdsRoot)
	findings = append(findings, gdsInputFindings...)
	profiles, profileFindings := loadGDSProfileEdges(gdsRoot, services.Schemas)
	findings = append(findings, profileFindings...)
	mappingEdges := []ModuleBridgeMappingEdge{}
	if len(findings) == 0 {
		var comparisonFindings []domain.Finding
		mappingEdges, comparisonFindings = compareModuleBridge(
			ctx, nddevRoot, document, registry, anchor, gitmodulePaths, gitlinks,
			profiles,
		)
		findings = append(findings, comparisonFindings...)
	}
	inputDigests := map[string]string{
		"bridge":                report.InputDigest,
		"nddev_module_registry": bytesDigest(registryRaw),
		"nddev_anchor":          bytesDigest(anchorRaw),
		"nddev_gitmodules":      bytesDigest(gitmodulesRaw),
		"nddev_git_index":       bytesDigest(indexRaw),
	}
	for name, digest := range gdsInputs {
		inputDigests[name] = digest
	}
	parityDigest, err := canonicaljson.Digest(map[string]any{
		"contract": harness.ModuleBridgeContract, "identity_digest": report.IdentityDigest,
		"input_digests": inputDigests, "mapping_edges": mappingEdges,
	})
	if err != nil {
		findings = append(findings, domain.Finding{
			Code: "GDS_MODULE_BRIDGE_PARITY_DIGEST_FAILED", Severity: domain.SeverityHigh,
			Message:  "Cannot compute the cross-repository parity digest.",
			Evidence: map[string]any{"error": err.Error()},
		})
	}
	data := ModuleBridgeParityData{
		Contract: harness.ModuleBridgeContract, Mappings: len(document.Mappings),
		MappingEdges: mappingEdges, IdentityDigest: report.IdentityDigest, InputDigests: inputDigests,
		ParityDigest: parityDigest,
	}
	sort.Slice(findings, func(left, right int) bool {
		if findings[left].Code == findings[right].Code {
			return fmt.Sprint(findings[left].Evidence) < fmt.Sprint(findings[right].Evidence)
		}
		return findings[left].Code < findings[right].Code
	})
	return domain.NewEnvelope(command, classifyFindings(findings), data, findings...)
}

func exactCheckoutRoot(
	ctx context.Context,
	value string,
	label string,
) (string, *domain.Finding) {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
		finding := domain.Finding{
			Code: "GDS_MODULE_BRIDGE_ROOT_INVALID", Severity: domain.SeverityHigh,
			Message:  "Bridge parity roots must be explicit absolute Git checkout paths.",
			Evidence: map[string]any{"root_kind": label, "path": value},
		}
		return "", &finding
	}
	clean := filepath.Clean(value)
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		finding := domain.Finding{
			Code: "GDS_MODULE_BRIDGE_ROOT_INVALID", Severity: domain.SeverityHigh,
			Message:  "Bridge parity root must be an existing non-symlink directory.",
			Evidence: map[string]any{"root_kind": label, "path": clean},
		}
		return "", &finding
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		finding := domain.Finding{
			Code: "GDS_MODULE_BRIDGE_ROOT_INVALID", Severity: domain.SeverityHigh,
			Message:  "Bridge parity root cannot be resolved safely.",
			Evidence: map[string]any{"root_kind": label, "path": clean},
		}
		return "", &finding
	}
	clean = filepath.Clean(resolved)
	output, err := runReadOnlyGit(ctx, clean, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(strings.TrimSpace(string(output))) != clean {
		finding := domain.Finding{
			Code: "GDS_MODULE_BRIDGE_ROOT_INVALID", Severity: domain.SeverityHigh,
			Message:  "Bridge parity root must be the exact top level of a Git checkout.",
			Evidence: map[string]any{"root_kind": label, "path": clean},
		}
		return "", &finding
	}
	return clean, nil
}

func loadNDDevRegistry(
	path string,
) ([]byte, nddevRegistryDocument, []domain.Finding) {
	raw, err := readRegularFile(path)
	if err != nil {
		return nil, nddevRegistryDocument{}, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_NDDEV_REGISTRY_INVALID",
			"Cannot read the NDDev module registry.", path, err,
		)}
	}
	var document nddevRegistryDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil || len(document.Modules) == 0 {
		return raw, nddevRegistryDocument{}, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_NDDEV_REGISTRY_INVALID",
			"Cannot decode a non-empty NDDev module registry.", path, err,
		)}
	}
	if trailingErr := decoder.Decode(&struct{}{}); trailingErr != io.EOF {
		return raw, nddevRegistryDocument{}, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_NDDEV_REGISTRY_INVALID",
			"NDDev module registry contains trailing JSON values.", path, nil,
		)}
	}
	return raw, document, nil
}

func loadNDDevAnchor(
	path string,
) ([]byte, nddevAnchorDocument, []domain.Finding) {
	raw, err := readRegularFile(path)
	if err != nil {
		return nil, nddevAnchorDocument{}, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_NDDEV_ANCHOR_INVALID",
			"Cannot read the NDDev repository anchor.", path, err,
		)}
	}
	value, decodeErr := serialization.Decode(path, raw)
	if decodeErr != nil {
		return raw, nddevAnchorDocument{}, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_NDDEV_ANCHOR_INVALID",
			"Cannot decode the NDDev repository anchor.", path, decodeErr,
		)}
	}
	object, _ := value.(map[string]any)
	repository, _ := object["repository"].(map[string]any)
	relationships, _ := object["relationships"].([]any)
	document := nddevAnchorDocument{}
	document.Repository.ID, _ = repository["id"].(string)
	for _, rawRelationship := range relationships {
		relationship, _ := rawRelationship.(map[string]any)
		document.Relationships = append(document.Relationships, struct {
			Type           string `json:"type"`
			Target         string `json:"target"`
			GitmodulesName string `json:"gitmodules_name"`
		}{
			Type:           fmt.Sprint(relationship["type"]),
			Target:         fmt.Sprint(relationship["target"]),
			GitmodulesName: fmt.Sprint(relationship["gitmodules_name"]),
		})
	}
	if document.Repository.ID == "" {
		return raw, nddevAnchorDocument{}, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_NDDEV_ANCHOR_INVALID",
			"NDDev repository anchor has no repository ID.", path, nil,
		)}
	}
	return raw, document, nil
}

func readGitmodulePaths(
	ctx context.Context,
	root string,
) ([]byte, map[string]bool, []domain.Finding) {
	raw, err := readRegularFile(filepath.Join(root, ".gitmodules"))
	if err != nil {
		return nil, nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_NDDEV_GITMODULES_INVALID",
			"Cannot read the NDDev .gitmodules file.", filepath.Join(root, ".gitmodules"), err,
		)}
	}
	output, err := runReadOnlyGit(
		ctx, root, "config", "-f", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`,
	)
	if err != nil {
		return raw, nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_NDDEV_GITMODULES_INVALID",
			"Cannot enumerate NDDev gitmodule paths.", filepath.Join(root, ".gitmodules"), err,
		)}
	}
	paths := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return raw, nil, []domain.Finding{parityFinding(
				"GDS_MODULE_BRIDGE_NDDEV_GITMODULES_INVALID",
				"NDDev gitmodule path output is malformed.", filepath.Join(root, ".gitmodules"), nil,
			)}
		}
		paths[fields[1]] = true
	}
	return raw, paths, nil
}

func readGitlinks(
	ctx context.Context,
	root string,
) ([]byte, map[string]string, []domain.Finding) {
	output, err := runReadOnlyGit(ctx, root, "ls-files", "-s", "--", "modules")
	if err != nil {
		return nil, nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_NDDEV_GIT_INDEX_INVALID",
			"Cannot enumerate NDDev stage-zero module gitlinks.", root, err,
		)}
	}
	gitlinks := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return output, nil, []domain.Finding{parityFinding(
				"GDS_MODULE_BRIDGE_NDDEV_GIT_INDEX_INVALID",
				"NDDev git index output is malformed.", root, nil,
			)}
		}
		if fields[0] == "160000" && fields[2] == "0" {
			gitlinks[fields[3]] = fields[1]
		}
	}
	return output, gitlinks, nil
}

func compareModuleBridge(
	ctx context.Context,
	nddevRoot string,
	bridge harness.ModuleBridgeDocument,
	registry nddevRegistryDocument,
	anchor nddevAnchorDocument,
	gitmodules map[string]bool,
	gitlinks map[string]string,
	profiles map[string]gdsProfileEdge,
) ([]ModuleBridgeMappingEdge, []domain.Finding) {
	findings := []domain.Finding{}
	edges := []ModuleBridgeMappingEdge{}
	modules := map[string]nddevModule{}
	publicRepositories := map[string]string{}
	for _, module := range registry.Modules {
		if _, duplicate := modules[module.ID]; duplicate {
			findings = append(findings, bridgeParityMismatch(
				module.ID, "NDDev module ID occurs more than once in the registry.",
			))
		}
		modules[module.ID] = module
		repositoryKey := strings.ToLower(module.Repository)
		if prior, duplicate := publicRepositories[repositoryKey]; duplicate {
			findings = append(findings, bridgeParityMismatch(
				module.Repository,
				"NDDev public repository is owned by more than one module: "+
					prior+" and "+module.ID,
			))
		}
		publicRepositories[repositoryKey] = module.ID
	}
	relationships := map[string]string{}
	relationshipTargets := map[string]string{}
	for _, relationship := range anchor.Relationships {
		if relationship.Type != bridge.RelationshipScope {
			continue
		}
		if _, duplicate := relationships[relationship.GitmodulesName]; duplicate {
			findings = append(findings, bridgeParityMismatch(
				relationship.GitmodulesName,
				"NDDev anchor contains duplicate git-submodule-consumer paths.",
			))
		}
		relationships[relationship.GitmodulesName] = relationship.Target
		if prior, duplicate := relationshipTargets[relationship.Target]; duplicate {
			findings = append(findings, bridgeParityMismatch(
				relationship.Target,
				"NDDev anchor repository target is owned by more than one module path: "+
					prior+" and "+relationship.GitmodulesName,
			))
		}
		relationshipTargets[relationship.Target] = relationship.GitmodulesName
	}
	mapped := map[string]bool{}
	for _, mapping := range bridge.Mappings {
		module, found := modules[mapping.ModuleID]
		derivedModulePath := filepath.ToSlash(filepath.Join("modules", mapping.ModuleID))
		if mapping.Lifecycle == "retired" {
			if found {
				findings = append(findings, bridgeParityMismatch(
					mapping.ModuleID, "retired bridge mapping remains in the current NDDev registry",
				))
			}
			if _, present := relationships[derivedModulePath]; present {
				findings = append(findings, bridgeParityMismatch(
					mapping.ModuleID, "retired bridge mapping retains a current GDS relationship",
				))
			}
			continue
		}
		if !found {
			findings = append(findings, bridgeParityMismatch(
				mapping.ModuleID, "Bridge module is absent from the NDDev registry.",
			))
			continue
		}
		mapped[mapping.ModuleID] = true
		derivedValidationSlice := filepath.ToSlash(filepath.Join("validation", mapping.ModuleID))
		expectedSlice := filepath.ToSlash(filepath.Dir(module.ValidationManifest))
		gdsRepositoryID := relationships[derivedModulePath]
		checks := []struct {
			ok       bool
			evidence string
		}{
			{module.Path == derivedModulePath, "module path is not derived from module_id"},
			{expectedSlice == derivedValidationSlice, "validation slice is not derived from module_id"},
			{anchor.Repository.ID == bridge.Consumer.RepositoryID, "consumer repository ID differs"},
			{gdsRepositoryID != "", "GDS repository relationship is missing"},
			{gitmodules[derivedModulePath], "derived module path is absent from .gitmodules"},
			{gitlinks[derivedModulePath] != "", "derived module path is absent from the stage-zero git index"},
			{gitlinks[derivedModulePath] == module.ExpectedHead, "stage-zero gitlink differs from expected_head"},
			// The evidence owner is not compared here. It is a bridge-owned
			// declaration with no counterpart on the NDDev side, so parity has
			// nothing to compare it against; the schema already constrains its
			// shape and `validate` already requires it. The check that used to
			// sit here pinned the literal "example-org/example-harnesses",
			// which is why that placeholder survived in the bridge for so long:
			// naming the real owner failed a check that demanded the example.
		}
		for _, check := range checks {
			if !check.ok {
				findings = append(findings, bridgeParityMismatch(mapping.ModuleID, check.evidence))
			}
		}
		edge, edgeFindings := validateMappingContractProfile(
			ctx, nddevRoot, mapping, module, profiles[mapping.HarnessID],
			gdsRepositoryID, gitlinks[derivedModulePath],
		)
		edges = append(edges, edge)
		findings = append(findings, edgeFindings...)
		manifestPath := filepath.Join(nddevRoot, filepath.FromSlash(module.ValidationManifest))
		if _, err := readRegularFile(manifestPath); err != nil {
			findings = append(findings, bridgeParityMismatch(
				mapping.ModuleID, "validation manifest is missing or unsafe",
			))
		}
	}
	for moduleID := range modules {
		if !mapped[moduleID] {
			findings = append(findings, bridgeParityMismatch(
				moduleID, "NDDev registry module has no bridge mapping.",
			))
		}
	}
	modulePaths := map[string]bool{}
	for _, module := range modules {
		modulePaths[module.Path] = true
	}
	for path := range gitmodules {
		if !modulePaths[path] {
			findings = append(findings, bridgeParityMismatch(
				path, ".gitmodules path has no NDDev registry module.",
			))
		}
	}
	for path := range gitlinks {
		if !modulePaths[path] {
			findings = append(findings, bridgeParityMismatch(
				path, "stage-zero gitlink has no NDDev registry module.",
			))
		}
	}
	for path := range relationships {
		if !modulePaths[path] {
			findings = append(findings, bridgeParityMismatch(
				path, "GDS relationship has no NDDev registry module.",
			))
		}
	}
	sort.Slice(edges, func(left, right int) bool {
		return edges[left].ModuleID < edges[right].ModuleID
	})
	return edges, findings
}

func validateMappingContractProfile(
	ctx context.Context,
	nddevRoot string,
	mapping harness.ModuleHarnessMapping,
	module nddevModule,
	profile gdsProfileEdge,
	gdsRepositoryID string,
	gitlinkSHA string,
) (ModuleBridgeMappingEdge, []domain.Finding) {
	findings := []domain.Finding{}
	registryExpectation := module.Expectations.Contract
	registryExpectationDigest, err := canonicaljson.Digest(registryExpectation)
	if err != nil {
		findings = append(findings, bridgeParityMismatch(
			mapping.ModuleID, "NDDev registry contract expectation cannot be digested",
		))
	}
	registryContractVersion := positiveInteger(registryExpectation["contract_version"])
	publicContract, publicContractDigest, publicContractFindings := loadPublicContractAtGitlink(
		ctx, nddevRoot, mapping.ModuleID, gitlinkSHA,
	)
	findings = append(findings, publicContractFindings...)
	findings = append(findings, validatePublicModuleProjectionOwnershipAtGitlink(
		ctx, nddevRoot, mapping.ModuleID, gitlinkSHA,
	)...)
	publicContractVersion := positiveInteger(publicContract["contract_version"])
	capabilityVersion, capabilityVersionOK := profile.CapabilityVersion.(string)
	capabilityVersionOK = capabilityVersionOK && capabilityVersion != ""
	checks := []struct {
		ok     bool
		detail string
	}{
		{registryContractVersion > 0, "registry contract version is missing"},
		{fmt.Sprint(registryExpectation["product_name"]) == mapping.ModuleID, "registry contract product_name differs"},
		{fmt.Sprint(registryExpectation["github_repository"]) == module.Repository, "registry contract repository differs"},
		{publicContractVersion > 0, "public contract version is missing at the exact gitlink"},
		{fmt.Sprint(publicContract["product_name"]) == mapping.ModuleID, "public contract product_name differs at the exact gitlink"},
		{fmt.Sprint(publicContract["github_repository"]) == module.Repository, "public contract repository differs at the exact gitlink"},
		{publicContractVersion == registryContractVersion, "public contract version differs from the NDDev registry expectation"},
		{profile.Object != nil, "GDS capability profile is missing"},
		{capabilityVersionOK, "GDS capability version must be a non-empty string"},
	}
	if profile.Object != nil {
		harnessProfile, _ := profile.Object["harness_profile"].(map[string]any)
		runtimeTests, _ := harnessProfile["runtime_tests"].(map[string]any)
		checks = append(checks,
			struct {
				ok     bool
				detail string
			}{fmt.Sprint(harnessProfile["id"]) == mapping.HarnessID, "GDS capability profile id differs"},
			struct {
				ok     bool
				detail string
			}{runtimeTests["required"] == false, "GDS runtime_tests.required must remain false"},
		)
	}
	for _, check := range checks {
		if !check.ok {
			findings = append(findings, bridgeParityMismatch(mapping.ModuleID, check.detail))
		}
	}
	return ModuleBridgeMappingEdge{
		ModuleID: mapping.ModuleID, HarnessID: mapping.HarnessID,
		PublicRepository: module.Repository, GDSRepositoryID: gdsRepositoryID,
		ExpectedHead: module.ExpectedHead, GitlinkSHA: gitlinkSHA,
		RegistryContractVersion:   registryContractVersion,
		RegistryExpectationDigest: registryExpectationDigest,
		PublicContractVersion:     publicContractVersion, PublicContractDigest: publicContractDigest,
		GDSCapabilityVersion: capabilityVersion, GDSProfileDigest: profile.Digest,
	}, findings
}

func validatePublicModuleProjectionOwnershipAtGitlink(
	ctx context.Context,
	nddevRoot string,
	moduleID string,
	gitlinkSHA string,
) []domain.Finding {
	if gitlinkSHA == "" {
		return []domain.Finding{bridgeParityMismatch(
			moduleID, "cannot read repository projection ownership without an exact gitlink",
		)}
	}
	moduleRoot := filepath.Join(nddevRoot, "modules", moduleID)
	raw, err := runReadOnlyGit(
		ctx, moduleRoot, "cat-file", "blob", gitlinkSHA+":.gds/repository.yaml",
	)
	if err != nil {
		return []domain.Finding{bridgeParityMismatch(
			moduleID, "cannot read repository projection ownership from the exact gitlink",
		)}
	}
	value, err := serialization.Decode(".gds/repository.yaml", raw)
	if err != nil {
		return []domain.Finding{bridgeParityMismatch(
			moduleID, "repository projection ownership at the exact gitlink is invalid",
		)}
	}
	document, _ := value.(map[string]any)
	agent, _ := document["agent"].(map[string]any)
	verification, _ := document["verification"].(map[string]any)
	commands, _ := verification["commands"].(map[string]any)
	required, _ := verification["required"].([]any)
	testCommands, _ := commands["test"].([]any)
	testCommand := ""
	if len(testCommands) == 1 {
		testCommand, _ = testCommands[0].(string)
	}
	validatorModeOK := false
	treeEntry, validatorErr := runReadOnlyGit(
		ctx, moduleRoot, "ls-tree", gitlinkSHA, "--", publicModuleValidatorPath,
	)
	if validatorErr == nil {
		fields := strings.SplitN(strings.TrimSpace(string(treeEntry)), " ", 3)
		validatorModeOK = len(fields) == 3 &&
			(fields[0] == "100644" || fields[0] == "100755") &&
			fields[1] == "blob" &&
			strings.HasSuffix(fields[2], "\t"+publicModuleValidatorPath)
	}
	checks := []struct {
		ok     bool
		detail string
	}{
		{agent["generated_agents"] == false, "public module agent.generated_agents must be false"},
		{stringListContains(required, "test"), "public module verification.required must include test"},
		{testCommand == publicModuleValidatorCommand,
			"public module test verification command must exactly match the canonical validator command"},
		{validatorModeOK,
			"public module canonical validator must be a tracked regular executable or non-executable file"},
	}
	findings := []domain.Finding{}
	for _, check := range checks {
		if !check.ok {
			findings = append(findings, bridgeParityMismatch(moduleID, check.detail))
		}
	}
	return findings
}

func stringListContains(values []any, expected string) bool {
	for _, value := range values {
		if fmt.Sprint(value) == expected {
			return true
		}
	}
	return false
}

func positiveInteger(raw any) int {
	switch value := raw.(type) {
	case float64:
		if !math.IsNaN(value) && !math.IsInf(value, 0) &&
			value > 0 && value == math.Trunc(value) && value <= float64(^uint(0)>>1) {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	case json.Number:
		integer, err := value.Int64()
		if err == nil && integer > 0 && uint64(integer) <= uint64(^uint(0)>>1) {
			return int(integer)
		}
	}
	return 0
}

func loadPublicContractAtGitlink(
	ctx context.Context,
	nddevRoot string,
	moduleID string,
	gitlinkSHA string,
) (map[string]any, string, []domain.Finding) {
	if gitlinkSHA == "" {
		return nil, "", []domain.Finding{bridgeParityMismatch(
			moduleID, "cannot read public contract without an exact gitlink",
		)}
	}
	moduleRoot := filepath.Join(nddevRoot, "modules", moduleID)
	info, err := os.Lstat(moduleRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", []domain.Finding{bridgeParityMismatch(
			moduleID, "exact-gitlink module checkout is missing or unsafe",
		)}
	}
	raw, err := runReadOnlyGit(
		ctx, moduleRoot, "cat-file", "blob", gitlinkSHA+":config/nddev-contract.json",
	)
	if err != nil {
		return nil, "", []domain.Finding{bridgeParityMismatch(
			moduleID, "cannot read public contract from the exact gitlink",
		)}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var contract map[string]any
	if err := decoder.Decode(&contract); err != nil || contract == nil {
		return nil, "", []domain.Finding{bridgeParityMismatch(
			moduleID, "public contract at the exact gitlink is invalid",
		)}
	}
	if trailingErr := decoder.Decode(&struct{}{}); trailingErr != io.EOF {
		return nil, "", []domain.Finding{bridgeParityMismatch(
			moduleID, "public contract at the exact gitlink has trailing JSON values",
		)}
	}
	digest, err := canonicaljson.Digest(contract)
	if err != nil {
		return nil, "", []domain.Finding{bridgeParityMismatch(
			moduleID, "public contract at the exact gitlink cannot be digested",
		)}
	}
	return contract, digest, nil
}

func loadGDSProfileEdges(
	root string,
	schemas *validation.Set,
) (map[string]gdsProfileEdge, []domain.Finding) {
	registryPath := filepath.Join(root, "harnesses", "capability-registry.yaml")
	raw, err := readRegularFile(registryPath)
	if err != nil {
		return nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
			"Cannot read the canonical GDS harness registry.", registryPath, err,
		)}
	}
	value, err := serialization.Decode(registryPath, raw)
	if err != nil {
		return nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
			"Cannot decode the canonical GDS harness registry.", registryPath, err,
		)}
	}
	object, _ := value.(map[string]any)
	entries, _ := object["harnesses"].([]any)
	result := map[string]gdsProfileEdge{}
	findings := []domain.Finding{}
	for _, rawEntry := range entries {
		entry, _ := rawEntry.(map[string]any)
		id := fmt.Sprint(entry["id"])
		profileRelative := fmt.Sprint(entry["profile"])
		profilePath := filepath.Join(root, filepath.FromSlash(profileRelative))
		if schemaFindings := schemas.ValidateFile("harness-profile", profilePath); len(schemaFindings) != 0 {
			findings = append(findings, schemaFindings...)
			continue
		}
		profileRaw, readErr := readRegularFile(profilePath)
		if readErr != nil {
			findings = append(findings, parityFinding(
				"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
				"Cannot read a canonical GDS harness profile.", profilePath, readErr,
			))
			continue
		}
		profileValue, decodeErr := serialization.Decode(profilePath, profileRaw)
		if decodeErr != nil {
			findings = append(findings, parityFinding(
				"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
				"Cannot decode a canonical GDS harness profile.", profilePath, decodeErr,
			))
			continue
		}
		profileObject, _ := profileValue.(map[string]any)
		harnessProfile, _ := profileObject["harness_profile"].(map[string]any)
		digest, digestErr := canonicaljson.Digest(profileObject)
		if digestErr != nil {
			findings = append(findings, parityFinding(
				"GDS_MODULE_BRIDGE_PARITY_DIGEST_FAILED",
				"Cannot digest a canonical GDS harness profile.", profilePath, digestErr,
			))
			continue
		}
		result[id] = gdsProfileEdge{
			Object:            profileObject,
			CapabilityVersion: harnessProfile["capability_version"],
			Digest:            digest,
		}
	}
	return result, findings
}

func gdsBridgeInputDigests(
	root string,
) (map[string]string, []domain.Finding) {
	registryPath := filepath.Join(root, "harnesses", "capability-registry.yaml")
	registryRaw, err := readRegularFile(registryPath)
	if err != nil {
		return nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
			"Cannot read the canonical GDS harness registry.", registryPath, err,
		)}
	}
	profilePaths, err := filepath.Glob(filepath.Join(root, "harnesses", "*", "profile.yaml"))
	if err != nil {
		return nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
			"Cannot enumerate canonical GDS harness profiles.", root, err,
		)}
	}
	sort.Strings(profilePaths)
	profiles := map[string]string{}
	for _, path := range profilePaths {
		raw, readErr := readRegularFile(path)
		if readErr != nil {
			return nil, []domain.Finding{parityFinding(
				"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
				"Cannot read a canonical GDS harness profile.", path, readErr,
			)}
		}
		profiles[filepath.Base(filepath.Dir(path))] = bytesDigest(raw)
	}
	profileDigest, err := canonicaljson.Digest(profiles)
	if err != nil {
		return nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_PARITY_DIGEST_FAILED",
			"Cannot digest canonical GDS harness profiles.", root, err,
		)}
	}
	devicePaths, err := filepath.Glob(filepath.Join(root, "estate", "devices", "*.yaml"))
	if err != nil {
		return nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
			"Cannot enumerate canonical GDS device descriptors.", root, err,
		)}
	}
	sort.Strings(devicePaths)
	selections := map[string][]string{}
	for _, path := range devicePaths {
		raw, readErr := readRegularFile(path)
		if readErr != nil {
			return nil, []domain.Finding{parityFinding(
				"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
				"Cannot read a canonical GDS device descriptor.", path, readErr,
			)}
		}
		value, decodeErr := serialization.Decode(path, raw)
		if decodeErr != nil {
			return nil, []domain.Finding{parityFinding(
				"GDS_MODULE_BRIDGE_GDS_INPUT_INVALID",
				"Cannot decode a canonical GDS device descriptor.", path, decodeErr,
			)}
		}
		object, _ := value.(map[string]any)
		device, _ := object["device"].(map[string]any)
		deviceID, _ := device["id"].(string)
		selected, _ := object["harnesses"].([]any)
		harnesses := make([]string, 0, len(selected))
		for _, rawHarness := range selected {
			harnesses = append(harnesses, fmt.Sprint(rawHarness))
		}
		sort.Strings(harnesses)
		selections[deviceID] = harnesses
	}
	deviceDigest, err := canonicaljson.Digest(selections)
	if err != nil {
		return nil, []domain.Finding{parityFinding(
			"GDS_MODULE_BRIDGE_PARITY_DIGEST_FAILED",
			"Cannot digest canonical GDS device selections.", root, err,
		)}
	}
	return map[string]string{
		"gds_harness_registry":  bytesDigest(registryRaw),
		"gds_harness_profiles":  profileDigest,
		"gds_device_selections": deviceDigest,
	}, nil
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("not a regular non-symlink file")
	}
	return os.ReadFile(path)
}

func runReadOnlyGit(
	ctx context.Context,
	root string,
	args ...string,
) ([]byte, error) {
	gitPath, err := trustedGitExecutable()
	if err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp("", "gds-module-bridge-git-home-")
	if err != nil {
		return nil, fmt.Errorf("create isolated Git home: %w", err)
	}
	defer os.RemoveAll(home)
	hooksPath := filepath.Join(home, "hooks-disabled")
	if err := os.Mkdir(hooksPath, 0o700); err != nil {
		return nil, fmt.Errorf("create inert Git hooks path: %w", err)
	}
	commandArgs := []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + hooksPath,
		"-C", root,
	}
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, gitPath, commandArgs...)
	command.Env = readOnlyGitEnvironment(home)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func readOnlyGitEnvironment(home string) []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"LANG=C", "LC_ALL=C",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=" + home,
	}
}

func trustedGitExecutable() (string, error) {
	const path = "/usr/bin/git"
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("trusted Git executable is unavailable at %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("trusted Git executable is unsafe at %s", path)
	}
	return path, nil
}

func bytesDigest(raw []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

func parityFinding(code, message, path string, err error) domain.Finding {
	if err != nil {
		message += " " + err.Error()
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: map[string]any{"path": path},
	}
}

func bridgeParityMismatch(identity, detail string) domain.Finding {
	return domain.Finding{
		Code: "GDS_MODULE_BRIDGE_PARITY_MISMATCH", Severity: domain.SeverityHigh,
		Message:  "NDDev module inventory and the canonical GDS bridge differ.",
		Evidence: map[string]any{"identity": identity, "detail": detail},
	}
}
