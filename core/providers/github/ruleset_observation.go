package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type RulesetBypassActor struct {
	ActorID    *int64 `json:"actor_id"`
	ActorType  string `json:"actor_type"`
	BypassMode string `json:"bypass_mode"`
}

type RepositoryRulesetState struct {
	ID                int64                `json:"id"`
	Name              string               `json:"name"`
	Target            string               `json:"target"`
	SourceType        string               `json:"source_type"`
	Source            string               `json:"source"`
	Enforcement       string               `json:"enforcement"`
	BypassActorsKnown bool                 `json:"bypass_actors_known"`
	BypassActors      []RulesetBypassActor `json:"bypass_actors"`
	ConditionIncludes []string             `json:"condition_includes"`
	ConditionExcludes []string             `json:"condition_excludes"`
	Rules             []RulesetRule        `json:"rules"`
	// WritablePayload is the complete documented update payload reconstructed
	// from the privileged observation. It is required for lossless updates.
	WritablePayload json.RawMessage `json:"writable_payload,omitempty"`
	WritableDigest  string          `json:"writable_digest,omitempty"`
}

type rulesetObservationResponse struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Target       string          `json:"target"`
	SourceType   string          `json:"source_type"`
	Source       string          `json:"source"`
	Enforcement  string          `json:"enforcement"`
	BypassActors json.RawMessage `json:"bypass_actors"`
	Conditions   struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []struct {
		Type       string          `json:"type"`
		Parameters json.RawMessage `json:"parameters"`
	} `json:"rules"`
}

func (client *Client) ListRepositoryRulesets(
	ctx context.Context,
	owner string,
	name string,
) ([]RulesetSummary, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) {
		return nil, ResponseMeta{}, fmt.Errorf("invalid GitHub repository identity")
	}
	base := "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/"
	return client.getRulesets(ctx, base)
}

