package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type RequiredStatusCheck struct {
	Context       string `json:"context"`
	IntegrationID int64  `json:"integration_id,omitempty"`
}

type RulesetRule struct {
	Type                             string                `json:"type"`
	RequiredApprovingReviewCount     int                   `json:"required_approving_review_count,omitempty"`
	DismissStaleReviewsOnPush        bool                  `json:"dismiss_stale_reviews_on_push,omitempty"`
	RequireCodeOwnerReview           bool                  `json:"require_code_owner_review,omitempty"`
	RequiredReviewThreadResolution   bool                  `json:"required_review_thread_resolution,omitempty"`
	RequiredStatusChecks             []RequiredStatusCheck `json:"required_status_checks,omitempty"`
	StrictRequiredStatusChecksPolicy bool                  `json:"strict_required_status_checks_policy,omitempty"`
	// OpaqueParameters preserves externally managed or provider-new rule
	// parameters. GDS never synthesizes this field for rules it owns.
	OpaqueParameters json.RawMessage `json:"opaque_parameters,omitempty"`
	// ExternalParameters preserves, as canonical JSON, the parameters of a rule
	// whose other fields GDS owns and types. GitHub keeps adding parameters to
	// existing rules, so partial ownership means typing what GDS governs and
	// carrying the rest through untouched rather than rejecting it as unknown.
	// It is observation-only: updates reuse the lossless observed payload, so GDS
	// never synthesizes an external parameter back to the provider.
	ExternalParameters json.RawMessage `json:"external_parameters,omitempty"`
}

type RepositoryRuleset struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	Target      string        `json:"target"`
	Enforcement string        `json:"enforcement"`
	Rules       []RulesetRule `json:"rules"`
}

type rulesetMutationResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	SourceType  string `json:"source_type"`
	Source      string `json:"source"`
	Enforcement string `json:"enforcement"`
}

