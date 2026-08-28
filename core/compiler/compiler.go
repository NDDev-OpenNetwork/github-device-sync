package compiler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

var policyTierOrder = map[string]int{
	"base": 0, "owner": 1, "portfolio": 2, "role": 3,
	"stack": 4, "lifecycle": 5, "repository": 6,
}

var monotonicStrength = map[string]map[string]int{
	"security.external_write_requires_approval":     {"false": 0, "true": 1},
	"security.public_projection_scan":               {"optional": 0, "required": 1},
	"context.private_parent_persistence":            {"ephemeral-only": 0, "forbidden": 1},
	"agent.generated_projection_edit":               {"warn": 0, "forbidden": 1},
	"security.secrets_in_repository":                {"forbidden": 1},
	"package_management.npm_family_on_managed_path": {"forbidden": 1},
	"package_management.mutable_version_resolution": {"forbidden": 1},
	"package_management.remote_stream_to_shell":     {"forbidden": 1},
}

type Compiler struct {
	schemas *validation.Set
	loader  *Loader
	Now     func() time.Time
	// owners resolves a provider login to its declared estate owner id. Empty
	// means unresolvable rather than "derive it", because deriving it is the
	// defect this field exists to remove.
	owners OwnerIdentities
}

func New(schemas *validation.Set) *Compiler {
	return &Compiler{schemas: schemas, loader: NewLoader(schemas), Now: time.Now}
}

func (compiler *Compiler) CompileDirectory(
	root string,
	anchor domain.RepositoryAnchor,
	bundleVersion string,
) CompileResult {
	sources, findings := compiler.loader.Load(root)
	if len(findings) != 0 {
		return CompileResult{Findings: findings}
	}
	owners, ownerFindings := compiler.loader.LoadOwners(root)
	if len(ownerFindings) != 0 {
		ownerDirectory := filepath.Join(root, "estate", "owners")
		_, ownerErr := os.Stat(ownerDirectory)
		if !(isPublicModuleAnchor(anchor) && os.IsNotExist(ownerErr) &&
			allFindingCodes(ownerFindings, "GDS_POLICY_OWNER_REGISTER_UNAVAILABLE")) {
			return CompileResult{Findings: ownerFindings}
		}
		owners = OwnerIdentities{}
	}
	compiler = compiler.WithOwners(owners)
	exceptions, exceptionFindings := compiler.loader.LoadExceptions(root)
	if len(exceptionFindings) != 0 {
		return CompileResult{Findings: exceptionFindings}
	}
	return compiler.CompileWithExceptions(
		anchor, sources, exceptions, bundleVersion, compiler.Now().UTC(),
	)
}

func isPublicModuleAnchor(anchor domain.RepositoryAnchor) bool {
	if anchor.Classification.VisibilityContract != "public" {
		return false
	}
	for _, role := range anchor.Repository.Roles {
		if role == "module" {
			return true
		}
	}
	return false
}

func allFindingCodes(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code != code {
			return false
		}
	}
	return len(findings) != 0
}

func (compiler *Compiler) Compile(
	anchor domain.RepositoryAnchor,
	available map[string]PolicySource,
	bundleVersion string,
) CompileResult {
	return compiler.CompileWithExceptions(
		anchor, available, nil, bundleVersion, time.Time{},
	)
}

