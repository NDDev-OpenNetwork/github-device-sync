// Package validation compiles the canonical embedded schemas and validates GDS
// documents without network resolution.
package validation

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	gdsschemas "github.com/NDDev-OpenNetwork/github-device-sync/schemas"
)

type Set struct {
	compiled map[string]*jsonschema.Schema
}

type offlineLoader struct{}

type ecmaRegexp struct {
	mu       sync.Mutex
	compiled *regexp2.Regexp
}

func (regexp *ecmaRegexp) MatchString(value string) bool {
	regexp.mu.Lock()
	defer regexp.mu.Unlock()
	matched, err := regexp.compiled.MatchString(value)
	return err == nil && matched
}

func (regexp *ecmaRegexp) String() string {
	regexp.mu.Lock()
	defer regexp.mu.Unlock()
	return regexp.compiled.String()
}

func compileECMARegexp(expression string) (jsonschema.Regexp, error) {
	compiled, err := regexp2.Compile(expression, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	compiled.MatchTimeout = 2 * time.Second
	return &ecmaRegexp{compiled: compiled}, nil
}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("network schema resolution is forbidden: %s", url)
}

func NewSchemaSet() (*Set, error) {
	entries, err := fs.ReadDir(gdsschemas.V1, "v1")
	if err != nil {
		return nil, fmt.Errorf("read embedded schemas: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(offlineLoader{})
	compiler.UseRegexpEngine(compileECMARegexp)

	type resource struct {
		name string
		id   string
		doc  any
	}
	resources := []resource{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		path := "v1/" + entry.Name()
		raw, err := gdsschemas.V1.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read embedded schema %s: %w", path, err)
		}
		doc, err := serialization.Decode(path, raw)
		if err != nil {
			return nil, fmt.Errorf("decode embedded schema %s: %w", path, err)
		}
		object, ok := doc.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema %s root is not an object", path)
		}
		id, ok := object["$id"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("schema %s has no non-empty $id", path)
		}
		resources = append(resources, resource{
			name: strings.TrimSuffix(entry.Name(), ".schema.json"), id: id, doc: doc,
		})
	}
	if len(resources) == 0 {
		return nil, errors.New("no embedded GDS schemas found")
	}
	for _, resource := range resources {
		if err := compiler.AddResource(resource.id, resource.doc); err != nil {
			return nil, fmt.Errorf("register schema %s: %w", resource.name, err)
		}
	}

	compiled := map[string]*jsonschema.Schema{}
	for _, resource := range resources {
		schema, err := compiler.Compile(resource.id)
		if err != nil {
			return nil, fmt.Errorf("compile schema %s: %w", resource.name, err)
		}
		compiled[resource.name] = schema
	}
	return &Set{compiled: compiled}, nil
}

