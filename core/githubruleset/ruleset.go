// Package githubruleset reconciles one closed default-branch repository ruleset.
package githubruleset

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

const Action = "github-default-branch-ruleset"

type PrivilegedReader interface {
	GetRepositoryRuleset(context.Context, int64) (githubprovider.RepositoryRulesetState, githubprovider.ResponseMeta, error)
	ListRepositoryRulesets(context.Context) ([]githubprovider.RulesetSummary, githubprovider.ResponseMeta, error)
}

type Writer interface {
	Scope() githubprovider.RepositoryMutationScope
	UpsertDefaultBranchRuleset(context.Context, githubprovider.RepositoryRuleset, *githubprovider.RepositoryRulesetState) (githubprovider.RulesetSummary, githubprovider.MutationMeta, error)
}

type Scope struct {
	ReadInstallationID   string `json:"read_installation_id"`
	MutationCapabilityID string `json:"mutation_capability_id"`
	ProviderRepositoryID int64  `json:"provider_repository_id"`
	Owner                string `json:"owner"`
	Name                 string `json:"name"`
}

type Parameters struct {
	Scope    Scope                                  `json:"scope"`
	Expected *githubprovider.RepositoryRulesetState `json:"expected,omitempty"`
	Desired  githubprovider.RepositoryRuleset       `json:"desired"`
}

type Evidence struct {
	State      githubprovider.RepositoryRulesetState `json:"state"`
	Digest     string                                `json:"digest"`
	Mutation   *githubprovider.MutationMeta          `json:"mutation,omitempty"`
	Idempotent bool                                  `json:"idempotent"`
}

type Handler struct {
	Reader PrivilegedReader
	Writer Writer
	Scope  githubprovider.RepositoryMutationScope
}

func OperationParameters(value Parameters) map[string]any {
	return map[string]any{"github_ruleset": value}
}

func StepParameters(step operations.Step) (Parameters, error) {
	if len(step.Parameters) != 1 {
		return Parameters{}, errors.New("GitHub ruleset step must contain one parameter domain")
	}
	raw, found := step.Parameters["github_ruleset"]
	if !found {
		return Parameters{}, errors.New("github_ruleset parameters are missing")
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return Parameters{}, fmt.Errorf("encode GitHub ruleset parameters: %w", err)
	}
	var value Parameters
	if err := json.Unmarshal(payload, &value); err != nil {
		return Parameters{}, fmt.Errorf("decode GitHub ruleset parameters: %w", err)
	}
	value.Desired, err = normalizeDesired(value.Desired)
	if err != nil {
		return Parameters{}, err
	}
	if value.Expected != nil {
		expected, normalizeErr := normalizeState(*value.Expected)
		if normalizeErr != nil || expected.BypassActorsKnown || len(expected.BypassActors) != 0 || expected.WritableDigest == "" {
			return Parameters{}, errors.New("planned GitHub ruleset evidence must omit privileged bypass actors")
		}
		if expected.SourceType != "Repository" ||
			!strings.EqualFold(expected.Source, value.Scope.Owner+"/"+value.Scope.Name) {
			return Parameters{}, errors.New("planned GitHub ruleset source differs from repository scope")
		}
		value.Expected = &expected
		if value.Desired.ID != expected.ID {
			return Parameters{}, errors.New("GitHub ruleset update must preserve the ruleset identity")
		}
	} else if value.Desired.ID != 0 {
		return Parameters{}, errors.New("GitHub ruleset creation cannot predeclare a provider ID")
	}
	if step.Action != Action || value.Scope.ReadInstallationID == "" ||
		value.Scope.MutationCapabilityID == "" || value.Scope.ProviderRepositoryID <= 0 ||
		value.Scope.Owner == "" || value.Scope.Name == "" {
		return Parameters{}, errors.New("GitHub ruleset parameters are invalid")
	}
	return value, nil
}