func (compiler *Compiler) CompileWithExceptions(
	anchor domain.RepositoryAnchor,
	available map[string]PolicySource,
	exceptions []PolicyException,
	bundleVersion string,
	asOf time.Time,
) CompileResult {
	findings := []domain.Finding{}
	exceptionIndex, exceptionFindings := prepareExceptions(anchor, exceptions, asOf)
	findings = append(findings, exceptionFindings...)
	selected := make([]PolicySource, 0, len(anchor.Policy.Profiles))
	for _, id := range anchor.Policy.Profiles {
		source, found := available[id]
		if !found {
			findings = append(findings, domain.Finding{
				Code:     "GDS_POLICY_PROFILE_MISSING",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Selected policy profile %q does not exist.", id),
				Evidence: map[string]any{"profile": id},
			})
			continue
		}
		if reason := matchFailure(source.Match, anchor, compiler.owners); reason != "" {
			findings = append(findings, domain.Finding{
				Code:     "GDS_POLICY_PROFILE_NOT_APPLICABLE",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Selected policy profile %q is not applicable: %s.", id, reason),
				Evidence: map[string]any{"profile": id, "source": source.Path},
			})
			continue
		}
		if !distributionAllowed(
			source.Policy.Distribution, anchor.Classification.VisibilityContract,
		) {
			findings = append(findings, domain.Finding{
				Code:     "GDS_POLICY_VISIBILITY_VIOLATION",
				Severity: domain.SeverityCritical,
				Message: fmt.Sprintf(
					"Policy profile %q with %s distribution cannot compile for a %s repository.",
					id, source.Policy.Distribution, anchor.Classification.VisibilityContract,
				),
				Evidence: map[string]any{"profile": id, "source": source.Path},
			})
			continue
		}
		selected = append(selected, source)
	}
	if len(findings) != 0 {
		sortFindings(findings)
		return CompileResult{Findings: findings}
	}

	sort.Slice(selected, func(left, right int) bool {
		leftTier := policyTierOrder[selected[left].Policy.Tier]
		rightTier := policyTierOrder[selected[right].Policy.Tier]
		if leftTier != rightTier {
			return leftTier < rightTier
		}
		if selected[left].Policy.Priority != selected[right].Policy.Priority {
			return selected[left].Policy.Priority < selected[right].Policy.Priority
		}
		return selected[left].Policy.ID < selected[right].Policy.ID
	})

	state := mergeState{
		effective:       map[string]any{},
		provenance:      map[string]Provenance{},
		claims:          map[string]string{},
		monotonic:       map[string]struct{}{},
		exceptions:      exceptionIndex,
		exceptionsUsed:  map[string]bool{},
		profileSource:   map[string]Provenance{},
		profilePosition: map[string]int{},
	}
	for _, source := range selected {
		state.mergeSource(source, &findings)
	}
	findings = append(findings, state.unusedExceptionFindings()...)
	if len(findings) != 0 {
		sortFindings(findings)
		return CompileResult{Findings: findings}
	}
	state.finalizeProfiles()
	state.validateEffectivePolicy(&findings)
	if len(findings) != 0 {
		sortFindings(findings)
		return CompileResult{Findings: findings}
	}

	sourceRefs := make([]PolicySourceRef, 0, len(selected))
	for _, source := range selected {
		sourceRefs = append(sourceRefs, sourceRef(source))
	}
	document := CompiledPolicyDocument{
		SchemaVersion: domain.SchemaVersion,
		CompiledPolicy: CompiledPolicyMetadata{
			RepositoryID: anchor.Repository.ID, BundleVersion: bundleVersion,
		},
		Sources: sourceRefs, Effective: state.effective, Provenance: state.provenance,
	}
	digest, err := compiledDigest(document)
	if err != nil {
		return CompileResult{Findings: []domain.Finding{{
			Code: "GDS_POLICY_DIGEST_FAILED", Severity: domain.SeverityCritical,
			Message: fmt.Sprintf("Cannot compute compiled policy digest: %v", err),
		}}}
	}
	document.CompiledPolicy.Digest = digest

	raw, err := json.Marshal(document)
	if err != nil {
		return CompileResult{Findings: []domain.Finding{{
			Code: "GDS_POLICY_OUTPUT_INVALID", Severity: domain.SeverityCritical,
			Message: fmt.Sprintf("Cannot encode compiled policy: %v", err),
		}}}
	}
	value, err := serialization.Decode("compiled-policy.json", raw)
	if err != nil {
		return CompileResult{Findings: []domain.Finding{{
			Code: "GDS_POLICY_OUTPUT_INVALID", Severity: domain.SeverityCritical,
			Message: fmt.Sprintf("Cannot decode compiled policy: %v", err),
		}}}
	}
	if schemaFindings := compiler.schemas.Validate(
		"compiled-policy", value, "compiled-policy.json",
	); len(schemaFindings) != 0 {
		return CompileResult{Findings: schemaFindings}
	}
	return CompileResult{Document: document, Findings: []domain.Finding{}}
}