func (set *Set) Names() []string {
	names := make([]string, 0, len(set.compiled))
	for name := range set.compiled {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (set *Set) Validate(schemaName string, value any, source string) []domain.Finding {
	schema, found := set.compiled[schemaName]
	if !found || schemaName == "common" {
		return []domain.Finding{{
			Code:     "GDS_SCHEMA_NAME_UNKNOWN",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Unknown instance schema %q.", schemaName),
			Evidence: map[string]any{"source": source, "schema": schemaName},
		}}
	}
	if err := schema.Validate(value); err != nil {
		return validationErrorFindings(schemaName, source, err)
	}
	return semanticFindings(schemaName, source, value)
}

func (set *Set) ValidateFile(schemaName, path string) []domain.Finding {
	value, err := serialization.DecodeFile(path)
	if err != nil {
		return []domain.Finding{serializationFinding(path, err)}
	}
	return set.Validate(schemaName, value, path)
}

func (set *Set) ValidateCanonical(root string, fixtureIndex string) []domain.Finding {
	findings := []domain.Finding{}
	findings = append(findings, set.ValidateFile(
		"repository", filepath.Join(root, ".gds", "repository.yaml"),
	)...)
	findings = append(findings, set.ValidateFile(
		"migration-registry", filepath.Join(root, "schemas", "migrations", "registry.yaml"),
	)...)
	findings = append(findings, set.ValidateFile(
		"skill-registry", filepath.Join(root, "skills", "registry.yaml"),
	)...)
	findings = append(findings, set.ValidateFile(
		"harness-registry", filepath.Join(root, "harnesses", "capability-registry.yaml"),
	)...)
	findings = append(findings, set.ValidateFile(
		"module-harness-bridge", filepath.Join(root, "harnesses", "module-bridge.yaml"),
	)...)
	profilePaths, err := filepath.Glob(filepath.Join(root, "harnesses", "*", "profile.yaml"))
	if err != nil {
		findings = append(findings, domain.Finding{
			Code: "GDS_HARNESS_PROFILE_DISCOVERY_FAILED", Severity: domain.SeverityHigh,
			Message:  "Cannot enumerate canonical harness profiles.",
			Evidence: map[string]any{"root": root, "error": err.Error()},
		})
	} else {
		sort.Strings(profilePaths)
		for _, profilePath := range profilePaths {
			findings = append(findings, set.ValidateFile("harness-profile", profilePath)...)
		}
	}
	findings = append(findings, set.ValidateFile(
		"bundle-trust", filepath.Join(root, "requirements", "bundle-trust.yaml"),
	)...)
	findings = append(findings, set.ValidateFile(
		"source-register", filepath.Join(root, "docs", "source-register", "sources.yaml"),
	)...)
	exceptionPaths, err := filepath.Glob(filepath.Join(root, "estate", "exceptions", "*.yaml"))
	if err != nil {
		findings = append(findings, domain.Finding{
			Code: "GDS_POLICY_EXCEPTION_DISCOVERY_FAILED", Severity: domain.SeverityHigh,
			Message:  "Cannot enumerate policy exception sources.",
			Evidence: map[string]any{"root": root, "error": err.Error()},
		})
	} else {
		sort.Strings(exceptionPaths)
		for _, exceptionPath := range exceptionPaths {
			findings = append(
				findings, set.ValidateFile("policy-exception", exceptionPath)...,
			)
		}
	}
	if fixtureIndex != "" {
		findings = append(findings, set.ValidateFixtureIndex(fixtureIndex)...)
	}
	return findings
}

func (set *Set) ValidateFixtureIndex(indexPath string) []domain.Finding {
	value, err := serialization.DecodeFile(indexPath)
	if err != nil {
		return []domain.Finding{serializationFinding(indexPath, err)}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return []domain.Finding{fixtureFinding(
			"GDS_FIXTURE_INDEX_INVALID", "Fixture index root must be an object.", indexPath, nil,
		)}
	}
	cases, ok := object["cases"].([]any)
	if !ok {
		return []domain.Finding{fixtureFinding(
			"GDS_FIXTURE_INDEX_INVALID", "Fixture index must contain a cases array.", indexPath, nil,
		)}
	}
	root, err := filepath.Abs(filepath.Dir(indexPath))
	if err != nil {
		return []domain.Finding{fixtureFinding(
			"GDS_FIXTURE_INDEX_INVALID", "Cannot resolve fixture root.", indexPath, err,
		)}
	}

	findings := []domain.Finding{}
	for index, rawCase := range cases {
		fixtureCase, ok := rawCase.(map[string]any)
		if !ok {
			findings = append(findings, fixtureFinding(
				"GDS_FIXTURE_CASE_INVALID",
				fmt.Sprintf("Fixture case %d must be an object.", index),
				indexPath,
				nil,
			))
			continue
		}
		caseID, idOK := fixtureCase["id"].(string)
		schemaName, schemaOK := fixtureCase["schema"].(string)
		relativePath, pathOK := fixtureCase["path"].(string)
		expectedValid, validOK := fixtureCase["valid"].(bool)
		expectedCode, codeOK := fixtureCase["expected_code"].(string)
		if !idOK || !schemaOK || !pathOK || !validOK || (!expectedValid && !codeOK) {
			findings = append(findings, fixtureFinding(
				"GDS_FIXTURE_CASE_INVALID",
				fmt.Sprintf("Fixture case %d has invalid metadata.", index),
				indexPath,
				nil,
			))
			continue
		}
		fixturePath, err := filepath.Abs(filepath.Join(root, relativePath))
		if err != nil || !pathWithin(root, fixturePath) {
			findings = append(findings, fixtureFinding(
				"GDS_FIXTURE_PATH_OUTSIDE_ROOT",
				fmt.Sprintf("Fixture %q escapes the fixture root.", caseID),
				fixturePath,
				err,
			))
			continue
		}
		caseFindings := set.ValidateFile(schemaName, fixturePath)
		actualValid := len(caseFindings) == 0
		matchedCode := expectedValid
		if !expectedValid {
			matchedCode = false
			for _, finding := range caseFindings {
				if finding.Code == expectedCode {
					matchedCode = true
					break
				}
			}
		}
		if actualValid != expectedValid || !matchedCode {
			actualCodes := make([]string, 0, len(caseFindings))
			for _, finding := range caseFindings {
				actualCodes = append(actualCodes, finding.Code)
			}
			findings = append(findings, domain.Finding{
				Code:     "GDS_FIXTURE_EXPECTATION_MISMATCH",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Fixture %q did not meet its expected result.", caseID),
				Evidence: map[string]any{
					"case": caseID, "path": fixturePath, "schema": schemaName,
					"expected_valid": expectedValid, "expected_code": expectedCode,
					"actual_valid": actualValid, "actual_codes": actualCodes,
				},
			})
		}
	}
	return findings
}

func validationErrorFindings(schemaName, source string, err error) []domain.Finding {
	var validationError *jsonschema.ValidationError
	if !errors.As(err, &validationError) {
		return []domain.Finding{{
			Code:     "GDS_INSTANCE_INVALID",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("%s violates %s: %v", source, schemaName, err),
			Evidence: map[string]any{"source": source, "schema": schemaName},
		}}
	}
	output := validationError.BasicOutput()
	return []domain.Finding{{
		Code:     "GDS_INSTANCE_INVALID",
		Severity: domain.SeverityHigh,
		Message:  fmt.Sprintf("%s violates %s: %s", source, schemaName, validationError),
		Evidence: map[string]any{
			"source": source, "schema": schemaName, "validation": output,
		},
	}}
}

func serializationFinding(path string, err error) domain.Finding {
	code := "GDS_INPUT_PARSE_FAILED"
	evidence := map[string]any{"path": path}
	var contractError *serialization.ContractError
	if errors.As(err, &contractError) {
		code = contractError.Code
		if contractError.Line > 0 {
			evidence["line"] = contractError.Line
			evidence["column"] = contractError.Column
		}
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: err.Error(), Evidence: evidence,
	}
}

func fixtureFinding(code, message, path string, err error) domain.Finding {
	if err != nil {
		message += " " + err.Error()
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: map[string]any{"path": path},
	}
}

func semanticFindings(schemaName, source string, value any) []domain.Finding {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	switch schemaName {
	case "device":
		return deviceFindings(source, object)
	case "bundle-lock":
		return bundleLockFindings(source, object)
	case "compiled-policy":
		return compiledPolicyFindings(source, object)
	case "migration-registry":
		return migrationFindings(source, object)
	case "plan":
		return planFindings(source, object)
	case "rollout":
		return rolloutFindings(source, object)
	case "source-register":
		return sourceRegisterFindings(source, object)
	default:
		return nil
	}
}

func deviceFindings(source string, object map[string]any) []domain.Finding {
	findings := []domain.Finding{}
	workspaceRoots, workspaceRootsOK := object["workspace_roots"].(map[string]any)
	if !workspaceRootsOK {
		findings = append(findings, domain.Finding{
			Code: "GDS_DEVICE_WORKSPACE_ROOTS_INVALID", Severity: domain.SeverityHigh,
			Message:  "Device workspace_roots must be a mapping.",
			Evidence: map[string]any{"source": source},
		})
	}
	materialization, materializationOK := object["materialization"].(map[string]any)
	if !materializationOK {
		findings = append(findings, domain.Finding{
			Code: "GDS_DEVICE_MATERIALIZATION_INVALID", Severity: domain.SeverityHigh,
			Message:  "Device materialization must be a mapping.",
			Evidence: map[string]any{"source": source},
		})
		return findings
	}
	assignments, _ := materialization["include"].([]any)
	selectors := map[string]struct{}{}
	usedRoots := map[string]string{}
	for index, raw := range assignments {
		assignment, _ := raw.(map[string]any)
		selector, _ := assignment["selector"].(string)
		workspaceRoot, _ := assignment["workspace_root"].(string)
		if selector != "" {
			if _, duplicate := selectors[selector]; duplicate {
				findings = append(findings, domain.Finding{
					Code: "GDS_DEVICE_SELECTOR_DUPLICATE", Severity: domain.SeverityHigh,
					Message:  "Device materialization selectors must be unique.",
					Evidence: map[string]any{"source": source, "index": index, "selector": selector},
				})
			}
			selectors[selector] = struct{}{}
		}
		if workspaceRoot == "" {
			continue
		}
		if _, found := workspaceRoots[workspaceRoot]; !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_DEVICE_WORKSPACE_ROOT_UNKNOWN", Severity: domain.SeverityHigh,
				Message:  "Device materialization assignment references an unknown workspace root.",
				Evidence: map[string]any{"source": source, "index": index, "workspace_root": workspaceRoot},
			})
		}
		if prior, reused := usedRoots[workspaceRoot]; reused && prior != selector {
			findings = append(findings, domain.Finding{
				Code: "GDS_DEVICE_WORKSPACE_ROOT_REUSED", Severity: domain.SeverityHigh,
				Message: "One workspace root cannot host multiple materialization selectors.",
				Evidence: map[string]any{
					"source": source, "index": index, "workspace_root": workspaceRoot,
					"selector": selector, "previous_selector": prior,
				},
			})
		} else {
			usedRoots[workspaceRoot] = selector
		}
	}
	findings = append(findings, deviceClassFindings(source, object)...)
	return findings
}

