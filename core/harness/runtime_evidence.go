package harness

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maximumRuntimeEvidenceBytes = 8 << 20

var runtimeEvidenceCaseIDs = []string{
	"destructive-implicit-negative",
	"exact-skill-discovery",
	"hook-lifecycle",
	"nested-instruction-discovery",
	"public-private-context-firewall",
	"read-only-explicit-invocation",
	"root-instruction-discovery",
}

var evalMetricIDs = []string{
	"critical-enforcement",
	"discovery-exact-set",
	"explicit-invocation",
	"output-hard-assertions",
	"trigger-near-miss-specificity",
	"trigger-positive-recall",
}

type RuntimeEvidenceEnvironment struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Executable   string `json:"executable"`
	Command      string `json:"command"`
}

type RuntimeEvidenceJudge struct {
	RunID            string   `json:"run_id"`
	Harness          string   `json:"harness"`
	HarnessVersion   string   `json:"harness_version"`
	ModelLabel       string   `json:"model_label"`
	ExecutionProfile string   `json:"execution_profile"`
	Tools            []string `json:"tools"`
	PromptDigest     string   `json:"prompt_digest"`
}

type RuntimeEvidence struct {
	SchemaVersion    int                        `json:"schema_version"`
	ContractVersion  string                     `json:"contract_version"`
	Harness          string                     `json:"harness"`
	HarnessVersion   string                     `json:"harness_version"`
	ModelLabel       string                     `json:"model_label"`
	ExecutionProfile string                     `json:"execution_profile"`
	Tools            []string                   `json:"tools"`
	Environment      RuntimeEvidenceEnvironment `json:"environment"`
	Judge            RuntimeEvidenceJudge       `json:"judge"`
	ProfileDigest    string                     `json:"profile_digest"`
	StartedAt        time.Time                  `json:"started_at"`
	FinishedAt       time.Time                  `json:"finished_at"`
	Cases            []EvalCaseResult           `json:"cases"`
	Metrics          []EvalMetric               `json:"metrics"`
	Transcripts      []EvalTranscript           `json:"transcripts"`
	ResultDigest     string                     `json:"result_digest"`
}

type runtimeEvidenceExpectation struct {
	Root             string
	SkillProfile     string
	Harness          string
	HarnessVersion   string
	Command          string
	Executable       string
	ModelLabel       string
	ExecutionProfile string
	Tools            []string
	ContractVersion  string
	ProfileDigest    string
	HooksSupported   bool
}

// expectedRuntimeEvidenceCaseIDs is the exact case set a harness must prove.
// A harness whose capability profile declares no hook support
// legitimately omits the hook-lifecycle case rather than being forced to prove
// a capability it does not have; every hook-capable harness (codex, claude)
// keeps the full set unchanged.
func expectedRuntimeEvidenceCaseIDs(hooksSupported bool) []string {
	if hooksSupported {
		return runtimeEvidenceCaseIDs
	}
	expected := make([]string, 0, len(runtimeEvidenceCaseIDs))
	for _, id := range runtimeEvidenceCaseIDs {
		if id == "hook-lifecycle" {
			continue
		}
		expected = append(expected, id)
	}
	return expected
}

func loadRuntimeEvidence(
	path string,
	expectation runtimeEvidenceExpectation,
	schemas *validation.Set,
) (RuntimeEvidence, []domain.Finding) {
	raw, err := readBoundedRegular(path, maximumRuntimeEvidenceBytes)
	if err != nil {
		return RuntimeEvidence{}, []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_READ_FAILED",
			"Cannot read bounded native runtime evidence.", err,
		)}
	}
	return validateRuntimeEvidenceBytes(raw, path, filepath.Dir(path), expectation, schemas)
}

