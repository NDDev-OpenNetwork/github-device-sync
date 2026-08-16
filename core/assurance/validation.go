package assurance

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

var expectedCheckIDs = []string{
	"bounded-resource-contract",
	"context-resolution",
	"estate-compilation",
	"fork-classification",
	"installation-outage-isolation",
	"kill-switch-contract",
	"mixed-access-states",
	"mixed-lifecycles",
	"portfolio-planning",
	"projection-generation",
	"reconciliation-persistence",
	"repository-status",
	"rollout-planning",
	"shared-module-relationships",
	"webhook-load",
	"worker-restart",
}

var expectedMetricIDs = []string{
	"api-read-calls-per-full-reconciliation",
	"context-p95-ms",
	"inventory-compile-ms",
	"peak-heap-bytes",
	"portfolio-plan-ms",
	"projection-generation-ms",
	"queue-max-lag-ms",
	"reconciliation-ms",
	"repository-status-p95-ms",
	"restart-recovery-ms",
	"rollout-plan-ms",
	"state-db-bytes",
	"webhook-throughput-per-second",
}

func Validate(report Report, schemas *validation.Set) []domain.Finding {
	findings := []domain.Finding{}
	raw, err := json.Marshal(report)
	if err != nil {
		return []domain.Finding{assuranceFinding(
			"GDS_ASSURANCE_REPORT_ENCODE_FAILED", "Assurance report cannot be encoded.",
		)}
	}
	value, err := serialization.Decode("assurance-report.json", raw)
	if err != nil {
		return []domain.Finding{assuranceFinding(
			"GDS_ASSURANCE_REPORT_DECODE_FAILED", "Assurance report cannot be decoded.",
		)}
	}
	findings = append(findings, schemas.Validate(
		"assurance-report", value, "assurance-report.json",
	)...)
	if len(findings) != 0 {
		return findings
	}
	if !report.FinishedAt.After(report.StartedAt) || report.FinishedAt.After(time.Now().UTC().Add(time.Minute)) {
		findings = append(findings, assuranceFinding(
			"GDS_ASSURANCE_TIME_INVALID", "Assurance timestamps are not ordered or plausible.",
		))
	}
	if report.Bounds.RequireCleanWorktree && !report.Source.WorktreeClean {
		findings = append(findings, assuranceFinding(
			"GDS_ASSURANCE_SOURCE_DIRTY", "Accepted assurance evidence requires a clean source worktree.",
		))
	}
	if !sameExactIDs(checkIDs(report.Checks), expectedCheckIDs) {
		findings = append(findings, assuranceFinding(
			"GDS_ASSURANCE_CHECK_SET_INVALID", "Assurance check identities are incomplete or duplicated.",
		))
	}
	if !sameExactIDs(metricIDs(report.Metrics), expectedMetricIDs) {
		findings = append(findings, assuranceFinding(
			"GDS_ASSURANCE_METRIC_SET_INVALID", "Assurance metric identities are incomplete or duplicated.",
		))
	}
	allPass := true
	for _, check := range report.Checks {
		if check.Status != "pass" {
			allPass = false
		}
	}
	for _, observed := range report.Metrics {
		configured, found := budgets[observed.ID]
		if !found || observed.Unit != configured.Unit ||
			observed.Comparison != configured.Comparison || observed.Limit != configured.Limit {
			findings = append(findings, assuranceFinding(
				"GDS_ASSURANCE_BUDGET_DRIFT", fmt.Sprintf("Metric %s does not use the canonical budget.", observed.ID),
			))
			allPass = false
			continue
		}
		expectedPass := observed.Observed <= observed.Limit
		if observed.Comparison == "at-least" {
			expectedPass = observed.Observed >= observed.Limit
		}
		if observed.Passed != expectedPass {
			findings = append(findings, assuranceFinding(
				"GDS_ASSURANCE_METRIC_RESULT_INVALID", fmt.Sprintf("Metric %s pass state is inconsistent.", observed.ID),
			))
		}
		if !observed.Passed {
			allPass = false
		}
	}
	if (report.Status == "pass") != allPass {
		findings = append(findings, assuranceFinding(
			"GDS_ASSURANCE_STATUS_INVALID", "Assurance status does not match its checks and budgets.",
		))
	}
	expectedDigest, err := reportDigest(report)
	if err != nil || report.ResultDigest != expectedDigest {
		findings = append(findings, assuranceFinding(
			"GDS_ASSURANCE_DIGEST_MISMATCH", "Assurance result digest is invalid.",
		))
	}
	sort.Slice(findings, func(left, right int) bool {
		return findings[left].Code < findings[right].Code
	})
	return findings
}

func reportDigest(report Report) (string, error) {
	report.ResultDigest = ""
	return canonicaljson.Digest(report)
}

func checkIDs(checks []Check) []string {
	result := make([]string, len(checks))
	for index, check := range checks {
		result[index] = check.ID
	}
	return result
}

func metricIDs(metrics []Metric) []string {
	result := make([]string, len(metrics))
	for index, metric := range metrics {
		result[index] = metric.ID
	}
	return result
}

func sameExactIDs(observed, expected []string) bool {
	if len(observed) != len(expected) {
		return false
	}
	observed = append([]string(nil), observed...)
	sort.Strings(observed)
	for index := range expected {
		if observed[index] != expected[index] {
			return false
		}
	}
	return true
}

func assuranceFinding(code, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
