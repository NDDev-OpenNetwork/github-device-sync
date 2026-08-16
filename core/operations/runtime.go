package operations

import (
	"fmt"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
)

const (
	MutationsDisabledEnvironment    = "GDS_MUTATIONS_DISABLED"
	WebhookReadOnlyEnvironment      = "GDS_WEBHOOK_PROCESSING_READ_ONLY"
	RolloutPausedEnvironment        = "GDS_ROLLOUT_PAUSED"
	HarnessHooksDisabledEnvironment = "GDS_HARNESS_HOOKS_DISABLED"
)

type KillSwitches struct {
	MutationsDisabled         bool `json:"mutations_disabled"`
	WebhookProcessingReadOnly bool `json:"webhook_processing_read_only"`
	RolloutPaused             bool `json:"rollout_paused"`
	HarnessHooksDisabled      bool `json:"harness_hooks_disabled"`
}

type ApprovalEvidence struct {
	ReferenceDigest string `json:"reference_digest"`
	PlanID          string `json:"plan_id"`
	PlanDigest      string `json:"plan_digest"`
	ApprovalClass   string `json:"approval_class"`
	ScopeDigest     string `json:"scope_digest"`
}

type environmentLookup func(string) (string, bool)

func LoadKillSwitches(lookup environmentLookup) (KillSwitches, error) {
	if lookup == nil {
		return KillSwitches{}, fmt.Errorf("environment lookup is nil")
	}
	values := []struct {
		name   string
		target *bool
	}{
		{name: MutationsDisabledEnvironment},
		{name: WebhookReadOnlyEnvironment},
		{name: RolloutPausedEnvironment},
		{name: HarnessHooksDisabledEnvironment},
	}
	result := KillSwitches{}
	values[0].target = &result.MutationsDisabled
	values[1].target = &result.WebhookProcessingReadOnly
	values[2].target = &result.RolloutPaused
	values[3].target = &result.HarnessHooksDisabled
	for _, item := range values {
		value, present := lookup(item.name)
		if !present || value == "" {
			continue
		}
		switch strings.ToLower(value) {
		case "true":
			*item.target = true
		case "false":
			*item.target = false
		default:
			return KillSwitches{MutationsDisabled: true}, fmt.Errorf(
				"%s must be exactly true or false", item.name,
			)
		}
	}
	return result, nil
}

func approvalEvidence(plan Plan, reference string) (ApprovalEvidence, error) {
	scopePayload := struct {
		Scope Scope  `json:"scope"`
		Steps []Step `json:"steps"`
	}{Scope: plan.Scope, Steps: plan.Steps}
	scopeDigest, err := canonicaljson.Digest(scopePayload)
	if err != nil {
		return ApprovalEvidence{}, fmt.Errorf("digest approval scope: %w", err)
	}
	return ApprovalEvidence{
		ReferenceDigest: digestString(reference),
		PlanID:          plan.PlanID, PlanDigest: plan.PlanDigest,
		ApprovalClass: plan.ApprovalClass, ScopeDigest: scopeDigest,
	}, nil
}

// ApprovalScopeDigest returns the canonical digest signed by an approval.
// Approval issuers use this helper so scope canonicalization cannot drift from
// the operation engine's verifier.
func ApprovalScopeDigest(plan Plan) (string, error) {
	evidence, err := approvalEvidence(plan, "scope-digest")
	if err != nil {
		return "", err
	}
	return evidence.ScopeDigest, nil
}

func StepIdempotencyKey(plan Plan, step Step) (string, error) {
	payload := struct {
		PlanDigest string `json:"plan_digest"`
		Step       Step   `json:"step"`
	}{PlanDigest: plan.PlanDigest, Step: step}
	return canonicaljson.Digest(payload)
}