// deviceClassFindings mirrors the platform/profile/gui/docker rules enforced by
// modules/macos-ubuntu-bootstrap/scripts/bootstrap.sh so a device descriptor and
// the OS installer it drives cannot disagree. Vocabulary comes from the
// rldyour-contract.json targets block. These rules only fire when a class block
// is present; a descriptor that omits it stays valid.
// deviceClassExecutionPolicies is the profile -> execution_policy mapping declared
// by the macos-ubuntu-bootstrap targets block in config/rldyour-contract.json.
// The contract is the single source of this vocabulary; TestDeviceClassContractParity
// asserts this map against it, so adding a profile in one place fails the build in
// the other rather than silently passing validation.
var deviceClassExecutionPolicies = map[string]string{
	"desktop":        "source-lsp-only",
	"desktop-builds": "local-dev-with-builds",
	"server":         "container-execution-only",
}

func deviceClassFindings(source string, object map[string]any) []domain.Finding {
	device, _ := object["device"].(map[string]any)
	classRaw, present := device["class"]
	if !present {
		return nil
	}
	class, _ := classRaw.(map[string]any)
	osName, _ := device["os"].(string)
	profile, _ := class["profile"].(string)
	gui, _ := class["gui"].(string)
	dockerMode, _ := class["docker_mode"].(string)
	executionPolicy, _ := class["execution_policy"].(string)
	hardening, hasHardening := class["hardening"]
	findings := []domain.Finding{}
	rule := func(code, message string, evidence map[string]any) {
		evidence["source"] = source
		findings = append(findings, domain.Finding{
			Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence,
		})
	}
	// macOS only supports the desktop profile and never local Docker.
	if osName == "macos" {
		if profile != "" && profile != "desktop" {
			rule("GDS_DEVICE_CLASS_MACOS_CONFLICT",
				"macOS only supports the desktop device class profile.",
				map[string]any{"os": osName, "profile": profile})
		}
		if dockerMode != "" && dockerMode != "none" {
			rule("GDS_DEVICE_CLASS_MACOS_CONFLICT",
				"macOS never installs local Docker; docker_mode must be none.",
				map[string]any{"os": osName, "docker_mode": dockerMode})
		}
	}
	// The server profile is always headless.
	if profile == "server" && gui != "" && gui != "disabled" {
		rule("GDS_DEVICE_CLASS_SERVER_GUI",
			"The server device class profile is always headless; gui must be disabled.",
			map[string]any{"profile": profile, "gui": gui})
	}
	// The desktop profile does not install Docker.
	// desktop-builds is the profile for local development with Docker.
	if profile == "desktop" && dockerMode != "" && dockerMode != "none" {
		rule("GDS_DEVICE_CLASS_DESKTOP_DOCKER",
			"The desktop device class profile does not install Docker; docker_mode must be none. Use profile desktop-builds for local Docker.",
			map[string]any{"profile": profile, "docker_mode": dockerMode})
	}
	if profile == "desktop-builds" && dockerMode != "" && dockerMode != "rootful" {
		rule("GDS_DEVICE_CLASS_DESKTOP_BUILDS_DOCKER",
			"The desktop-builds profile requires docker_mode rootful.",
			map[string]any{"profile": profile, "docker_mode": dockerMode})
	}
	// execution_policy, when declared, must match the profile. The mapping is the
	// macos-ubuntu-bootstrap targets block, mirrored here so a device descriptor
	// and the OS installer it drives cannot disagree.
	if executionPolicy != "" && profile != "" {
		expected := deviceClassExecutionPolicies[profile]
		if expected != "" && executionPolicy != expected {
			rule("GDS_DEVICE_CLASS_EXECUTION_POLICY",
				"Device class execution_policy must match the profile.",
				map[string]any{"profile": profile, "execution_policy": executionPolicy, "expected": expected})
		}
	}
	// Hardening toggles are server-only.
	if hasHardening && profile != "" && profile != "server" {
		if hardeningMap, ok := hardening.(map[string]any); ok && len(hardeningMap) > 0 {
			rule("GDS_DEVICE_CLASS_HARDENING_PROFILE",
				"Device class hardening is only permitted with the server profile.",
				map[string]any{"profile": profile, "hardening": hardening})
		}
	}
	return findings
}