type mergeState struct {
	effective       map[string]any
	provenance      map[string]Provenance
	claims          map[string]string
	monotonic       map[string]struct{}
	exceptions      map[string]PolicyException
	exceptionsUsed  map[string]bool
	profiles        []string
	profileSource   map[string]Provenance
	profilePosition map[string]int
}

func (state *mergeState) mergeSource(source PolicySource, findings *[]domain.Finding) {
	state.mergeObject(nil, source.Apply, source, findings)
	for _, path := range source.Constraints.Monotonic {
		if _, supported := monotonicStrength[path]; !supported {
			*findings = append(*findings, policyFinding(
				"GDS_POLICY_MONOTONIC_PATH_UNSUPPORTED", source,
				fmt.Sprintf("Monotonic path %q is not supported", path), path,
			))
			continue
		}
		if _, exists := lookupPath(source.Apply, strings.Split(path, ".")); !exists {
			*findings = append(*findings, policyFinding(
				"GDS_POLICY_MONOTONIC_SOURCE_MISSING", source,
				fmt.Sprintf("Monotonic path %q is not set by its declaring policy", path), path,
			))
			continue
		}
		state.monotonic[path] = struct{}{}
	}
}

func (state *mergeState) mergeObject(
	prefix []string,
	object map[string]any,
	source PolicySource,
	findings *[]domain.Finding,
) {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := append(append([]string{}, prefix...), key)
		value := object[key]
		if strings.Join(path, ".") == "agent.profiles" {
			state.mergeProfiles(value, source, findings)
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			state.mergeObject(path, nested, source, findings)
			continue
		}
		state.setLeaf(path, value, source, findings)
	}
}

func (state *mergeState) setLeaf(
	parts []string,
	value any,
	source PolicySource,
	findings *[]domain.Finding,
) {
	path := strings.Join(parts, ".")
	if state.claimed(path, source, findings) {
		return
	}
	if previous, exists := lookupPath(state.effective, parts); exists {
		if _, protected := state.monotonic[path]; protected && isWeakening(path, previous, value) {
			exception, allowed := state.exceptions[path]
			if !allowed || !reflect.DeepEqual(exception.Exception.RequestedValue, value) {
				*findings = append(*findings, policyFinding(
					"GDS_POLICY_MONOTONIC_WEAKENING", source,
					fmt.Sprintf("Policy attempts to weaken monotonic path %q", path), path,
				))
				return
			}
			setPath(state.effective, parts, value)
			state.replaceProvenance(parts, value, provenanceForException(source, exception))
			state.exceptionsUsed[path] = true
			return
		}
	}
	setPath(state.effective, parts, value)
	state.replaceProvenance(parts, value, provenanceFor(source, "set"))
}

func (state *mergeState) replaceProvenance(parts []string, value any, provenance Provenance) {
	pointer := jsonPointer(parts)
	for existing := range state.provenance {
		if existing == pointer || strings.HasPrefix(existing, pointer+"/") {
			delete(state.provenance, existing)
		}
	}
	state.recordLeafProvenance(parts, value, provenance)
}

func (state *mergeState) recordLeafProvenance(
	parts []string,
	value any,
	provenance Provenance,
) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			state.recordLeafProvenance(
				append(append([]string{}, parts...), key), typed[key], provenance,
			)
		}
	case []any:
		for index, item := range typed {
			state.recordLeafProvenance(
				append(append([]string{}, parts...), fmt.Sprint(index)), item, provenance,
			)
		}
	case []string:
		for index, item := range typed {
			state.recordLeafProvenance(
				append(append([]string{}, parts...), fmt.Sprint(index)), item, provenance,
			)
		}
	default:
		state.provenance[jsonPointer(parts)] = provenance
	}
}