func validateRuntimeEvidenceBytes(
	raw []byte,
	source string,
	directory string,
	expectation runtimeEvidenceExpectation,
	schemas *validation.Set,
) (RuntimeEvidence, []domain.Finding) {
	evidence := RuntimeEvidence{}
	value, err := serialization.Decode(source, raw)
	if err != nil {
		return evidence, []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_DECODE_FAILED",
			"Cannot decode native runtime evidence.", err,
		)}
	}
	findings := schemas.Validate("harness-runtime-evidence", value, source)
	if len(findings) != 0 {
		return evidence, findings
	}
	if err := serialization.DecodeInto(source, raw, &evidence); err != nil {
		return evidence, []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_DECODE_FAILED",
			"Cannot bind native runtime evidence to its typed contract.", err,
		)}
	}
	findings = append(findings, validateRuntimeEvidenceIdentity(evidence, value, expectation)...)
	findings = append(findings, validateRuntimeEvidenceTranscripts(
		evidence, directory,
	)...)
	findings = append(findings, validateRuntimeEvidenceMetrics(evidence)...)
	plan, planFindings := loadRuntimeEvalPlan(expectation.Root, expectation.SkillProfile)
	findings = append(findings, planFindings...)
	if len(planFindings) == 0 {
		findings = append(findings, validateRuntimeEvalCoverage(evidence, plan)...)
		expectedCounts := runtimeEvalPlanCounts(plan)
		for _, metric := range evidence.Metrics {
			if metric.Attempted != expectedCounts[metric.ID] {
				findings = append(findings, runtimeEvidenceFinding(
					"GDS_HARNESS_RUNTIME_METRIC_COVERAGE_INVALID",
					"Metric attempt count differs from the canonical evaluation corpus.",
					map[string]any{
						"metric": metric.ID, "expected": expectedCounts[metric.ID],
						"observed": metric.Attempted,
					},
				))
			}
		}
	}
	sortFindings(findings)
	return evidence, findings
}

func validateRuntimeEvidenceIdentity(
	evidence RuntimeEvidence,
	value any,
	expectation runtimeEvidenceExpectation,
) []domain.Finding {
	findings := []domain.Finding{}
	object, ok := value.(map[string]any)
	digest := ""
	var digestErr error
	if ok {
		digest, digestErr = canonicaljson.DigestObjectWithoutField(object, "result_digest")
	}
	if !ok || digestErr != nil || digest != evidence.ResultDigest {
		findings = append(findings, runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_DIGEST_MISMATCH",
			"Native runtime evidence digest does not match its canonical content.", nil,
		))
	}
	if evidence.Harness != expectation.Harness ||
		evidence.HarnessVersion != expectation.HarnessVersion ||
		evidence.ModelLabel != expectation.ModelLabel ||
		evidence.ExecutionProfile != expectation.ExecutionProfile ||
		evidence.ContractVersion != expectation.ContractVersion ||
		evidence.ProfileDigest != expectation.ProfileDigest ||
		!slices.Equal(evidence.Tools, expectation.Tools) {
		findings = append(findings, runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_IDENTITY_MISMATCH",
			"Runtime evidence is not bound to the exact requested harness, model, profile, tools, and contracts.",
			map[string]any{"harness": evidence.Harness},
		))
	}
	if evidence.ModelLabel == "not-proven" ||
		!slices.Equal(evidence.Tools, normalizedTools(evidence.Tools)) {
		findings = append(findings, runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_METADATA_INVALID",
			"Runtime evidence requires an exact model label and canonically ordered tool identities.", nil,
		))
	}
	if !slices.Contains(CanonicalIDs, evidence.Judge.Harness) ||
		evidence.Judge.RunID == "" || evidence.Judge.HarnessVersion == "" ||
		evidence.Judge.ModelLabel == "not-proven" ||
		evidence.Judge.ExecutionProfile == "" ||
		!slices.Equal(evidence.Judge.Tools, normalizedTools(evidence.Judge.Tools)) {
		findings = append(findings, runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_JUDGE_METADATA_INVALID",
			"Semantic output assertions require one exact supported judge identity and canonically ordered tools.",
			map[string]any{"judge_harness": evidence.Judge.Harness},
		))
	}
	executable, executableErr := filepath.Abs(evidence.Environment.Executable)
	expectedExecutable, expectedErr := filepath.Abs(expectation.Executable)
	if executableErr != nil || expectedErr != nil || filepath.Clean(executable) != filepath.Clean(expectedExecutable) ||
		evidence.Environment.Command != expectation.Command || evidence.Environment.OS != runtime.GOOS ||
		evidence.Environment.Architecture != runtime.GOARCH {
		findings = append(findings, runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_ENVIRONMENT_MISMATCH",
			"Runtime evidence environment differs from the executable and platform observed by this evaluation.",
			map[string]any{"os": evidence.Environment.OS, "architecture": evidence.Environment.Architecture},
		))
	}
	if evidence.StartedAt.IsZero() || evidence.FinishedAt.IsZero() ||
		evidence.FinishedAt.Before(evidence.StartedAt) {
		findings = append(findings, runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_TIME_INVALID",
			"Runtime evidence has an invalid execution interval.", nil,
		))
	}
	caseIDs := make([]string, 0, len(evidence.Cases))
	for _, item := range evidence.Cases {
		caseIDs = append(caseIDs, item.ID)
	}
	sort.Strings(caseIDs)
	expectedCaseIDs := expectedRuntimeEvidenceCaseIDs(expectation.HooksSupported)
	if !slices.Equal(caseIDs, expectedCaseIDs) {
		findings = append(findings, runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_CASE_SET_INVALID",
			"Runtime evidence does not contain the exact model-dependent case set.",
			map[string]any{"expected": expectedCaseIDs, "observed": caseIDs},
		))
	}
	metricIDs := make([]string, 0, len(evidence.Metrics))
	for _, item := range evidence.Metrics {
		metricIDs = append(metricIDs, item.ID)
	}
	sort.Strings(metricIDs)
	if !slices.Equal(metricIDs, evalMetricIDs) {
		findings = append(findings, runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_METRIC_SET_INVALID",
			"Runtime evidence does not contain the exact quality metric set.",
			map[string]any{"expected": evalMetricIDs, "observed": metricIDs},
		))
	}
	return findings
}