func sourceRegisterFindings(source string, object map[string]any) []domain.Finding {
	findings := []domain.Finding{}
	seen := map[string]struct{}{}
	sources, _ := object["sources"].([]any)
	for index, rawSource := range sources {
		entry, _ := rawSource.(map[string]any)
		id, _ := entry["id"].(string)
		if _, duplicate := seen[id]; duplicate {
			findings = append(findings, domain.Finding{
				Code: "GDS_SOURCE_REGISTER_DUPLICATE_ID", Severity: domain.SeverityHigh,
				Message:  "Source register ids must be unique.",
				Evidence: map[string]any{"source": source, "id": id, "index": index},
			})
		}
		seen[id] = struct{}{}
		verifiedAt, _ := entry["verified_at"].(string)
		nextReview, _ := entry["next_review"].(string)
		if verifiedAt != "" && nextReview != "" && nextReview < verifiedAt {
			findings = append(findings, domain.Finding{
				Code: "GDS_SOURCE_REGISTER_REVIEW_ORDER_INVALID", Severity: domain.SeverityHigh,
				Message:  "Source review date must not precede its verification date.",
				Evidence: map[string]any{"source": source, "id": id, "index": index},
			})
		}
	}
	sortFindingsByCodeAndEvidence(findings)
	return findings
}