func (mutator *RepositoryMutator) UpsertDefaultBranchRuleset(
	ctx context.Context,
	ruleset RepositoryRuleset,
	current *RepositoryRulesetState,
) (RulesetSummary, MutationMeta, error) {
	if err := validateRepositoryRuleset(ruleset); err != nil {
		return RulesetSummary{}, MutationMeta{}, err
	}
	var payload map[string]any
	if current != nil {
		// Each condition is reported separately. Collapsed into one message they
		// were indistinguishable, and the three have different operator actions:
		// a missing identity means the planner did not bind the observation, a
		// mismatched one means the live ruleset was replaced after planning, and
		// an empty payload means the observation was not lossless.
		switch {
		case ruleset.ID == 0:
			return RulesetSummary{}, MutationMeta{}, rulesetStageFailure(
				RulesetStageObservationBinding, "desired-identity-missing",
			)
		case current.ID != ruleset.ID:
			return RulesetSummary{}, MutationMeta{}, rulesetStageFailure(
				RulesetStageObservationBinding, "observed-identity-differs",
			)
		case len(current.WritablePayload) == 0:
			return RulesetSummary{}, MutationMeta{}, rulesetStageFailure(
				RulesetStageObservationBinding, "observation-not-lossless",
			)
		}
		if err := json.Unmarshal(current.WritablePayload, &payload); err != nil {
			return RulesetSummary{}, MutationMeta{}, rulesetStageCause(
				RulesetStageDesiredDecode, "preserved-payload-undecodable", err,
			)
		}
		payload["enforcement"] = ruleset.Enforcement
		if payload["enforcement"] == "" {
			payload["enforcement"] = "active"
		}
		if err := replaceOwnedRulesetChecks(payload, ruleset.Rules); err != nil {
			return RulesetSummary{}, MutationMeta{}, err
		}
	} else {
		payloadRules := make([]map[string]any, len(ruleset.Rules))
		for index, rule := range ruleset.Rules {
			payloadRules[index] = rulesetRulePayload(rule)
		}
		payload = map[string]any{
			"name": ruleset.Name, "target": "branch", "enforcement": "active",
			"bypass_actors": []any{},
			"conditions": map[string]any{
				"ref_name": map[string]any{
					"include": []string{"~DEFAULT_BRANCH"}, "exclude": []string{},
				},
			},
			"rules": payloadRules,
		}
	}
	method := http.MethodPost
	suffix := "rulesets"
	if ruleset.ID != 0 {
		method = http.MethodPut
		suffix += "/" + strconv.FormatInt(ruleset.ID, 10)
	}
	target, err := mutator.endpoint(suffix)
	if err != nil {
		return RulesetSummary{}, MutationMeta{}, rulesetStageCause(
			RulesetStageRequestEncode, "endpoint-unresolvable", err,
		)
	}
	response, meta, err := mutator.mutate(
		ctx, MutationRepositoryRuleset, method, target, payload,
	)
	if err != nil {
		return RulesetSummary{}, meta, err
	}
	// The postcondition used to be one seven-way disjunction collapsing into a
	// single error, so a provider that accepted the write but returned, say, a
	// different enforcement was indistinguishable from an unreadable body. Each
	// condition now names the field it proved wrong.
	var raw rulesetMutationResponse
	if err := decodeJSON(response.Body, &raw); err != nil {
		return RulesetSummary{}, meta, invalidMutationResponse(response, rulesetStageCause(
			RulesetStageResponseDecode, "response-body-undecodable", err,
		))
	}
	for _, condition := range []struct {
		ok     bool
		reason string
		field  string
	}{
		{raw.ID > 0, "identity-absent", "id"},
		{raw.Name == ruleset.Name, "name-differs", "name"},
		{raw.Target == "branch", "target-differs", "target"},
		{raw.SourceType == "Repository", "source-type-differs", "source_type"},
		{strings.EqualFold(raw.Source, mutator.scope.Owner+"/"+mutator.scope.Name), "source-differs", "source"},
		{raw.Enforcement == "active", "enforcement-differs", "enforcement"},
	} {
		if !condition.ok {
			return RulesetSummary{}, meta, invalidMutationResponse(response, rulesetStageFieldFailure(
				RulesetStagePostcondition, condition.reason, condition.field,
			))
		}
	}
	return RulesetSummary{
		ID: raw.ID, Name: raw.Name, Target: raw.Target, SourceType: raw.SourceType,
		Source: raw.Source, Enforcement: raw.Enforcement,
	}, meta, nil
}

func replaceOwnedRulesetChecks(payload map[string]any, desired []RulesetRule) error {
	rawRules, ok := payload["rules"].([]any)
	if !ok {
		return rulesetStageFieldFailure(
			RulesetStageExternalFieldMerge, "preserved-rules-not-a-list", "rules",
		)
	}
	var owned map[string]any
	for _, rule := range desired {
		if rule.Type == "required_status_checks" {
			owned = rulesetRulePayload(rule)
		}
	}
	// Do not add to a provider-controlled length when computing allocation
	// capacity. append grows the bounded decoded slice safely if the owned rule
	// was not present in the observation.
	result := make([]any, 0, len(rawRules))
	replaced := false
	for _, value := range rawRules {
		rule, ok := value.(map[string]any)
		if !ok {
			return rulesetStageFieldFailure(
				RulesetStageExternalFieldMerge, "preserved-rule-not-an-object", "rules[]",
			)
		}
		if rule["type"] == "required_status_checks" {
			if owned != nil {
				result = append(result, owned)
			}
			replaced = true
			continue
		}
		result = append(result, rule)
	}
	if owned != nil && !replaced {
		result = append(result, owned)
	}
	payload["rules"] = result
	return nil
}

