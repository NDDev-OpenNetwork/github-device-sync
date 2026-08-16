// Package portfolio builds aggregate plans across independent repository boundaries.
package portfolio

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type Plan struct {
	SchemaVersion   int       `json:"schema_version"`
	PlanID          string    `json:"plan_id"`
	CreatedAt       time.Time `json:"created_at"`
	Portfolio       string    `json:"portfolio"`
	Operation       string    `json:"operation"`
	Intent          string    `json:"intent"`
	TargetSetDigest string    `json:"target_set_digest"`
	Subplans        []Subplan `json:"subplans"`
	ReadyCount      int       `json:"ready_count"`
	BlockedCount    int       `json:"blocked_count"`
	PlanDigest      string    `json:"plan_digest"`
}

type Subplan struct {
	RepositoryID   string       `json:"repository_id"`
	Path           string       `json:"path"`
	Status         string       `json:"status"`
	HeadOID        string       `json:"head_oid,omitempty"`
	ManifestDigest string       `json:"manifest_digest,omitempty"`
	PolicyDigest   string       `json:"policy_digest,omitempty"`
	FindingCodes   []string     `json:"finding_codes"`
	DependsOn      []string     `json:"depends_on"`
	Compensation   Compensation `json:"compensation"`
	SubplanDigest  string       `json:"subplan_digest"`
}

type Compensation struct {
	Mode       string `json:"mode"`
	Action     string `json:"action,omitempty"`
	Reversible bool   `json:"reversible"`
	Idempotent bool   `json:"idempotent"`
}

type BuildInput struct {
	PlanID    string
	CreatedAt time.Time
	Portfolio string
	Operation string
	Intent    string
	Subplans  []Subplan
}

func Build(input BuildInput, schemas *validation.Set) (Plan, []domain.Finding) {
	subplans := append([]Subplan(nil), input.Subplans...)
	if len(subplans) == 0 || len(subplans) > 2000 {
		return Plan{}, []domain.Finding{planFinding(
			"GDS_PORTFOLIO_TARGET_COUNT_INVALID", "Portfolio target count must be between 1 and 2000.",
		)}
	}
	var orderErr error
	subplans, orderErr = dependencyOrder(subplans)
	if orderErr != nil {
		return Plan{}, []domain.Finding{planFinding("GDS_PORTFOLIO_DEPENDENCY_INVALID", orderErr.Error())}
	}
	targets := make([]string, 0, len(subplans))
	ready := 0
	blocked := 0
	for index := range subplans {
		if index > 0 && subplans[index-1].RepositoryID == subplans[index].RepositoryID {
			return Plan{}, []domain.Finding{planFinding(
				"GDS_PORTFOLIO_DUPLICATE_TARGET", "A repository occurs more than once in the portfolio plan.",
			)}
		}
		subplans[index].FindingCodes = normalizedFindingCodes(subplans[index].FindingCodes)
		subplans[index].DependsOn = normalizedFindingCodes(subplans[index].DependsOn)
		if subplans[index].Compensation.Mode == "" {
			subplans[index].Compensation.Mode = "none"
		}
		if subplans[index].Compensation.Mode == "automatic" && (!subplans[index].Compensation.Reversible || !subplans[index].Compensation.Idempotent || subplans[index].Compensation.Action == "") {
			return Plan{}, []domain.Finding{planFinding("GDS_PORTFOLIO_COMPENSATION_UNPROVEN", "Automatic compensation requires explicit reversible and idempotent proof plus an action.")}
		}
		targets = append(targets, subplans[index].RepositoryID)
		subplans[index].SubplanDigest = ""
		digest, err := canonicaljson.Digest(subplans[index])
		if err != nil {
			return Plan{}, []domain.Finding{planFinding("GDS_PORTFOLIO_PLAN_DIGEST_FAILED", err.Error())}
		}
		subplans[index].SubplanDigest = digest
		if subplans[index].Status == "ready" {
			ready++
		} else {
			blocked++
		}
	}
	sort.Strings(targets)
	targetDigest, err := canonicaljson.Digest(targets)
	if err != nil {
		return Plan{}, []domain.Finding{planFinding("GDS_PORTFOLIO_PLAN_DIGEST_FAILED", err.Error())}
	}
	plan := Plan{
		SchemaVersion: domain.SchemaVersion, PlanID: input.PlanID,
		CreatedAt: input.CreatedAt.UTC(), Portfolio: input.Portfolio,
		Operation: input.Operation, Intent: input.Intent, TargetSetDigest: targetDigest,
		Subplans: subplans, ReadyCount: ready, BlockedCount: blocked,
	}
	digest, err := planDigest(plan)
	if err != nil {
		return Plan{}, []domain.Finding{planFinding("GDS_PORTFOLIO_PLAN_DIGEST_FAILED", err.Error())}
	}
	plan.PlanDigest = digest
	if findings := Validate(plan, schemas); len(findings) != 0 {
		return Plan{}, findings
	}
	return plan, nil
}