func (client *Client) GetRepositoryRuleset(
	ctx context.Context,
	owner string,
	name string,
	rulesetID int64,
) (RepositoryRulesetState, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) || rulesetID <= 0 {
		return RepositoryRulesetState{}, ResponseMeta{}, fmt.Errorf("invalid GitHub repository ruleset identity")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/rulesets/"+
			strconv.FormatInt(rulesetID, 10),
		url.Values{"includes_parents": {"false"}},
	)
	if err != nil {
		return RepositoryRulesetState{}, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return RepositoryRulesetState{}, response.Meta, err
	}
	var raw rulesetObservationResponse
	if err := decodeJSON(response.Body, &raw); err != nil {
		return RepositoryRulesetState{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	var rawFields map[string]json.RawMessage
	if err := decodeJSON(response.Body, &rawFields); err != nil {
		return RepositoryRulesetState{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	state, err := normalizeRepositoryRuleset(raw, rawFields, owner, name)
	if err != nil {
		return RepositoryRulesetState{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	return state, response.Meta, nil
}

func (mutator *RepositoryMutator) GetRepositoryRuleset(
	ctx context.Context,
	rulesetID int64,
) (RepositoryRulesetState, ResponseMeta, error) {
	if _, allowed := mutator.operations[MutationRepositoryRuleset]; !allowed {
		return RepositoryRulesetState{}, ResponseMeta{}, fmt.Errorf(
			"GitHub mutation operation %q is outside bound repository scope", MutationRepositoryRuleset,
		)
	}
	state, meta, err := mutator.factory.client.GetRepositoryRuleset(
		ctx, mutator.scope.Owner, mutator.scope.Name, rulesetID,
	)
	if err != nil {
		return RepositoryRulesetState{}, meta, err
	}
	if !state.BypassActorsKnown {
		return RepositoryRulesetState{}, meta, fmt.Errorf(
			"GitHub mutation credential did not expose ruleset bypass actors",
		)
	}
	return state, meta, nil
}

func (mutator *RepositoryMutator) ListRepositoryRulesets(
	ctx context.Context,
) ([]RulesetSummary, ResponseMeta, error) {
	if _, allowed := mutator.operations[MutationRepositoryRuleset]; !allowed {
		return nil, ResponseMeta{}, fmt.Errorf(
			"GitHub mutation operation %q is outside bound repository scope", MutationRepositoryRuleset,
		)
	}
	return mutator.factory.client.ListRepositoryRulesets(
		ctx, mutator.scope.Owner, mutator.scope.Name,
	)
}

func normalizeRepositoryRuleset(
	raw rulesetObservationResponse,
	rawFields map[string]json.RawMessage,
	owner string,
	name string,
) (RepositoryRulesetState, error) {
	if raw.ID <= 0 || !boundedProviderText(raw.Name, 256) || raw.Target != "branch" ||
		raw.SourceType != "Repository" || !strings.EqualFold(raw.Source, owner+"/"+name) ||
		(raw.Enforcement != "active" && raw.Enforcement != "disabled" && raw.Enforcement != "evaluate") ||
		len(raw.Conditions.RefName.Include) == 0 || len(raw.Conditions.RefName.Include) > 100 ||
		len(raw.Conditions.RefName.Exclude) > 100 || len(raw.Rules) == 0 || len(raw.Rules) > 32 {
		return RepositoryRulesetState{}, fmt.Errorf("GitHub repository ruleset response is invalid")
	}
	state := RepositoryRulesetState{
		ID: raw.ID, Name: raw.Name, Target: raw.Target, SourceType: raw.SourceType,
		Source: raw.Source, Enforcement: raw.Enforcement,
		ConditionIncludes: append([]string(nil), raw.Conditions.RefName.Include...),
		ConditionExcludes: append([]string(nil), raw.Conditions.RefName.Exclude...),
		BypassActors:      []RulesetBypassActor{}, Rules: make([]RulesetRule, len(raw.Rules)),
	}
	if raw.BypassActors != nil {
		writableFields := map[string]json.RawMessage{}
		for _, key := range []string{"name", "target", "enforcement", "bypass_actors", "conditions", "rules"} {
			value, found := rawFields[key]
			if !found || !json.Valid(value) {
				return RepositoryRulesetState{}, fmt.Errorf("GitHub ruleset lacks lossless writable field %q", key)
			}
			writableFields[key] = append(json.RawMessage(nil), value...)
		}
		writable, err := json.Marshal(writableFields)
		if err != nil {
			return RepositoryRulesetState{}, fmt.Errorf("preserve GitHub ruleset payload: %w", err)
		}
		state.WritablePayload = writable
		state.WritableDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(writable))
	}
	for _, values := range [][]string{state.ConditionIncludes, state.ConditionExcludes} {
		if err := validateUniqueBoundedStrings(values, 256); err != nil {
			return RepositoryRulesetState{}, err
		}
	}
	sort.Strings(state.ConditionIncludes)
	sort.Strings(state.ConditionExcludes)
	if raw.BypassActors != nil {
		state.BypassActorsKnown = true
		if err := json.Unmarshal(raw.BypassActors, &state.BypassActors); err != nil ||
			len(state.BypassActors) > 100 {
			return RepositoryRulesetState{}, fmt.Errorf("GitHub ruleset bypass actors are invalid")
		}
		for _, actor := range state.BypassActors {
			if !validBypassActor(actor) {
				return RepositoryRulesetState{}, fmt.Errorf("GitHub ruleset bypass actor is invalid")
			}
		}
		sort.Slice(state.BypassActors, func(left, right int) bool {
			leftID, rightID := int64(-1), int64(-1)
			if state.BypassActors[left].ActorID != nil {
				leftID = *state.BypassActors[left].ActorID
			}
			if state.BypassActors[right].ActorID != nil {
				rightID = *state.BypassActors[right].ActorID
			}
			if state.BypassActors[left].ActorType != state.BypassActors[right].ActorType {
				return state.BypassActors[left].ActorType < state.BypassActors[right].ActorType
			}
			if leftID != rightID {
				return leftID < rightID
			}
			return state.BypassActors[left].BypassMode < state.BypassActors[right].BypassMode
		})
	}
	seenRules := map[string]struct{}{}
	for index, rawRule := range raw.Rules {
		if _, duplicate := seenRules[rawRule.Type]; duplicate {
			return RepositoryRulesetState{}, fmt.Errorf("GitHub ruleset repeats a rule")
		}
		seenRules[rawRule.Type] = struct{}{}
		rule, err := normalizeRulesetRule(rawRule.Type, rawRule.Parameters)
		if err != nil {
			return RepositoryRulesetState{}, err
		}
		state.Rules[index] = rule
	}
	sort.Slice(state.Rules, func(left, right int) bool { return state.Rules[left].Type < state.Rules[right].Type })
	return state, nil
}

func normalizeRulesetRule(ruleType string, parameters json.RawMessage) (RulesetRule, error) {
	rule := RulesetRule{Type: ruleType}
	switch ruleType {
	case "deletion", "non_fast_forward", "required_linear_history", "required_signatures":
		if len(parameters) != 0 && string(parameters) != "null" && string(parameters) != "{}" {
			return RulesetRule{}, fmt.Errorf("parameterless GitHub ruleset rule has parameters")
		}
	case "pull_request":
		var value struct {
			RequiredApprovingReviewCount               int      `json:"required_approving_review_count"`
			DismissStaleReviewsOnPush                  bool     `json:"dismiss_stale_reviews_on_push"`
			RequireCodeOwnerReview                     bool     `json:"require_code_owner_review"`
			RequiredReviewThreadResolution             bool     `json:"required_review_thread_resolution"`
			RequireLastPushApproval                    bool     `json:"require_last_push_approval"`
			RequireExtraApprovalForUnattributedChanges bool     `json:"require_extra_approval_for_unattributed_changes"`
			AllowedMergeMethods                        []string `json:"allowed_merge_methods"`
		}
		external, err := decodeOwnedRuleParameters(parameters, &value, []string{
			"required_approving_review_count", "dismiss_stale_reviews_on_push",
			"require_code_owner_review", "required_review_thread_resolution",
			"require_last_push_approval",
			"require_extra_approval_for_unattributed_changes", "allowed_merge_methods",
		})
		if err != nil {
			return RulesetRule{}, fmt.Errorf("GitHub pull-request ruleset parameters are unsupported")
		}
		rule.ExternalParameters = external
		rule.RequiredApprovingReviewCount = value.RequiredApprovingReviewCount
		rule.DismissStaleReviewsOnPush = value.DismissStaleReviewsOnPush
		rule.RequireCodeOwnerReview = value.RequireCodeOwnerReview
		rule.RequiredReviewThreadResolution = value.RequiredReviewThreadResolution
		rule.RequireLastPushApproval = value.RequireLastPushApproval
		rule.RequireExtraApprovalForUnattributedChanges = value.RequireExtraApprovalForUnattributedChanges
		rule.AllowedMergeMethods = append([]string(nil), value.AllowedMergeMethods...)
	case "required_status_checks":
		var value struct {
			RequiredStatusChecks             []RequiredStatusCheck `json:"required_status_checks"`
			StrictRequiredStatusChecksPolicy bool                  `json:"strict_required_status_checks_policy"`
			DoNotEnforceOnCreate             bool                  `json:"do_not_enforce_on_create"`
		}
		external, err := decodeOwnedRuleParameters(parameters, &value, []string{
			"required_status_checks", "strict_required_status_checks_policy",
			"do_not_enforce_on_create",
		})
		if err != nil || value.DoNotEnforceOnCreate {
			return RulesetRule{}, fmt.Errorf("GitHub status-check ruleset parameters are unsupported")
		}
		rule.ExternalParameters = external
		rule.RequiredStatusChecks = append([]RequiredStatusCheck(nil), value.RequiredStatusChecks...)
		rule.StrictRequiredStatusChecksPolicy = value.StrictRequiredStatusChecksPolicy
		sort.Slice(rule.RequiredStatusChecks, func(left, right int) bool {
			if rule.RequiredStatusChecks[left].Context != rule.RequiredStatusChecks[right].Context {
				return rule.RequiredStatusChecks[left].Context < rule.RequiredStatusChecks[right].Context
			}
			return rule.RequiredStatusChecks[left].IntegrationID < rule.RequiredStatusChecks[right].IntegrationID
		})
	default:
		if !boundedProviderText(ruleType, 128) {
			return RulesetRule{}, fmt.Errorf("GitHub ruleset rule type is invalid")
		}
		rule.OpaqueParameters = append(json.RawMessage(nil), parameters...)
	}
	if rule.OpaqueParameters == nil {
		if err := validateRepositoryRuleset(RepositoryRuleset{
			Name: "validation", Rules: []RulesetRule{rule},
		}); err != nil {
			return RulesetRule{}, err
		}
	}
	return rule, nil
}

// DecodeRulesetDocument decodes a ruleset in the provider's own wire shape --
// the form GitHub returns and the form a tracked contract file is written in --
// into the typed desired ruleset.
//
// It exists because that wire shape nests rule fields under `parameters` while
// RulesetRule is flat. Unmarshalling the document straight into RepositoryRuleset
// therefore succeeds and silently yields empty rules, so every later comparison
// passes vacuously. Routing through the same normalizer the live observation
// uses means a tracked contract and an observed payload are read identically.
func DecodeRulesetDocument(raw []byte) (RepositoryRuleset, error) {
	var document struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Target      string `json:"target"`
		Enforcement string `json:"enforcement"`
		Rules       []struct {
			Type       string          `json:"type"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return RepositoryRuleset{}, fmt.Errorf("decode ruleset document: %w", err)
	}
	if !boundedProviderText(document.Name, 256) || len(document.Rules) == 0 ||
		len(document.Rules) > 32 {
		return RepositoryRuleset{}, fmt.Errorf("ruleset document is invalid")
	}
	ruleset := RepositoryRuleset{
		ID: document.ID, Name: document.Name, Target: document.Target,
		Enforcement: document.Enforcement, Rules: make([]RulesetRule, 0, len(document.Rules)),
	}
	if ruleset.Target == "" {
		ruleset.Target = "branch"
	}
	seen := map[string]struct{}{}
	for _, rule := range document.Rules {
		if _, duplicate := seen[rule.Type]; duplicate {
			return RepositoryRuleset{}, fmt.Errorf("ruleset document repeats rule %q", rule.Type)
		}
		seen[rule.Type] = struct{}{}
		normalized, err := normalizeRulesetRule(rule.Type, rule.Parameters)
		if err != nil {
			return RepositoryRuleset{}, err
		}
		// Externally managed parameters are observation-only. A tracked contract
		// may legitimately restate them -- it is written in the provider's shape --
		// but a desired ruleset must never carry them back, because an update
		// preserves them from the lossless observation instead. Keeping them here
		// would also declare ownership GDS does not have.
		normalized.ExternalParameters = nil
		normalized.OpaqueParameters = nil
		ruleset.Rules = append(ruleset.Rules, normalized)
	}
	sort.Slice(ruleset.Rules, func(left, right int) bool {
		return ruleset.Rules[left].Type < ruleset.Rules[right].Type
	})
	return ruleset, nil
}

// decodeOwnedRuleParameters types the parameters GDS owns and returns every other
// parameter as canonical JSON.
//
// Rejecting an unmodelled parameter as unknown would be correct only if GitHub
// froze the rule schema. It does not: the live pull_request rule now carries
// required_reviewers, dismissal_restriction, and allowed_merge_methods, none of
// which GDS governs. Failing there makes the current ruleset unobservable, and
// nothing downstream can classify drift on a state it could not read. Owned
// fields stay strictly typed and bounded, so drift on what GDS governs is exact;
// external fields are preserved verbatim for reporting and survive an update
// because the mutation path rewrites only the owned rule.
func decodeOwnedRuleParameters(raw json.RawMessage, target any, owned []string) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("GitHub ruleset parameters contain multiple JSON values")
		}
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if len(raw) != 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
	}
	for _, key := range owned {
		delete(fields, key)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	if len(fields) > 64 {
		return nil, errors.New("GitHub ruleset rule declares too many external parameters")
	}
	for key, value := range fields {
		if !boundedProviderText(key, 128) || !json.Valid(value) || len(value) > 64<<10 {
			return nil, errors.New("GitHub ruleset external parameter is invalid")
		}
	}
	// json.Marshal sorts map keys, so the preserved form is canonical and its
	// digest is stable across observations.
	external, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return external, nil
}

func validateUniqueBoundedStrings(values []string, maximum int) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		if !boundedProviderText(value, maximum) {
			return fmt.Errorf("GitHub ruleset condition is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("GitHub ruleset conditions contain duplicates")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validBypassActor(actor RulesetBypassActor) bool {
	switch actor.ActorType {
	case "Integration", "RepositoryRole", "Team", "User":
		if actor.ActorID == nil || *actor.ActorID <= 0 {
			return false
		}
	case "OrganizationAdmin":
	case "DeployKey":
		if actor.ActorID != nil {
			return false
		}
	default:
		return false
	}
	return actor.BypassMode == "always" || actor.BypassMode == "pull_request"
}