func validateRepositoryRuleset(ruleset RepositoryRuleset) error {
	// Each rejected field is named, so a contract that a live reconcile refuses
	// points at the exact declaration to correct rather than at the whole
	// document. The rule type is a closed set from this package, so echoing it
	// as a field identifier carries no provider payload.
	for _, condition := range []struct {
		ok     bool
		reason string
		field  string
	}{
		{ruleset.ID >= 0, "identity-negative", "id"},
		{boundedProviderText(ruleset.Name, 256), "name-unbounded", "name"},
		{ruleset.Target == "" || ruleset.Target == "branch", "target-unsupported", "target"},
		{ruleset.Enforcement == "" || ruleset.Enforcement == "active", "enforcement-unsupported", "enforcement"},
		{len(ruleset.Rules) > 0, "rules-empty", "rules"},
		{len(ruleset.Rules) <= 32, "rules-over-bound", "rules"},
	} {
		if !condition.ok {
			return rulesetStageFieldFailure(RulesetStageContractValidation, condition.reason, condition.field)
		}
	}
	seen := map[string]struct{}{}
	for _, rule := range ruleset.Rules {
		if _, duplicate := seen[rule.Type]; duplicate {
			return rulesetStageFieldFailure(RulesetStageContractValidation, "rule-repeated", rule.Type)
		}
		seen[rule.Type] = struct{}{}
		switch rule.Type {
		case "deletion", "non_fast_forward", "required_linear_history", "required_signatures":
			if rule.RequiredApprovingReviewCount != 0 || len(rule.RequiredStatusChecks) != 0 {
				return rulesetStageFieldFailure(
					RulesetStageContractValidation, "parameterless-rule-carries-parameters", rule.Type,
				)
			}
		case "pull_request":
			if rule.RequiredApprovingReviewCount < 0 || rule.RequiredApprovingReviewCount > 6 ||
				len(rule.RequiredStatusChecks) != 0 {
				return rulesetStageFieldFailure(
					RulesetStageContractValidation, "pull-request-parameters-invalid", rule.Type,
				)
			}
		case "required_status_checks":
			if len(rule.RequiredStatusChecks) == 0 || len(rule.RequiredStatusChecks) > 50 ||
				rule.RequiredApprovingReviewCount != 0 {
				return rulesetStageFieldFailure(
					RulesetStageContractValidation, "status-check-parameters-invalid", rule.Type,
				)
			}
			checks := map[string]struct{}{}
			for _, check := range rule.RequiredStatusChecks {
				if !boundedProviderText(check.Context, 256) || check.IntegrationID < 0 {
					return rulesetStageFieldFailure(
						RulesetStageContractValidation, "required-status-check-invalid", rule.Type,
					)
				}
				key := check.Context + ":" + strconv.FormatInt(check.IntegrationID, 10)
				if _, duplicate := checks[key]; duplicate {
					return rulesetStageFieldFailure(
						RulesetStageContractValidation, "required-status-check-duplicated", rule.Type,
					)
				}
				checks[key] = struct{}{}
			}
		default:
			return rulesetStageFieldFailure(RulesetStageContractValidation, "rule-type-unsupported", rule.Type)
		}
	}
	return nil
}

func rulesetRulePayload(rule RulesetRule) map[string]any {
	payload := map[string]any{"type": rule.Type}
	switch rule.Type {
	case "pull_request":
		payload["parameters"] = map[string]any{
			"required_approving_review_count":   rule.RequiredApprovingReviewCount,
			"dismiss_stale_reviews_on_push":     rule.DismissStaleReviewsOnPush,
			"require_code_owner_review":         rule.RequireCodeOwnerReview,
			"required_review_thread_resolution": rule.RequiredReviewThreadResolution,
			"require_last_push_approval":        false,
		}
	case "required_status_checks":
		payload["parameters"] = map[string]any{
			"required_status_checks":               rule.RequiredStatusChecks,
			"strict_required_status_checks_policy": rule.StrictRequiredStatusChecksPolicy,
			"do_not_enforce_on_create":             false,
		}
	}
	return payload
}