func normalizedFindingCodes(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] == result[write-1] {
			continue
		}
		result[write] = result[read]
		write++
	}
	return result[:write]
}

func Validate(plan Plan, schemas *validation.Set) []domain.Finding {
	raw, err := json.Marshal(plan)
	if err != nil {
		return []domain.Finding{planFinding("GDS_PORTFOLIO_PLAN_ENCODE_FAILED", err.Error())}
	}
	value, err := serialization.Decode("portfolio-plan.json", raw)
	if err != nil {
		return []domain.Finding{planFinding("GDS_PORTFOLIO_PLAN_DECODE_FAILED", err.Error())}
	}
	findings := schemas.Validate("portfolio-plan", value, "in-memory-portfolio-plan")
	if len(findings) != 0 {
		return findings
	}
	expected, err := planDigest(plan)
	if err != nil || expected != plan.PlanDigest {
		return []domain.Finding{planFinding("GDS_PORTFOLIO_PLAN_DIGEST_MISMATCH", "Portfolio plan digest is invalid.")}
	}
	for _, subplan := range plan.Subplans {
		candidate := subplan
		candidate.SubplanDigest = ""
		digest, digestErr := canonicaljson.Digest(candidate)
		if digestErr != nil || digest != subplan.SubplanDigest {
			findings = append(findings, planFinding(
				"GDS_PORTFOLIO_SUBPLAN_DIGEST_MISMATCH", "Repository subplan digest is invalid.",
			))
		}
	}
	targets := make([]string, 0, len(plan.Subplans))
	ready := 0
	blocked := 0
	ordered, orderErr := dependencyOrder(plan.Subplans)
	if orderErr != nil {
		findings = append(findings, planFinding("GDS_PORTFOLIO_DEPENDENCY_INVALID", orderErr.Error()))
	}
	for index, subplan := range plan.Subplans {
		if orderErr == nil && ordered[index].RepositoryID != subplan.RepositoryID {
			findings = append(findings, planFinding("GDS_PORTFOLIO_SUBPLAN_ORDER_INVALID", "Repository subplans are not in deterministic dependency order."))
		}
		targets = append(targets, subplan.RepositoryID)
		if subplan.Status == "ready" {
			ready++
		} else {
			blocked++
		}
	}
	sort.Strings(targets)
	targetDigest, _ := canonicaljson.Digest(targets)
	if targetDigest != plan.TargetSetDigest || ready != plan.ReadyCount || blocked != plan.BlockedCount {
		findings = append(findings, planFinding(
			"GDS_PORTFOLIO_PLAN_SUMMARY_MISMATCH", "Portfolio target digest or summary counts are invalid.",
		))
	}
	return findings
}

func dependencyOrder(values []Subplan) ([]Subplan, error) {
	byID := map[string]Subplan{}
	incoming := map[string]int{}
	outgoing := map[string][]string{}
	for _, item := range values {
		if item.RepositoryID == "" || byID[item.RepositoryID].RepositoryID != "" {
			return nil, fmt.Errorf("duplicate or empty repository identity")
		}
		byID[item.RepositoryID] = item
		incoming[item.RepositoryID] = 0
	}
	for _, item := range values {
		seen := map[string]bool{}
		for _, dependency := range item.DependsOn {
			if dependency == item.RepositoryID || byID[dependency].RepositoryID == "" || seen[dependency] {
				return nil, fmt.Errorf("invalid dependency %s -> %s", dependency, item.RepositoryID)
			}
			seen[dependency] = true
			incoming[item.RepositoryID]++
			outgoing[dependency] = append(outgoing[dependency], item.RepositoryID)
		}
	}
	ready := []string{}
	for id, count := range incoming {
		if count == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	result := make([]Subplan, 0, len(values))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		result = append(result, byID[id])
		sort.Strings(outgoing[id])
		for _, next := range outgoing[id] {
			incoming[next]--
			if incoming[next] == 0 {
				ready = append(ready, next)
				sort.Strings(ready)
			}
		}
	}
	if len(result) != len(values) {
		return nil, fmt.Errorf("dependency graph contains a cycle")
	}
	return result, nil
}

func planDigest(plan Plan) (string, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	value, err := serialization.Decode("portfolio-plan.json", raw)
	if err != nil {
		return "", err
	}
	return canonicaljson.DigestObjectWithoutField(value.(map[string]any), "plan_digest")
}

func planFinding(code string, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