func (handler *Handler) Apply(ctx context.Context, step operations.Step) (operations.ApplyEvidence, error) {
	parameters, err := StepParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if err := handler.validateBinding(parameters.Scope, true); err != nil {
		return operations.ApplyEvidence{}, err
	}
	before, exists, err := handler.observe(ctx, parameters)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if exists && (!before.BypassActorsKnown || len(before.WritablePayload) == 0) {
		return operations.ApplyEvidence{}, errors.New(
			"GitHub ruleset full writable state is hidden; ordinary upsert is blocked",
		)
	}
	if exists && ownedStateEqual(before, parameters.Desired) {
		evidence, err := stateEvidence(before, nil, true)
		return operations.ApplyEvidence{Before: evidence, After: evidence}, err
	}
	if parameters.Expected == nil {
		if exists {
			return operations.ApplyEvidence{}, errors.New("GitHub ruleset appeared after planning; mutation was not attempted")
		}
	} else if !exists || !reflect.DeepEqual(visibleState(before), *parameters.Expected) {
		beforeEvidence, _ := stateEvidence(before, nil, false)
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub ruleset state changed after planning; mutation was not attempted",
		)
	}
	beforeEvidence := Evidence{}
	if exists {
		beforeEvidence, err = stateEvidence(before, nil, false)
		if err != nil {
			return operations.ApplyEvidence{}, err
		}
	}
	var preserved *githubprovider.RepositoryRulesetState
	if exists {
		preserved = &before
	}
	summary, meta, err := handler.Writer.UpsertDefaultBranchRuleset(ctx, parameters.Desired, preserved)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	after, _, err := handler.observeByID(ctx, parameters, summary.ID)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	expectedAfter := desiredState(parameters.Scope, parameters.Desired, summary.ID)
	if exists {
		expectedAfter = applyOwnedState(before, parameters.Desired)
	}
	exact := reflect.DeepEqual(after, expectedAfter)
	if !exists {
		exact = createdStateEqual(after, expectedAfter)
	}
	if !exact {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub ruleset mutation completed without the exact planned state",
		)
	}
	afterEvidence, err := stateEvidence(after, &meta, false)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	return operations.ApplyEvidence{Before: beforeEvidence, After: afterEvidence}, nil
}

func createdStateEqual(observed, expected githubprovider.RepositoryRulesetState) bool {
	return observed.ID == expected.ID && observed.Name == expected.Name && observed.Target == expected.Target &&
		observed.SourceType == expected.SourceType && strings.EqualFold(observed.Source, expected.Source) &&
		observed.BypassActorsKnown && len(observed.BypassActors) == 0 &&
		reflect.DeepEqual(observed.ConditionIncludes, expected.ConditionIncludes) &&
		reflect.DeepEqual(observed.ConditionExcludes, expected.ConditionExcludes) &&
		ownedStateEqual(observed, githubprovider.RepositoryRuleset{Enforcement: expected.Enforcement, Rules: expected.Rules})
}

func (handler *Handler) Verify(ctx context.Context, step operations.Step, recorded json.RawMessage) error {
	parameters, err := StepParameters(step)
	if err != nil {
		return err
	}
	if err := handler.validateBinding(parameters.Scope, false); err != nil {
		return err
	}
	var evidence Evidence
	if err := json.Unmarshal(recorded, &evidence); err != nil {
		return fmt.Errorf("decode recorded GitHub ruleset evidence: %w", err)
	}
	evidence.State, err = normalizeState(evidence.State)
	if err != nil || !evidence.State.BypassActorsKnown || len(evidence.State.WritablePayload) == 0 {
		return errors.New("recorded GitHub ruleset evidence is invalid")
	}
	digest, err := canonicaljson.Digest(evidence.State)
	if err != nil || digest != evidence.Digest {
		return errors.New("recorded GitHub ruleset digest is invalid")
	}
	current, _, err := handler.observeByID(ctx, parameters, evidence.State.ID)
	if err != nil || !reflect.DeepEqual(current, evidence.State) ||
		!ownedStateEqual(current, parameters.Desired) {
		return errors.New("current GitHub ruleset differs from verified operation evidence")
	}
	return nil
}

func (handler *Handler) observe(
	ctx context.Context,
	parameters Parameters,
) (githubprovider.RepositoryRulesetState, bool, error) {
	if parameters.Expected != nil {
		return handler.observeByID(ctx, parameters, parameters.Expected.ID)
	}
	summaries, _, err := handler.Reader.ListRepositoryRulesets(ctx)
	if err != nil {
		return githubprovider.RepositoryRulesetState{}, false, err
	}
	for _, summary := range summaries {
		if summary.SourceType == "Repository" && strings.EqualFold(summary.Source, parameters.Scope.Owner+"/"+parameters.Scope.Name) &&
			summary.Name == parameters.Desired.Name {
			state, _, stateErr := handler.observeByID(ctx, parameters, summary.ID)
			return state, stateErr == nil, stateErr
		}
	}
	return githubprovider.RepositoryRulesetState{}, false, nil
}

