package assurance

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
)

type PerformanceMode string

const (
	DeterministicRequired PerformanceMode = "deterministic-required"
	RelativeRequired      PerformanceMode = "relative-required"
	AbsoluteCalibrated    PerformanceMode = "absolute-calibrated"
	Informational         PerformanceMode = "informational"
)

func ValidPerformanceMode(mode PerformanceMode) bool {
	return mode == DeterministicRequired || mode == RelativeRequired ||
		mode == AbsoluteCalibrated || mode == Informational
}

// ReportAcceptable uses an explicit oracle mode; ambient CI or race settings
// never weaken a required gate.
func ReportAcceptable(report Report, mode PerformanceMode) bool {
	if !ValidPerformanceMode(mode) || (report.Status != "pass" && report.Status != "fail") {
		return false
	}
	if mode == Informational {
		return true
	}
	for _, observed := range report.Metrics {
		if observed.Passed {
			continue
		}
		configured, known := budgets[observed.ID]
		if !known || configured.Evidence == budgetEvidenceStable {
			return false
		}
		// Wall-clock metrics are evaluated only by the immutable relative
		// baseline or calibrated absolute policy owned by their explicit mode.
	}
	return true
}

// CompareRelative rejects regressions against an immutable comparable
// baseline. Stable metrics are already covered by deterministic budgets; this
// comparator handles measured metrics without inventing an absolute SLO.
func CompareRelative(candidate, baseline Report, maximumRegression float64) error {
	if maximumRegression < 0 || maximumRegression > 1 || candidate.Scenario != baseline.Scenario {
		return fmt.Errorf("relative performance baseline is not comparable")
	}
	base := map[string]Metric{}
	for _, metric := range baseline.Metrics {
		base[metric.ID] = metric
	}
	for _, current := range candidate.Metrics {
		configured := budgets[current.ID]
		if configured.Evidence != budgetEvidenceWallClock {
			continue
		}
		previous, ok := base[current.ID]
		if !ok || previous.Unit != current.Unit || previous.Comparison != current.Comparison || previous.Observed <= 0 {
			return fmt.Errorf("relative baseline lacks comparable metric %s", current.ID)
		}
		regressed := current.Observed > previous.Observed*(1+maximumRegression)
		if current.Comparison == "at-least" {
			regressed = current.Observed < previous.Observed*(1-maximumRegression)
		}
		if regressed {
			return fmt.Errorf("relative performance regression: %s", current.ID)
		}
	}
	return nil
}

// Baseline binds a measured report to an immutable comparable runner and
// scenario. Its digest is suitable for pinning in policy or signed evidence;
// shared-CI timing must never be promoted by merely setting CI=true.
type Baseline struct {
	SchemaVersion int         `json:"schema_version"`
	BaselineID    string      `json:"baseline_id"`
	GeneratedAt   time.Time   `json:"generated_at"`
	RunnerDigest  string      `json:"runner_digest"`
	Environment   Environment `json:"environment"`
	Scenario      Scenario    `json:"scenario"`
	ReportDigest  string      `json:"report_digest"`
	Digest        string      `json:"digest"`
}

type CalibratedMetric struct {
	ID         string  `json:"id"`
	Unit       string  `json:"unit"`
	Comparison string  `json:"comparison"`
	Threshold  float64 `json:"threshold"`
	Variance   float64 `json:"variance"`
}

type CalibratedPolicy struct {
	SchemaVersion          int                `json:"schema_version"`
	PolicyID               string             `json:"policy_id"`
	GeneratedAt            time.Time          `json:"generated_at"`
	RunnerDigest           string             `json:"runner_digest"`
	Environment            Environment        `json:"environment"`
	Scenario               Scenario           `json:"scenario"`
	SampleCount            int                `json:"sample_count"`
	VarianceEvidenceDigest string             `json:"variance_evidence_digest"`
	Metrics                []CalibratedMetric `json:"metrics"`
	Digest                 string             `json:"digest"`
}

func NewBaseline(id, runnerDigest string, generatedAt time.Time, report Report) (Baseline, error) {
	if id == "" || runnerDigest == "" || report.ResultDigest == "" || !report.Source.WorktreeClean ||
		!ReportAcceptable(report, DeterministicRequired) {
		return Baseline{}, fmt.Errorf("baseline requires deterministic-pass clean immutable report and runner digest")
	}
	value := Baseline{SchemaVersion: 1, BaselineID: id, GeneratedAt: generatedAt.UTC(), RunnerDigest: runnerDigest,
		Environment: report.Environment, Scenario: report.Scenario, ReportDigest: report.ResultDigest}
	digest, err := canonicaljson.Digest(value)
	if err != nil {
		return Baseline{}, err
	}
	value.Digest = digest
	return value, nil
}