func rolloutFindings(source string, object map[string]any) []domain.Finding {
	findings := []domain.Finding{}
	waveIDs := map[string]struct{}{}
	targetIDs := map[string]struct{}{}
	targets := []string{}
	if waves, ok := object["waves"].([]any); ok {
		for index, rawWave := range waves {
			wave, ok := rawWave.(map[string]any)
			if !ok {
				continue
			}
			waveID, _ := wave["id"].(string)
			if _, duplicate := waveIDs[waveID]; duplicate {
				findings = append(findings, domain.Finding{
					Code: "GDS_ROLLOUT_WAVE_DUPLICATE", Severity: domain.SeverityHigh,
					Message:  "Rollout wave ids must be unique.",
					Evidence: map[string]any{"source": source, "wave_id": waveID},
				})
			}
			waveIDs[waveID] = struct{}{}
			ordinal, ordinalOK := integer(wave["ordinal"])
			if !ordinalOK || ordinal != int64(index) {
				findings = append(findings, domain.Finding{
					Code: "GDS_ROLLOUT_WAVE_ORDER_INVALID", Severity: domain.SeverityHigh,
					Message:  "Rollout wave ordinals must be contiguous and ordered.",
					Evidence: map[string]any{"source": source, "wave_id": waveID},
				})
			}
			if rawTargets, ok := wave["repository_ids"].([]any); ok {
				for _, rawTarget := range rawTargets {
					target, _ := rawTarget.(string)
					if _, duplicate := targetIDs[target]; duplicate {
						findings = append(findings, domain.Finding{
							Code: "GDS_ROLLOUT_DUPLICATE_TARGET", Severity: domain.SeverityHigh,
							Message:  "A rollout target occurs in more than one wave.",
							Evidence: map[string]any{"source": source, "repository_id": target},
						})
					}
					targetIDs[target] = struct{}{}
					targets = append(targets, target)
				}
			}
		}
	}
	sort.Strings(targets)
	targetDigest, err := canonicaljson.Digest(targets)
	targetCount, countOK := integer(object["target_count"])
	if err != nil || !countOK || targetCount != int64(len(targets)) || object["target_set_digest"] != targetDigest {
		findings = append(findings, domain.Finding{
			Code: "GDS_ROLLOUT_TARGET_SET_MISMATCH", Severity: domain.SeverityHigh,
			Message:  "Rollout target count or digest does not match its waves.",
			Evidence: map[string]any{"source": source, "expected_digest": targetDigest},
		})
	}
	expectedDigest, err := canonicaljson.DigestObjectWithoutField(object, "plan_digest")
	if err != nil || object["plan_digest"] != expectedDigest {
		findings = append(findings, domain.Finding{
			Code: "GDS_ROLLOUT_PLAN_DIGEST_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Rollout plan digest does not match its canonical payload.",
			Evidence: map[string]any{
				"source": source, "expected": expectedDigest, "observed": object["plan_digest"],
			},
		})
	}
	sortFindingsByCodeAndEvidence(findings)
	return findings
}