func (handler *Handler) observeByID(
	ctx context.Context,
	_ Parameters,
	rulesetID int64,
) (githubprovider.RepositoryRulesetState, bool, error) {
	state, _, err := handler.Reader.GetRepositoryRuleset(ctx, rulesetID)
	if err != nil {
		return githubprovider.RepositoryRulesetState{}, false, err
	}
	normalized, err := normalizeState(state)
	return normalized, err == nil, err
}

func (handler *Handler) validateBinding(scope Scope, requireWriter bool) error {
	if handler == nil || handler.Reader == nil {
		return errors.New("GitHub ruleset handler binding is incomplete")
	}
	bound := handler.Scope
	if handler.Writer != nil {
		writerScope := handler.Writer.Scope()
		if bound.RepositoryID != 0 && !reflect.DeepEqual(bound, writerScope) {
			return errors.New("GitHub ruleset handler and writer scopes differ")
		}
		bound = writerScope
	} else if requireWriter {
		return errors.New("GitHub ruleset mutation writer is unavailable")
	}
	if bound.RepositoryID != scope.ProviderRepositoryID ||
		!strings.EqualFold(bound.Owner, scope.Owner) || !strings.EqualFold(bound.Name, scope.Name) {
		return errors.New("GitHub ruleset writer identity differs from the immutable plan")
	}
	return nil
}

func normalizeDesired(value githubprovider.RepositoryRuleset) (githubprovider.RepositoryRuleset, error) {
	if value.ID < 0 || value.Name == "" || len(value.Name) > 256 || strings.ContainsAny(value.Name, "\x00\r\n") ||
		(value.Target != "" && value.Target != "branch") ||
		(value.Enforcement != "" && value.Enforcement != "active") ||
		len(value.Rules) == 0 || len(value.Rules) > 32 {
		return githubprovider.RepositoryRuleset{}, errors.New("desired GitHub ruleset is invalid")
	}
	value.Target = "branch"
	value.Enforcement = "active"
	value.Rules = append([]githubprovider.RulesetRule(nil), value.Rules...)
	seen := map[string]struct{}{}
	for index := range value.Rules {
		rule := &value.Rules[index]
		if _, duplicate := seen[rule.Type]; duplicate {
			return githubprovider.RepositoryRuleset{}, errors.New("desired GitHub ruleset repeats a rule")
		}
		seen[rule.Type] = struct{}{}
		switch rule.Type {
		case "deletion", "non_fast_forward", "required_linear_history", "required_signatures":
			if rule.RequiredApprovingReviewCount != 0 || len(rule.RequiredStatusChecks) != 0 {
				return githubprovider.RepositoryRuleset{}, errors.New("parameterless GitHub ruleset rule has parameters")
			}
		case "pull_request":
			if rule.RequiredApprovingReviewCount < 0 || rule.RequiredApprovingReviewCount > 6 ||
				len(rule.RequiredStatusChecks) != 0 {
				return githubprovider.RepositoryRuleset{}, errors.New("GitHub pull-request rule is invalid")
			}
			if len(rule.AllowedMergeMethods) == 0 {
				rule.AllowedMergeMethods = []string{"merge", "rebase", "squash"}
			}
			methods := append([]string(nil), rule.AllowedMergeMethods...)
			sort.Strings(methods)
			for methodIndex, method := range methods {
				if (method != "merge" && method != "rebase" && method != "squash") ||
					(methodIndex > 0 && methods[methodIndex-1] == method) {
					return githubprovider.RepositoryRuleset{}, errors.New("GitHub pull-request merge methods are invalid")
				}
			}
			rule.AllowedMergeMethods = methods
		case "required_status_checks":
			if len(rule.RequiredStatusChecks) == 0 || len(rule.RequiredStatusChecks) > 50 ||
				rule.RequiredApprovingReviewCount != 0 {
				return githubprovider.RepositoryRuleset{}, errors.New("GitHub status-check rule is invalid")
			}
			checks := append([]githubprovider.RequiredStatusCheck(nil), rule.RequiredStatusChecks...)
			sort.Slice(checks, func(left, right int) bool {
				if checks[left].Context != checks[right].Context {
					return checks[left].Context < checks[right].Context
				}
				return checks[left].IntegrationID < checks[right].IntegrationID
			})
			for checkIndex, check := range checks {
				if check.Context == "" || len(check.Context) > 256 || strings.ContainsAny(check.Context, "\x00\r\n") ||
					check.IntegrationID < 0 || (checkIndex > 0 && checks[checkIndex-1] == check) {
					return githubprovider.RepositoryRuleset{}, errors.New("GitHub required status checks are invalid")
				}
			}
			rule.RequiredStatusChecks = checks
		default:
			return githubprovider.RepositoryRuleset{}, fmt.Errorf("GitHub ruleset rule %q is unsupported", rule.Type)
		}
	}
	sort.Slice(value.Rules, func(left, right int) bool { return value.Rules[left].Type < value.Rules[right].Type })
	return value, nil
}