func (state *mergeState) unusedExceptionFindings() []domain.Finding {
	findings := []domain.Finding{}
	paths := make([]string, 0, len(state.exceptions))
	for path := range state.exceptions {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if state.exceptionsUsed[path] {
			continue
		}
		exception := state.exceptions[path]
		findings = append(findings, domain.Finding{
			Code: "GDS_POLICY_EXCEPTION_UNUSED", Severity: domain.SeverityHigh,
			Message: "A scoped policy exception did not authorize an actual monotonic weakening.",
			Evidence: map[string]any{
				"exception_id":  exception.Exception.ID,
				"repository_id": exception.Exception.RepositoryID,
				"path":          path,
				"file":          exception.Path,
			},
		})
	}
	return findings
}

func (state *mergeState) mergeProfiles(
	value any,
	source PolicySource,
	findings *[]domain.Finding,
) {
	path := "agent.profiles"
	if state.claimed(path, source, findings) {
		return
	}
	mutation, ok := value.(map[string]any)
	if !ok {
		*findings = append(*findings, policyFinding(
			"GDS_POLICY_LIST_MUTATION_INVALID", source,
			"Agent profiles must use explicit append/remove operations", path,
		))
		return
	}
	appendValues, appendOK := stringValues(mutation["append"])
	removeValues, removeOK := stringValues(mutation["remove"])
	if !appendOK || !removeOK {
		*findings = append(*findings, policyFinding(
			"GDS_POLICY_LIST_MUTATION_INVALID", source,
			"Agent profile mutations must contain string arrays", path,
		))
		return
	}
	overlap := stringIntersection(appendValues, removeValues)
	if len(overlap) != 0 {
		*findings = append(*findings, policyFinding(
			"GDS_POLICY_LIST_MUTATION_CONFLICT", source,
			fmt.Sprintf("Agent profiles occur in append and remove: %s", strings.Join(overlap, ", ")),
			path,
		))
		return
	}
	for _, profile := range removeValues {
		state.removeProfile(profile)
	}
	for _, profile := range appendValues {
		if _, exists := state.profilePosition[profile]; exists {
			continue
		}
		state.profiles = append(state.profiles, profile)
		state.reindexProfiles()
		state.profileSource[profile] = provenanceFor(source, "append")
	}
}

func (state *mergeState) claimed(
	path string,
	source PolicySource,
	findings *[]domain.Finding,
) bool {
	key := fmt.Sprintf("%s\x00%d\x00%s", source.Policy.Tier, source.Policy.Priority, path)
	if previous, conflict := state.claims[key]; conflict && previous != source.Policy.ID {
		*findings = append(*findings, policyFinding(
			"GDS_POLICY_SAME_TIER_CONFLICT", source,
			fmt.Sprintf(
				"Policies %q and %q set %q at the same tier and priority",
				previous, source.Policy.ID, path,
			), path,
		))
		return true
	}
	state.claims[key] = source.Policy.ID
	return false
}

func (state *mergeState) removeProfile(profile string) {
	position, exists := state.profilePosition[profile]
	if !exists {
		return
	}
	state.profiles = append(state.profiles[:position], state.profiles[position+1:]...)
	delete(state.profileSource, profile)
	state.reindexProfiles()
}

func (state *mergeState) reindexProfiles() {
	state.profilePosition = map[string]int{}
	for index, profile := range state.profiles {
		state.profilePosition[profile] = index
	}
}

func (state *mergeState) finalizeProfiles() {
	if len(state.profiles) == 0 {
		return
	}
	agent, ok := state.effective["agent"].(map[string]any)
	if !ok {
		agent = map[string]any{}
		state.effective["agent"] = agent
	}
	values := append([]string(nil), state.profiles...)
	agent["profiles"] = values
	for index, profile := range values {
		state.provenance[fmt.Sprintf("/effective/agent/profiles/%d", index)] =
			state.profileSource[profile]
	}
}