func compiledPolicyFindings(source string, object map[string]any) []domain.Finding {
	findings := []domain.Finding{}
	effective, _ := object["effective"].(map[string]any)
	provenance, _ := object["provenance"].(map[string]any)
	expected := map[string]struct{}{}
	collectLeafPointers(effective, []string{"effective"}, expected)
	for pointer := range expected {
		if _, found := provenance[pointer]; !found {
			findings = append(findings, domain.Finding{
				Code:     "GDS_COMPILED_POLICY_PROVENANCE_MISSING",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Compiled policy leaf %s has no provenance.", pointer),
				Evidence: map[string]any{"source": source, "pointer": pointer},
			})
		}
	}
	for pointer := range provenance {
		if _, found := expected[pointer]; !found {
			findings = append(findings, domain.Finding{
				Code:     "GDS_COMPILED_POLICY_PROVENANCE_ORPHAN",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Provenance %s does not identify an effective leaf.", pointer),
				Evidence: map[string]any{"source": source, "pointer": pointer},
			})
		}
	}

	sourceRefs := map[string]map[string]any{}
	previousSourceOrder := ""
	if sources, ok := object["sources"].([]any); ok {
		for index, rawSource := range sources {
			if policySource, ok := rawSource.(map[string]any); ok {
				if id, ok := policySource["id"].(string); ok {
					if _, duplicate := sourceRefs[id]; duplicate {
						findings = append(findings, domain.Finding{
							Code:     "GDS_COMPILED_POLICY_DUPLICATE_SOURCE",
							Severity: domain.SeverityHigh,
							Message:  fmt.Sprintf("Compiled policy source %q occurs more than once.", id),
							Evidence: map[string]any{"source": source, "id": id, "index": index},
						})
					}
					sourceRefs[id] = policySource
					priority, _ := integer(policySource["priority"])
					order := fmt.Sprintf(
						"%02d\x00%010d\x00%s", policyTierRank(fmt.Sprint(policySource["tier"])),
						priority, id,
					)
					if previousSourceOrder != "" && order <= previousSourceOrder {
						findings = append(findings, domain.Finding{
							Code:     "GDS_COMPILED_POLICY_SOURCE_ORDER_INVALID",
							Severity: domain.SeverityHigh,
							Message:  "Compiled policy sources are not in tier, priority, and id order.",
							Evidence: map[string]any{"source": source, "id": id, "index": index},
						})
					}
					previousSourceOrder = order
				}
			}
		}
	}
	for pointer, rawProvenance := range provenance {
		entry, ok := rawProvenance.(map[string]any)
		if !ok {
			continue
		}
		id, _ := entry["source"].(string)
		reference, found := sourceRefs[id]
		operation, _ := entry["operation"].(string)
		fileMatches := reference["path"] == entry["file"]
		if operation == "exception" {
			fileMatches = true
		}
		if !found || reference["tier"] != entry["tier"] ||
			fmt.Sprint(reference["priority"]) != fmt.Sprint(entry["priority"]) ||
			!fileMatches {
			findings = append(findings, domain.Finding{
				Code:     "GDS_COMPILED_POLICY_PROVENANCE_SOURCE_INVALID",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Provenance %s does not match a compiled source.", pointer),
				Evidence: map[string]any{"source": source, "pointer": pointer},
			})
		}
	}

	metadata, _ := object["compiled_policy"].(map[string]any)
	payload := map[string]any{
		"schema_version": object["schema_version"],
		"repository_id":  metadata["repository_id"],
		"bundle_version": metadata["bundle_version"],
		"sources":        object["sources"],
		"effective":      object["effective"],
		"provenance":     object["provenance"],
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		findings = append(findings, domain.Finding{
			Code:     "GDS_COMPILED_POLICY_DIGEST_UNCOMPUTABLE",
			Severity: domain.SeverityHigh,
			Message:  "Compiled policy digest cannot be computed from its payload.",
			Evidence: map[string]any{"source": source, "error": err.Error()},
		})
	} else {
		expectedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		if metadata["digest"] != expectedDigest {
			findings = append(findings, domain.Finding{
				Code:     "GDS_COMPILED_POLICY_DIGEST_MISMATCH",
				Severity: domain.SeverityHigh,
				Message:  "Compiled policy digest does not match its canonical payload.",
				Evidence: map[string]any{
					"source": source, "expected": expectedDigest,
					"observed": metadata["digest"],
				},
			})
		}
	}
	sortFindingsByCodeAndEvidence(findings)
	return findings
}