func normalizeState(value githubprovider.RepositoryRulesetState) (githubprovider.RepositoryRulesetState, error) {
	if value.ID <= 0 || value.Name == "" || value.Target != "branch" || value.SourceType != "Repository" ||
		value.Source == "" || (value.Enforcement != "active" && value.Enforcement != "disabled" && value.Enforcement != "evaluate") || len(value.ConditionIncludes) == 0 ||
		len(value.Rules) == 0 {
		return githubprovider.RepositoryRulesetState{}, errors.New("GitHub ruleset state is invalid")
	}
	includes := make([]string, len(value.ConditionIncludes))
	copy(includes, value.ConditionIncludes)
	excludes := make([]string, len(value.ConditionExcludes))
	copy(excludes, value.ConditionExcludes)
	value.ConditionIncludes = includes
	value.ConditionExcludes = excludes
	sort.Strings(value.ConditionIncludes)
	sort.Strings(value.ConditionExcludes)
	bypassActors := make([]githubprovider.RulesetBypassActor, len(value.BypassActors))
	copy(bypassActors, value.BypassActors)
	value.BypassActors = bypassActors
	if len(value.WritablePayload) != 0 {
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(value.WritablePayload))
		if value.WritableDigest != "" && value.WritableDigest != digest {
			return githubprovider.RepositoryRulesetState{}, errors.New("GitHub ruleset writable-state digest is invalid")
		}
		value.WritableDigest = digest
	}
	knownRules := make([]githubprovider.RulesetRule, 0, len(value.Rules))
	opaqueRules := make([]githubprovider.RulesetRule, 0, len(value.Rules))
	for _, rule := range value.Rules {
		if rule.OpaqueParameters != nil {
			if rule.Type == "" || len(rule.Type) > 128 || !json.Valid(rule.OpaqueParameters) {
				return githubprovider.RepositoryRulesetState{}, errors.New("opaque GitHub ruleset rule is invalid")
			}
			opaqueRules = append(opaqueRules, rule)
			continue
		}
		knownRules = append(knownRules, rule)
	}
	desired, err := normalizeDesired(githubprovider.RepositoryRuleset{
		ID: value.ID, Name: value.Name, Target: value.Target,
		Enforcement: "active", Rules: knownRules,
	})
	if err != nil {
		return githubprovider.RepositoryRulesetState{}, err
	}
	value.Rules = append(desired.Rules, opaqueRules...)
	sort.Slice(value.Rules, func(i, j int) bool { return value.Rules[i].Type < value.Rules[j].Type })
	return value, nil
}

// VisibleState is the comparable form of an observed ruleset: the privileged
// bypass detail and the full writable payload are dropped, leaving the digest
// that stands in for them. Apply compares the stored expectation against exactly
// this form, so a planner must record it rather than the raw observation.
func VisibleState(value githubprovider.RepositoryRulesetState) githubprovider.RepositoryRulesetState {
	return visibleState(value)
}

func visibleState(value githubprovider.RepositoryRulesetState) githubprovider.RepositoryRulesetState {
	if len(value.WritablePayload) != 0 && value.WritableDigest == "" {
		value.WritableDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(value.WritablePayload))
	}
	value.BypassActorsKnown = false
	value.BypassActors = []githubprovider.RulesetBypassActor{}
	value.WritablePayload = nil
	// An empty observed list decodes to a nil slice, which serializes as null and
	// is rejected where the contract requires an array. Canonicalizing here keeps
	// a stored expectation representable, and because Apply compares against this
	// same form, both sides are normalized identically.
	if value.ConditionIncludes == nil {
		value.ConditionIncludes = []string{}
	}
	if value.ConditionExcludes == nil {
		value.ConditionExcludes = []string{}
	}
	if value.Rules == nil {
		value.Rules = []githubprovider.RulesetRule{}
	}
	// Externally owned rule parameters are carried as raw bytes, and DeepEqual
	// compares those bytes. A stored plan round-trips them through Go's encoder,
	// which orders object keys, while a fresh observation keeps whatever order
	// the provider sent -- so the same state compared unequal to itself and every
	// apply reported "state changed after planning" without ever attempting a
	// mutation. Re-encoding both sides here makes the comparison about content.
	rules := make([]githubprovider.RulesetRule, len(value.Rules))
	copy(rules, value.Rules)
	for index := range rules {
		rules[index].ExternalParameters = canonicalRawJSON(rules[index].ExternalParameters)
		rules[index].OpaqueParameters = canonicalRawJSON(rules[index].OpaqueParameters)
	}
	value.Rules = rules
	return value
}