func (state *mergeState) validateEffectivePolicy(findings *[]domain.Finding) {
	allowedValue, allowedFound := lookupPath(
		state.effective, []string{"github", "actions", "allowed_actions"},
	)
	selectedValue, selectedFound := lookupPath(
		state.effective, []string{"github", "actions", "selected_actions"},
	)
	if !allowedFound && !selectedFound {
		return
	}
	allowed, allowedOK := allowedValue.(map[string]any)
	selected, selectedOK := selectedValue.(map[string]any)
	allowedManagedSelected := allowedOK && allowed["management"] == "managed" &&
		allowed["value"] == "selected"
	selectedManaged := selectedOK && selected["management"] == "managed"
	if allowedManagedSelected == selectedManaged {
		return
	}
	*findings = append(*findings, domain.Finding{
		Code:     "GDS_POLICY_GITHUB_SELECTED_ACTIONS_INCONSISTENT",
		Severity: domain.SeverityHigh,
		Message: "Managed selected Actions require one complete managed selected-actions value, " +
			"and that value is invalid for every other allowed-actions mode.",
		Evidence: map[string]any{
			"allowed_actions_managed_selected": allowedManagedSelected,
			"selected_actions_managed":         selectedManaged,
		},
	})
}

func matchFailure(
	match PolicyMatch,
	anchor domain.RepositoryAnchor,
	owners OwnerIdentities,
) string {
	if match.Owner != "" {
		// Resolved through the estate register, never synthesised from the
		// provider login. Lowercasing the login produced an id that happened to
		// be right for one owner of five, so four could never be named by a
		// policy at all -- and the failure was silent, because a profile whose
		// match cannot be satisfied is skipped rather than rejected.
		declared, resolved := owners[strings.ToLower(anchor.Provider.Owner)]
		if !resolved {
			return fmt.Sprintf(
				"no estate owner declares provider login %q, so owner %q cannot be resolved",
				anchor.Provider.Owner, match.Owner,
			)
		}
		if match.Owner != declared {
			return fmt.Sprintf("owner %q does not match %q", match.Owner, declared)
		}
	}
	if len(match.Roles) != 0 && !intersects(match.Roles, anchor.Repository.Roles) {
		return "repository roles do not match"
	}
	if len(match.Portfolios) != 0 && !intersects(match.Portfolios, anchor.Classification.Portfolios) {
		return "repository portfolios do not match"
	}
	if len(match.VisibilityContract) != 0 &&
		!contains(match.VisibilityContract, anchor.Classification.VisibilityContract) {
		return "visibility contract does not match"
	}
	if len(match.Lifecycle) != 0 && !contains(match.Lifecycle, anchor.Repository.Lifecycle) {
		return "lifecycle does not match"
	}
	return ""
}

func sourceRef(source PolicySource) PolicySourceRef {
	return PolicySourceRef{
		ID: source.Policy.ID, Tier: source.Policy.Tier, Priority: source.Policy.Priority,
		Distribution: source.Policy.Distribution, Path: source.Path, Digest: source.Digest,
	}
}

func distributionAllowed(distribution, visibility string) bool {
	rank := map[string]int{"public": 0, "internal": 1, "private": 2}
	distributionRank, distributionFound := rank[distribution]
	visibilityRank, visibilityFound := rank[visibility]
	return distributionFound && visibilityFound && distributionRank <= visibilityRank
}

func provenanceFor(source PolicySource, operation string) Provenance {
	return Provenance{
		Source: source.Policy.ID, Tier: source.Policy.Tier,
		Priority: source.Policy.Priority, File: source.Path, Operation: operation,
	}
}

func provenanceForException(source PolicySource, exception PolicyException) Provenance {
	return Provenance{
		Source: source.Policy.ID, Tier: source.Policy.Tier,
		Priority: source.Policy.Priority, File: exception.Path, Operation: "exception",
		ExceptionID:     exception.Exception.ID,
		ApprovalRef:     exception.Exception.OwnerApprovalRef,
		ExpiresAt:       exception.Exception.ExpiresAt,
		ExceptionDigest: exception.Digest,
	}
}