func Calibrate(reports []Report, policyID, runnerDigest string, generatedAt time.Time) (CalibratedPolicy, error) {
	if len(reports) < 10 || policyID == "" || runnerDigest == "" {
		return CalibratedPolicy{}, fmt.Errorf("calibration requires at least ten comparable reports")
	}
	first := reports[0]
	digests := make([]string, 0, len(reports))
	seenDigests := map[string]bool{}
	values := map[string][]float64{}
	shapes := map[string]Metric{}
	for _, report := range reports {
		if report.ResultDigest == "" || seenDigests[report.ResultDigest] || !report.Source.WorktreeClean || report.Environment != first.Environment || report.Scenario != first.Scenario ||
			!ReportAcceptable(report, DeterministicRequired) {
			return CalibratedPolicy{}, fmt.Errorf("calibration reports are not clean deterministic-pass comparable evidence")
		}
		digests = append(digests, report.ResultDigest)
		seenDigests[report.ResultDigest] = true
		for _, metric := range report.Metrics {
			if configured, ok := budgets[metric.ID]; ok && configured.Evidence == budgetEvidenceWallClock {
				values[metric.ID] = append(values[metric.ID], metric.Observed)
				shapes[metric.ID] = metric
			}
		}
	}
	sort.Strings(digests)
	varianceDigest, err := canonicaljson.Digest(digests)
	if err != nil {
		return CalibratedPolicy{}, err
	}
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	metrics := make([]CalibratedMetric, 0, len(ids))
	for _, id := range ids {
		samples := values[id]
		if len(samples) != len(reports) {
			return CalibratedPolicy{}, fmt.Errorf("calibration metric %s is missing samples", id)
		}
		mean := 0.0
		for _, value := range samples {
			mean += value
		}
		mean /= float64(len(samples))
		variance := 0.0
		for _, value := range samples {
			variance += (value - mean) * (value - mean)
		}
		variance /= float64(len(samples) - 1)
		coefficient := 0.0
		if mean > 0 {
			coefficient = math.Sqrt(variance) / mean
		}
		maximum := samples[0]
		minimum := samples[0]
		for _, value := range samples[1:] {
			if value > maximum {
				maximum = value
			}
			if value < minimum {
				minimum = value
			}
		}
		margin := math.Max(0.10, 3*coefficient)
		threshold := maximum * (1 + margin)
		if shapes[id].Comparison == "at-least" {
			threshold = minimum * math.Max(0, 1-margin)
		}
		metrics = append(metrics, CalibratedMetric{ID: id, Unit: shapes[id].Unit, Comparison: shapes[id].Comparison, Threshold: threshold, Variance: coefficient})
	}
	policy := CalibratedPolicy{SchemaVersion: 1, PolicyID: policyID, GeneratedAt: generatedAt.UTC(), RunnerDigest: runnerDigest,
		Environment: first.Environment, Scenario: first.Scenario, SampleCount: len(reports), VarianceEvidenceDigest: varianceDigest, Metrics: metrics}
	policy.Digest, err = canonicaljson.Digest(policy)
	return policy, err
}

func ComparePinnedRelative(candidate Report, baselineReport Report, baseline Baseline, runnerDigest string, maximumRegression float64) error {
	copy := baseline
	copy.Digest = ""
	digest, err := canonicaljson.Digest(copy)
	if err != nil || digest != baseline.Digest || baseline.ReportDigest != baselineReport.ResultDigest ||
		baseline.RunnerDigest != runnerDigest || baseline.Environment != candidate.Environment || baseline.Scenario != candidate.Scenario {
		return fmt.Errorf("relative performance baseline evidence is not immutable or comparable")
	}
	return CompareRelative(candidate, baselineReport, maximumRegression)
}

func EvaluateCalibrated(candidate Report, policy CalibratedPolicy, runnerDigest string) error {
	copy := policy
	copy.Digest = ""
	digest, err := canonicaljson.Digest(copy)
	if err != nil || digest != policy.Digest || policy.SchemaVersion != 1 || policy.PolicyID == "" ||
		policy.RunnerDigest != runnerDigest || policy.Environment != candidate.Environment || policy.Scenario != candidate.Scenario ||
		policy.SampleCount < 10 || policy.VarianceEvidenceDigest == "" || len(policy.Metrics) == 0 {
		return fmt.Errorf("absolute performance policy is not calibrated or comparable")
	}
	observed := make(map[string]Metric, len(candidate.Metrics))
	for _, metric := range candidate.Metrics {
		observed[metric.ID] = metric
	}
	seen := map[string]bool{}
	for _, expected := range policy.Metrics {
		current, ok := observed[expected.ID]
		if !ok || seen[expected.ID] || expected.Threshold <= 0 || expected.Variance < 0 || expected.Variance > 1 ||
			expected.Unit != current.Unit || expected.Comparison != current.Comparison {
			return fmt.Errorf("calibrated policy metric %s is invalid", expected.ID)
		}
		seen[expected.ID] = true
		failed := current.Observed > expected.Threshold
		if expected.Comparison == "at-least" {
			failed = current.Observed < expected.Threshold
		}
		if failed {
			return fmt.Errorf("absolute calibrated performance regression: %s", expected.ID)
		}
	}
	return nil
}