// canonicalRawJSON re-encodes preserved provider bytes with ordered object keys.
// Invalid or empty input is returned unchanged: this normalizes a comparison, it
// is not a validator, and silently dropping bytes it cannot parse would lose the
// external state the raw field exists to preserve.
func canonicalRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return encoded
}

func desiredState(
	scope Scope,
	desired githubprovider.RepositoryRuleset,
	providerID int64,
) githubprovider.RepositoryRulesetState {
	return githubprovider.RepositoryRulesetState{
		ID: providerID, Name: desired.Name, Target: "branch", SourceType: "Repository",
		Source: scope.Owner + "/" + scope.Name, Enforcement: "active",
		BypassActorsKnown: true, BypassActors: []githubprovider.RulesetBypassActor{},
		ConditionIncludes: []string{"~DEFAULT_BRANCH"}, ConditionExcludes: []string{},
		Rules: desired.Rules,
	}
}

func ownedStateEqual(current githubprovider.RepositoryRulesetState, desired githubprovider.RepositoryRuleset) bool {
	if current.Enforcement != desired.Enforcement {
		return false
	}
	if desired.Enforcement == "" && current.Enforcement != "active" {
		return false
	}
	return reflect.DeepEqual(ownedRules(current.Rules), ownedRules(desired.Rules))
}

func applyOwnedState(current githubprovider.RepositoryRulesetState, desired githubprovider.RepositoryRuleset) githubprovider.RepositoryRulesetState {
	current.Enforcement = desired.Enforcement
	if current.Enforcement == "" {
		current.Enforcement = "active"
	}
	owned := ownedRulesByType(desired.Rules)
	result := make([]githubprovider.RulesetRule, 0, len(current.Rules)+len(owned))
	for _, rule := range current.Rules {
		if rule.Type == "required_status_checks" || rule.Type == "pull_request" {
			if replacement, exists := owned[rule.Type]; exists {
				if rule.Type == "pull_request" {
					replacement.ExternalParameters = append(json.RawMessage(nil), rule.ExternalParameters...)
				}
				result = append(result, replacement)
			}
			delete(owned, rule.Type)
			continue
		}
		result = append(result, rule)
	}
	for _, typeName := range []string{"pull_request", "required_status_checks"} {
		if replacement, exists := owned[typeName]; exists {
			result = append(result, replacement)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Type < result[j].Type })
	current.Rules = result
	return current
}

func ownedRules(rules []githubprovider.RulesetRule) []githubprovider.RulesetRule {
	byType := ownedRulesByType(rules)
	result := make([]githubprovider.RulesetRule, 0, len(byType))
	for _, typeName := range []string{"pull_request", "required_status_checks"} {
		if rule, exists := byType[typeName]; exists {
			result = append(result, rule)
		}
	}
	return result
}

func ownedRulesByType(rules []githubprovider.RulesetRule) map[string]githubprovider.RulesetRule {
	result := map[string]githubprovider.RulesetRule{}
	for _, rule := range rules {
		if rule.Type == "required_status_checks" || rule.Type == "pull_request" {
			copy := rule
			copy.ExternalParameters = nil
			copy.OpaqueParameters = nil
			copy.AllowedMergeMethods = append([]string(nil), copy.AllowedMergeMethods...)
			sort.Strings(copy.AllowedMergeMethods)
			result[copy.Type] = copy
		}
	}
	return result
}

func requiredChecks(rules []githubprovider.RulesetRule) *githubprovider.RulesetRule {
	rule, exists := ownedRulesByType(rules)["required_status_checks"]
	if !exists {
		return nil
	}
	return &rule
}

func stateEvidence(
	state githubprovider.RepositoryRulesetState,
	meta *githubprovider.MutationMeta,
	idempotent bool,
) (Evidence, error) {
	digest, err := canonicaljson.Digest(state)
	return Evidence{State: state, Digest: digest, Mutation: meta, Idempotent: idempotent}, err
}
