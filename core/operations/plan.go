// Package operations implements GDS plan/apply/verify orchestration.
package operations

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type Plan struct {
	SchemaVersion int            `json:"schema_version"`
	PlanID        string         `json:"plan_id"`
	Operation     string         `json:"operation"`
	CreatedAt     time.Time      `json:"created_at"`
	ExpiresAt     time.Time      `json:"expires_at"`
	Actor         Actor          `json:"actor"`
	Scope         Scope          `json:"scope"`
	Preconditions []Precondition `json:"preconditions"`
	Steps         []Step         `json:"steps"`
	ApprovalClass string         `json:"approval_class"`
	PlanDigest    string         `json:"plan_digest"`
}

type Actor struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

type Scope struct {
	TaskID       string   `json:"task_id,omitempty"`
	Repositories []string `json:"repositories"`
}

type Precondition struct {
	RepositoryID         string `json:"repository_id"`
	HeadOID              string `json:"head_oid"`
	WorktreeFingerprint  string `json:"worktree_fingerprint,omitempty"`
	IndexTreeOID         string `json:"index_tree_oid,omitempty"`
	UpstreamOID          string `json:"upstream_oid,omitempty"`
	RemoteDefaultOID     string `json:"remote_default_oid,omitempty"`
	RemoteEvidenceDigest string `json:"remote_evidence_digest,omitempty"`
	ManifestDigest       string `json:"manifest_digest"`
	PolicyDigest         string `json:"policy_digest"`
}

type Step struct {
	StepID           string         `json:"step_id"`
	RepositoryID     string         `json:"repository_id"`
	Action           string         `json:"action"`
	RequiresApproval bool           `json:"requires_approval"`
	Compensation     Compensation   `json:"compensation"`
	Parameters       map[string]any `json:"parameters,omitempty"`
	WriteSet         []string       `json:"write_set"`
}

type Compensation struct {
	Mode       string `json:"mode"`
	Action     string `json:"action,omitempty"`
	Reversible bool   `json:"reversible,omitempty"`
	Idempotent bool   `json:"idempotent,omitempty"`
}

type PlanInput struct {
	Operation     string
	Actor         Actor
	TaskID        string
	Preconditions []Precondition
	Steps         []Step
	ApprovalClass string
}

func NewPlan(planID string, createdAt time.Time, expiresAt time.Time, input PlanInput) (Plan, error) {
	preconditions := append([]Precondition(nil), input.Preconditions...)
	sort.Slice(preconditions, func(left, right int) bool {
		return preconditions[left].RepositoryID < preconditions[right].RepositoryID
	})
	repositories := make([]string, 0, len(preconditions))
	for _, precondition := range preconditions {
		repositories = append(repositories, precondition.RepositoryID)
	}
	plan := Plan{
		SchemaVersion: domain.SchemaVersion,
		PlanID:        planID, Operation: input.Operation,
		CreatedAt: createdAt.UTC(), ExpiresAt: expiresAt.UTC(), Actor: input.Actor,
		Scope:         Scope{TaskID: input.TaskID, Repositories: repositories},
		Preconditions: preconditions, Steps: normalizeStepWriteSets(input.Steps),
		ApprovalClass: input.ApprovalClass,
	}
	digest, err := planDigest(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.PlanDigest = digest
	return plan, nil
}

func (plan Plan) Marshal() ([]byte, error) {
	return json.Marshal(plan)
}

func DecodePlan(raw []byte) (Plan, error) {
	var plan Plan
	if err := serialization.DecodeInto("plan.json", raw, &plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (plan Plan) Validate(schemas *validation.Set) []domain.Finding {
	raw, err := plan.Marshal()
	if err != nil {
		return []domain.Finding{{
			Code: "GDS_PLAN_ENCODE_FAILED", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	value, err := serialization.Decode("plan.json", raw)
	if err != nil {
		return []domain.Finding{{
			Code: "GDS_PLAN_DECODE_FAILED", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	findings := schemas.Validate("plan", value, "in-memory-plan")
	expectedDigest, digestErr := planDigest(plan)
	if digestErr != nil || expectedDigest != plan.PlanDigest {
		findings = append(findings, domain.Finding{
			Code: "GDS_PLAN_DIGEST_MISMATCH", Severity: domain.SeverityHigh,
			Message: "plan_digest does not match the exact plan content",
		})
	}
	if !plan.ExpiresAt.After(plan.CreatedAt) {
		findings = append(findings, planSemanticFinding(
			"expires_at must be after created_at",
		))
	}
	repositories := make(map[string]struct{}, len(plan.Preconditions))
	ordered := make([]string, 0, len(plan.Preconditions))
	for _, precondition := range plan.Preconditions {
		if _, duplicate := repositories[precondition.RepositoryID]; duplicate {
			findings = append(findings, planSemanticFinding(
				"precondition repository ids must be unique",
			))
			continue
		}
		repositories[precondition.RepositoryID] = struct{}{}
		ordered = append(ordered, precondition.RepositoryID)
	}
	sort.Strings(ordered)
	if !equalStrings(plan.Scope.Repositories, ordered) {
		findings = append(findings, planSemanticFinding(
			"scope repositories must exactly match sorted preconditions",
		))
	}
	stepIDs := map[string]struct{}{}
	covered := map[string]struct{}{}
	for _, step := range plan.Steps {
		if _, duplicate := stepIDs[step.StepID]; duplicate {
			findings = append(findings, planSemanticFinding("step ids must be unique"))
		}
		stepIDs[step.StepID] = struct{}{}
		if _, found := repositories[step.RepositoryID]; !found {
			findings = append(findings, planSemanticFinding(
				"every step repository requires one exact precondition",
			))
		} else {
			covered[step.RepositoryID] = struct{}{}
		}
		if len(step.WriteSet) == 0 || !sort.StringsAreSorted(step.WriteSet) {
			findings = append(findings, planSemanticFinding("every mutation step requires a sorted declared write set"))
		}
		for index, target := range step.WriteSet {
			if target == "" || len(target) > 256 || strings.ContainsAny(target, "\x00\r\n") ||
				(index > 0 && step.WriteSet[index-1] == target) {
				findings = append(findings, planSemanticFinding("step write-set targets must be unique bounded identities"))
			}
		}
	}
	if len(covered) != len(repositories) {
		findings = append(findings, planSemanticFinding(
			"every scoped repository requires at least one step",
		))
	}
	return findings
}

func normalizeStepWriteSets(steps []Step) []Step {
	result := append([]Step(nil), steps...)
	for index := range result {
		result[index].WriteSet = append([]string(nil), result[index].WriteSet...)
		if len(result[index].WriteSet) == 0 {
			result[index].WriteSet = []string{"repository"}
		}
		sort.Strings(result[index].WriteSet)
	}
	return result
}

func planSemanticFinding(message string) domain.Finding {
	return domain.Finding{
		Code: "GDS_PLAN_SEMANTIC_INVALID", Severity: domain.SeverityHigh,
		Message: message,
	}
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func planDigest(plan Plan) (string, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode plan for digest: %w", err)
	}
	value, err := serialization.Decode("plan.json", raw)
	if err != nil {
		return "", err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("plan payload is not an object")
	}
	return canonicaljson.DigestObjectWithoutField(object, "plan_digest")
}

func (plan Plan) RequiresApproval() bool {
	for _, step := range plan.Steps {
		if step.RequiresApproval {
			return true
		}
	}
	return false
}