func validateRuntimeEvidenceTranscripts(
	evidence RuntimeEvidence,
	directory string,
) []domain.Finding {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return []domain.Finding{runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_TRANSCRIPT_ROOT_INVALID",
			"Cannot open the runtime evidence directory as a confined root.",
			map[string]any{"error": err.Error()},
		)}
	}
	defer root.Close()
	findings := []domain.Finding{}
	seenReferences := map[string]struct{}{}
	seenSamples := map[string]struct{}{}
	transcriptsByDigest := map[string]EvalTranscript{}
	metricCounts := map[string][3]int{}
	for _, transcript := range evidence.Transcripts {
		if transcript.MetricID == nil || transcript.SampleID == nil || transcript.RunIndex == nil ||
			transcript.Passed == nil || transcript.MutationAttempted == nil || transcript.MutationCompleted == nil {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_TRANSCRIPT_METADATA_MISSING",
				"Every native runtime transcript must identify its metric, sample, run, result, and mutation state.",
				map[string]any{"reference": transcript.Reference},
			))
			continue
		}
		if !slices.Contains(runtimeEvidenceCaseIDs, transcript.CaseID) ||
			!slices.Contains(evalMetricIDs, *transcript.MetricID) {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_TRANSCRIPT_SCOPE_INVALID",
				"Transcript case or metric is outside the native runtime contract.",
				map[string]any{"reference": transcript.Reference},
			))
		}
		key := *transcript.MetricID + "\x00" + *transcript.SampleID + fmt.Sprintf("\x00%d", *transcript.RunIndex)
		if _, duplicate := seenSamples[key]; duplicate {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_TRANSCRIPT_SAMPLE_DUPLICATE",
				"Metric sample and run identity occurs more than once.",
				map[string]any{"sample": *transcript.SampleID, "run_index": *transcript.RunIndex},
			))
		}
		seenSamples[key] = struct{}{}
		if *transcript.MutationCompleted && !*transcript.MutationAttempted {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_TRANSCRIPT_MUTATION_INVALID",
				"A completed mutation cannot be reported without a mutation attempt.",
				map[string]any{"reference": transcript.Reference},
			))
		}
		counts := metricCounts[*transcript.MetricID]
		counts[0]++
		if *transcript.Passed {
			counts[1]++
		}
		if *transcript.MutationCompleted {
			counts[2]++
		}
		metricCounts[*transcript.MetricID] = counts
		if _, duplicate := seenReferences[transcript.Reference]; duplicate {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_TRANSCRIPT_REFERENCE_DUPLICATE",
				"A transcript file cannot prove more than one native runtime attempt.",
				map[string]any{"reference": transcript.Reference},
			))
			continue
		}
		seenReferences[transcript.Reference] = struct{}{}
		if filepath.IsAbs(transcript.Reference) || filepath.Clean(transcript.Reference) == "." ||
			strings.HasPrefix(filepath.Clean(transcript.Reference), ".."+string(filepath.Separator)) {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_TRANSCRIPT_PATH_INVALID",
				"Transcript references must be confined relative paths.",
				map[string]any{"reference": transcript.Reference},
			))
			continue
		}
		info, statErr := root.Lstat(filepath.FromSlash(transcript.Reference))
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Size() < 0 || info.Size() > maximumRuntimeEvidenceBytes || int64(transcript.Bytes) != info.Size() {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_TRANSCRIPT_FILE_INVALID",
				"Transcript must be one bounded non-symlink regular file with the declared size.",
				map[string]any{"reference": transcript.Reference},
			))
			continue
		}
		file, openErr := root.Open(filepath.FromSlash(transcript.Reference))
		if openErr != nil {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_TRANSCRIPT_READ_FAILED",
				"Cannot open a confined runtime transcript.",
				map[string]any{"reference": transcript.Reference},
			))
			continue
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, io.LimitReader(file, maximumRuntimeEvidenceBytes+1))
		closeErr := file.Close()
		actualDigest := fmt.Sprintf("sha256:%x", hash.Sum(nil))
		if copyErr != nil || closeErr != nil || actualDigest != transcript.Digest {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_TRANSCRIPT_DIGEST_MISMATCH",
				"Transcript SHA-256 does not match the declared evidence digest.",
				map[string]any{"reference": transcript.Reference},
			))
			continue
		}
		transcriptsByDigest[transcript.Digest] = transcript
	}
	for _, runtimeCase := range evidence.Cases {
		linked := 0
		passed := true
		for _, item := range runtimeCase.Evidence {
			transcript, found := transcriptsByDigest[item.Digest]
			if !found {
				continue
			}
			linked++
			if transcript.Passed == nil || !*transcript.Passed ||
				transcript.MutationAttempted == nil || *transcript.MutationAttempted ||
				transcript.MutationCompleted == nil || *transcript.MutationCompleted {
				passed = false
			}
		}
		if linked == 0 {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_CASE_TRANSCRIPT_MISSING",
				"Every runtime case must bind at least one verified transcript digest.",
				map[string]any{"case": runtimeCase.ID},
			))
			continue
		}
		expectedStatus := "fail"
		if passed {
			expectedStatus = "pass"
		}
		if runtimeCase.Status != expectedStatus {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_CASE_STATUS_INVALID",
				"Runtime case status does not match its transcript results and mutation evidence.",
				map[string]any{"case": runtimeCase.ID, "expected": expectedStatus},
			))
		}
	}
	for _, metric := range evidence.Metrics {
		counts := metricCounts[metric.ID]
		forbidden := 0
		if metric.ForbiddenSuccesses != nil {
			forbidden = *metric.ForbiddenSuccesses
		}
		if metric.Attempted != counts[0] || metric.Passed != counts[1] ||
			(metric.ID == "critical-enforcement" && forbidden != counts[2]) {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_METRIC_TRANSCRIPT_MISMATCH",
				"Metric totals do not match their exact transcript attempts.",
				map[string]any{"metric": metric.ID},
			))
		}
	}
	return findings
}