func policyTierRank(tier string) int {
	switch tier {
	case "base":
		return 0
	case "owner":
		return 1
	case "portfolio":
		return 2
	case "role":
		return 3
	case "stack":
		return 4
	case "lifecycle":
		return 5
	case "repository":
		return 6
	default:
		return 99
	}
}

func bundleLockFindings(source string, object map[string]any) []domain.Finding {
	projection, _ := object["projection"].(map[string]any)
	files, _ := projection["files"].([]any)
	seen := map[string]struct{}{}
	previous := ""
	findings := []domain.Finding{}
	for index, rawFile := range files {
		file, ok := rawFile.(map[string]any)
		if !ok {
			continue
		}
		path, _ := file["path"].(string)
		if _, duplicate := seen[path]; duplicate {
			findings = append(findings, domain.Finding{
				Code:     "GDS_BUNDLE_LOCK_DUPLICATE_PATH",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Bundle lock path %q occurs more than once.", path),
				Evidence: map[string]any{"source": source, "path": path, "index": index},
			})
		}
		seen[path] = struct{}{}
		if previous != "" && path <= previous {
			findings = append(findings, domain.Finding{
				Code:     "GDS_BUNDLE_LOCK_FILE_ORDER_INVALID",
				Severity: domain.SeverityHigh,
				Message:  "Bundle lock file paths must be unique and lexicographically ordered.",
				Evidence: map[string]any{"source": source, "path": path, "index": index},
			})
		}
		previous = path
	}
	raw, err := json.Marshal(files)
	if err == nil {
		expectedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		if projection["output_digest"] != expectedDigest {
			findings = append(findings, domain.Finding{
				Code:     "GDS_BUNDLE_LOCK_OUTPUT_DIGEST_MISMATCH",
				Severity: domain.SeverityHigh,
				Message:  "Bundle lock output digest does not match its ordered file list.",
				Evidence: map[string]any{
					"source": source, "expected": expectedDigest,
					"observed": projection["output_digest"],
				},
			})
		}
	}
	sortFindingsByCodeAndEvidence(findings)
	return findings
}

func collectLeafPointers(value any, parts []string, result map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			collectLeafPointers(child, append(parts, key), result)
		}
	case []any:
		for index, child := range typed {
			collectLeafPointers(child, append(parts, strconv.Itoa(index)), result)
		}
	default:
		result[jsonPointer(parts)] = struct{}{}
	}
}

func jsonPointer(parts []string) string {
	encoded := make([]string, len(parts))
	for index, part := range parts {
		encoded[index] = strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1")
	}
	return "/" + strings.Join(encoded, "/")
}

func sortFindingsByCodeAndEvidence(findings []domain.Finding) {
	sort.SliceStable(findings, func(left, right int) bool {
		if findings[left].Code != findings[right].Code {
			return findings[left].Code < findings[right].Code
		}
		return fmt.Sprint(findings[left].Evidence) < fmt.Sprint(findings[right].Evidence)
	})
}

func migrationFindings(source string, object map[string]any) []domain.Finding {
	migrations, _ := object["migrations"].([]any)
	seen := map[string]struct{}{}
	findings := []domain.Finding{}
	for index, rawMigration := range migrations {
		migration, ok := rawMigration.(map[string]any)
		if !ok {
			continue
		}
		id, _ := migration["id"].(string)
		if _, duplicate := seen[id]; id != "" && duplicate {
			findings = append(findings, domain.Finding{
				Code:     "GDS_MIGRATION_DUPLICATE_ID",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Duplicate migration id %q in %s.", id, source),
				Evidence: map[string]any{"source": source, "index": index, "id": id},
			})
		}
		seen[id] = struct{}{}
		from, fromOK := integer(migration["from"])
		to, toOK := integer(migration["to"])
		if fromOK && toOK && from >= to {
			findings = append(findings, domain.Finding{
				Code:     "GDS_MIGRATION_DIRECTION_INVALID",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Migration %q must increase schema version.", id),
				Evidence: map[string]any{
					"source": source, "index": index, "from": from, "to": to,
				},
			})
		}
	}
	return findings
}