func prepareExceptions(
	anchor domain.RepositoryAnchor,
	exceptions []PolicyException,
	asOf time.Time,
) (map[string]PolicyException, []domain.Finding) {
	indexed := map[string]PolicyException{}
	findings := []domain.Finding{}
	for _, exception := range exceptions {
		metadata := exception.Exception
		if metadata.RepositoryID != anchor.Repository.ID {
			continue
		}
		if _, supported := monotonicStrength[metadata.PolicyPath]; !supported {
			findings = append(findings, domain.Finding{
				Code: "GDS_POLICY_EXCEPTION_PATH_UNSUPPORTED", Severity: domain.SeverityHigh,
				Message: "Policy exception targets a non-monotonic or unsupported path.",
				Evidence: map[string]any{
					"exception_id": metadata.ID, "path": metadata.PolicyPath,
					"file": exception.Path,
				},
			})
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339, metadata.ExpiresAt)
		if err != nil {
			findings = append(findings, domain.Finding{
				Code: "GDS_POLICY_EXCEPTION_EXPIRY_INVALID", Severity: domain.SeverityHigh,
				Message:  "Policy exception expiry is not a valid RFC 3339 timestamp.",
				Evidence: map[string]any{"exception_id": metadata.ID, "file": exception.Path},
			})
			continue
		}
		if !asOf.IsZero() && !asOf.Before(expiresAt) {
			findings = append(findings, domain.Finding{
				Code: "GDS_POLICY_EXCEPTION_EXPIRED", Severity: domain.SeverityHigh,
				Message: "Policy exception has expired and cannot authorize a weakening.",
				Evidence: map[string]any{
					"exception_id": metadata.ID, "expires_at": metadata.ExpiresAt,
					"file": exception.Path,
				},
			})
			continue
		}
		if previous, duplicate := indexed[metadata.PolicyPath]; duplicate {
			findings = append(findings, domain.Finding{
				Code: "GDS_POLICY_EXCEPTION_DUPLICATE_SCOPE", Severity: domain.SeverityHigh,
				Message: "More than one exception targets the same repository policy path.",
				Evidence: map[string]any{
					"path": metadata.PolicyPath, "first": previous.Path, "second": exception.Path,
				},
			})
			continue
		}
		indexed[metadata.PolicyPath] = exception
	}
	sortFindings(findings)
	return indexed, findings
}

func compiledDigest(document CompiledPolicyDocument) (string, error) {
	payload := map[string]any{
		"schema_version": document.SchemaVersion,
		"repository_id":  document.CompiledPolicy.RepositoryID,
		"bundle_version": document.CompiledPolicy.BundleVersion,
		"sources":        document.Sources,
		"effective":      document.Effective,
		"provenance":     document.Provenance,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	normalized, err := serialization.Decode("compiled-policy-digest.json", raw)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(canonical)), nil
}

func lookupPath(root map[string]any, parts []string) (any, bool) {
	var current any = root
	for _, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setPath(root map[string]any, parts []string, value any) {
	current := root
	for _, part := range parts[:len(parts)-1] {
		nested, ok := current[part].(map[string]any)
		if !ok {
			nested = map[string]any{}
			current[part] = nested
		}
		current = nested
	}
	current[parts[len(parts)-1]] = value
}

func jsonPointer(parts []string) string {
	encoded := make([]string, len(parts)+1)
	encoded[0] = "effective"
	for index, part := range parts {
		encoded[index+1] = strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1")
	}
	return "/" + strings.Join(encoded, "/")
}

func isWeakening(path string, previous, next any) bool {
	strengths := monotonicStrength[path]
	if len(strengths) == 0 {
		return false
	}
	previousStrength, previousOK := strengths[fmt.Sprint(previous)]
	nextStrength, nextOK := strengths[fmt.Sprint(next)]
	if !previousOK || !nextOK {
		return true
	}
	return nextStrength < previousStrength
}

func stringValues(value any) ([]string, bool) {
	if value == nil {
		return []string{}, true
	}
	raw, ok := value.([]any)
	if !ok {
		if typed, typedOK := value.([]string); typedOK {
			return append([]string(nil), typed...), true
		}
		return nil, false
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func stringIntersection(left, right []string) []string {
	rightSet := map[string]struct{}{}
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	result := []string{}
	for _, value := range left {
		if _, found := rightSet[value]; found {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func intersects(left, right []string) bool {
	for _, value := range left {
		if contains(right, value) {
			return true
		}
	}
	return false
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func policyFinding(code string, source PolicySource, message, path string) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message + ".",
		Evidence: map[string]any{
			"source": source.Policy.ID, "file": source.Path, "path": path,
		},
	}
}