func validateRuntimeEvidenceMetrics(evidence RuntimeEvidence) []domain.Finding {
	findings := []domain.Finding{}
	for _, metric := range evidence.Metrics {
		if metric.Attempted == 0 {
			if metric.ID != "trigger-positive-recall" || metric.Passed != 0 ||
				metric.Rate != nil || metric.Status != "not-applicable" {
				findings = append(findings, runtimeEvidenceFinding(
					"GDS_HARNESS_RUNTIME_METRIC_NOT_APPLICABLE_INVALID",
					"Only implicit-trigger recall may be not applicable when a profile has no implicit skills.",
					map[string]any{"metric": metric.ID},
				))
			}
			continue
		}
		if metric.Passed > metric.Attempted || metric.Status == "not-applicable" {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_METRIC_COUNTS_INVALID",
				"Every runtime metric requires one or more attempts and valid pass counts.",
				map[string]any{"metric": metric.ID},
			))
			continue
		}
		rate := float64(metric.Passed) / float64(metric.Attempted)
		if metric.Rate == nil || *metric.Rate != rate {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_METRIC_RATE_INVALID",
				"Metric rate must be derived exactly from passed and attempted counts.",
				map[string]any{"metric": metric.ID},
			))
		}
		passes := metric.Passed == metric.Attempted
		if metric.Threshold != nil {
			passes = rate >= *metric.Threshold
		}
		if metric.ID == "critical-enforcement" {
			passes = passes && metric.ForbiddenSuccesses != nil && *metric.ForbiddenSuccesses == 0
		}
		expected := "fail"
		if passes {
			expected = "pass"
		}
		if metric.Status != expected {
			findings = append(findings, runtimeEvidenceFinding(
				"GDS_HARNESS_RUNTIME_METRIC_STATUS_INVALID",
				"Metric status does not match its deterministic threshold result.",
				map[string]any{"metric": metric.ID, "expected": expected},
			))
		}
	}
	return findings
}