func planFindings(source string, object map[string]any) []domain.Finding {
	findings := []domain.Finding{}
	createdRaw, createdOK := object["created_at"].(string)
	expiresRaw, expiresOK := object["expires_at"].(string)
	if createdOK && expiresOK {
		created, createdErr := time.Parse(time.RFC3339Nano, createdRaw)
		expires, expiresErr := time.Parse(time.RFC3339Nano, expiresRaw)
		if createdErr == nil && expiresErr == nil && !expires.After(created) {
			findings = append(findings, domain.Finding{
				Code:     "GDS_PLAN_EXPIRY_INVALID",
				Severity: domain.SeverityHigh,
				Message:  "Plan expiry must be later than creation time.",
				Evidence: map[string]any{
					"source": source, "created_at": createdRaw, "expires_at": expiresRaw,
				},
			})
		}
	}

	scopeIDs := map[string]struct{}{}
	if scope, ok := object["scope"].(map[string]any); ok {
		if repositories, ok := scope["repositories"].([]any); ok {
			for _, repository := range repositories {
				if id, ok := repository.(string); ok {
					scopeIDs[id] = struct{}{}
				}
			}
		}
	}
	preconditionCounts := map[string]int{}
	if preconditions, ok := object["preconditions"].([]any); ok {
		for _, rawPrecondition := range preconditions {
			if precondition, ok := rawPrecondition.(map[string]any); ok {
				if id, ok := precondition["repository_id"].(string); ok {
					preconditionCounts[id]++
				}
			}
		}
	}
	preconditionsMatch := len(preconditionCounts) == len(scopeIDs)
	for id := range scopeIDs {
		preconditionsMatch = preconditionsMatch && preconditionCounts[id] == 1
	}
	if len(scopeIDs) > 0 && !preconditionsMatch {
		findings = append(findings, domain.Finding{
			Code:     "GDS_PLAN_PRECONDITION_SCOPE_MISMATCH",
			Severity: domain.SeverityHigh,
			Message:  "Plan preconditions must cover every scoped repository once.",
			Evidence: map[string]any{"source": source},
		})
	}

	stepIDs := map[string]struct{}{}
	if steps, ok := object["steps"].([]any); ok {
		for index, rawStep := range steps {
			step, ok := rawStep.(map[string]any)
			if !ok {
				continue
			}
			stepID, _ := step["step_id"].(string)
			if _, duplicate := stepIDs[stepID]; stepID != "" && duplicate {
				findings = append(findings, domain.Finding{
					Code:     "GDS_PLAN_DUPLICATE_STEP_ID",
					Severity: domain.SeverityHigh,
					Message:  fmt.Sprintf("Duplicate plan step id %q.", stepID),
					Evidence: map[string]any{"source": source, "index": index},
				})
			}
			stepIDs[stepID] = struct{}{}
			repositoryID, _ := step["repository_id"].(string)
			if _, inScope := scopeIDs[repositoryID]; repositoryID != "" && !inScope {
				findings = append(findings, domain.Finding{
					Code:     "GDS_PLAN_STEP_OUTSIDE_SCOPE",
					Severity: domain.SeverityHigh,
					Message:  fmt.Sprintf("Plan step %q targets a repository outside scope.", stepID),
					Evidence: map[string]any{
						"source": source, "index": index, "repository_id": repositoryID,
					},
				})
			}
		}
	}
	expectedDigest, err := canonicaljson.DigestObjectWithoutField(object, "plan_digest")
	if err != nil || object["plan_digest"] != expectedDigest {
		findings = append(findings, domain.Finding{
			Code:     "GDS_PLAN_DIGEST_MISMATCH",
			Severity: domain.SeverityHigh,
			Message:  "Plan digest does not match its canonical payload.",
			Evidence: map[string]any{
				"source": source, "expected": expectedDigest, "observed": object["plan_digest"],
			},
		})
	}
	return findings
}

func integer(value any) (int64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(number.String(), 10, 64)
		return parsed, err == nil
	case int:
		return int64(number), true
	case int64:
		return number, true
	default:
		return 0, false
	}
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