func mergeRuntimeEvidence(
	run *EvalRun,
	cases map[string]EvalCaseResult,
	evidence RuntimeEvidence,
) {
	for _, item := range evidence.Cases {
		setEvalCase(cases, item)
	}
	run.HarnessVersion = stringPointer(evidence.HarnessVersion)
	run.ModelLabel = evidence.ModelLabel
	run.ExecutionProfile = evidence.ExecutionProfile
	run.Tools = append([]string(nil), evidence.Tools...)
	run.Environment = EvalEnvironment{
		OS: evidence.Environment.OS, Architecture: evidence.Environment.Architecture,
		Executable: stringPointer(evidence.Environment.Executable),
		Command:    stringPointer(evidence.Environment.Command),
	}
	judge := evidence.Judge
	judge.Tools = append([]string(nil), evidence.Judge.Tools...)
	run.Judge = &judge
	run.StartedAt = evidence.StartedAt
	run.FinishedAt = evidence.FinishedAt
	run.Metrics = orderedEvalMetrics(evidence.Metrics)
	run.Transcripts = append([]EvalTranscript(nil), evidence.Transcripts...)
}

func orderedEvalMetrics(metrics []EvalMetric) []EvalMetric {
	byID := make(map[string]EvalMetric, len(metrics))
	for _, metric := range metrics {
		byID[metric.ID] = metric
	}
	result := make([]EvalMetric, 0, len(metrics))
	for _, expected := range defaultEvalMetrics() {
		result = append(result, byID[expected.ID])
	}
	return result
}

func runtimeEvidenceDigest(evidence RuntimeEvidence) (string, error) {
	evidence.ResultDigest = ""
	raw, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	value, err := serialization.Decode("harness-runtime-evidence.json", raw)
	if err != nil {
		return "", err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("runtime evidence root is not an object")
	}
	return canonicaljson.DigestObjectWithoutField(object, "result_digest")
}

func readBoundedRegular(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("input is not a bounded non-symlink regular file: %s", path)
	}
	return os.ReadFile(path)
}

func runtimeEvidenceFinding(code, message string, evidence map[string]any) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence}
}
