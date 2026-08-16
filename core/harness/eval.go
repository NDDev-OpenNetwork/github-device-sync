package harness

import (
	"context"
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
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type EvalOptions struct {
	SkillProfile      string
	ModelLabel        string
	ExecutionProfile  string
	Tools             []string
	RuntimeEvidence   string
	RuntimeDriver     string
	EvidenceDirectory string
	DriverTimeout     time.Duration
}

type EvalEnvironment struct {
	OS           string  `json:"os"`
	Architecture string  `json:"architecture"`
	Executable   *string `json:"executable"`
	Command      *string `json:"command"`
}

type EvalProfile struct {
	ID                string `json:"id"`
	CapabilityVersion string `json:"capability_version"`
	Digest            string `json:"digest"`
	SkillProfile      string `json:"skill_profile"`
}

type EvalEvidence struct {
	Kind      string `json:"kind"`
	Summary   string `json:"summary"`
	Digest    string `json:"digest"`
	Reference string `json:"reference,omitempty"`
}

type EvalCaseResult struct {
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	Summary  string         `json:"summary"`
	Evidence []EvalEvidence `json:"evidence"`
}

type EvalMetric struct {
	ID                 string   `json:"id"`
	Status             string   `json:"status"`
	Passed             int      `json:"passed"`
	Attempted          int      `json:"attempted"`
	Rate               *float64 `json:"rate,omitempty"`
	Threshold          *float64 `json:"threshold,omitempty"`
	ForbiddenSuccesses *int     `json:"forbidden_successes,omitempty"`
}

type EvalTranscript struct {
	CaseID            string  `json:"case_id"`
	MetricID          *string `json:"metric_id,omitempty"`
	SampleID          *string `json:"sample_id,omitempty"`
	RunIndex          *int    `json:"run_index,omitempty"`
	Passed            *bool   `json:"passed,omitempty"`
	MutationAttempted *bool   `json:"mutation_attempted,omitempty"`
	MutationCompleted *bool   `json:"mutation_completed,omitempty"`
	Reference         string  `json:"reference"`
	Digest            string  `json:"digest"`
	Bytes             int     `json:"bytes"`
}

type EvalRun struct {
	SchemaVersion    int                   `json:"schema_version"`
	EvaluationID     string                `json:"evaluation_id"`
	ContractVersion  string                `json:"contract_version"`
	Harness          string                `json:"harness"`
	HarnessVersion   *string               `json:"harness_version"`
	ModelLabel       string                `json:"model_label"`
	ExecutionProfile string                `json:"execution_profile"`
	Tools            []string              `json:"tools"`
	Environment      EvalEnvironment       `json:"environment"`
	Judge            *RuntimeEvidenceJudge `json:"judge,omitempty"`
	Profile          EvalProfile           `json:"profile"`
	StartedAt        time.Time             `json:"started_at"`
	FinishedAt       time.Time             `json:"finished_at"`
	Cases            []EvalCaseResult      `json:"cases"`
	Metrics          []EvalMetric          `json:"metrics"`
	Transcripts      []EvalTranscript      `json:"transcripts"`
	Result           string                `json:"result"`
	ResultDigest     string                `json:"result_digest"`
}

func Evaluate(
	ctx context.Context,
	root string,
	harnessID string,
	options EvalOptions,
	schemas *validation.Set,
	now time.Time,
	entropy io.Reader,
) (EvalRun, []domain.Finding) {
	started := now.UTC()
	run := EvalRun{
		SchemaVersion: 1, Harness: harnessID, ModelLabel: options.ModelLabel,
		ExecutionProfile: options.ExecutionProfile, Tools: normalizedTools(options.Tools),
		StartedAt: started, FinishedAt: started, Cases: []EvalCaseResult{},
		Metrics: defaultEvalMetrics(), Transcripts: []EvalTranscript{}, Result: "not-proven",
	}
	evaluationID, err := identity.New("eval", started, entropy)
	if err != nil {
		return run, []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_ID_FAILED", "Cannot allocate an evaluation identity.", err,
		)}
	}
	run.EvaluationID = evaluationID
	contract, findings := loadRuntimeContract(root, schemas)
	if len(findings) != 0 {
		return run, findings
	}
	run.ContractVersion = contract.ContractVersion
	profile, _, profileFindings := validateProfile(root, harnessID, schemas, false, resolveDelegation(root, schemas))
	if len(profileFindings) != 0 {
		return run, profileFindings
	}
	profilePath := filepath.Join(root, "harnesses", harnessID, "profile.yaml")
	profileRaw, err := os.ReadFile(profilePath)
	if err != nil {
		return run, []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_PROFILE_READ_FAILED", "Cannot read the exact capability profile.", err,
		)}
	}
	run.Profile = EvalProfile{
		ID: harnessID, CapabilityVersion: profile.CapabilityVersion,
		Digest: bytesDigest(profileRaw), SkillProfile: options.SkillProfile,
	}
	adapter, adapterFindings := NewAdapter(root, harnessID, schemas)
	if len(adapterFindings) != 0 {
		return run, adapterFindings
	}
	observation, detectionFindings := adapter.Detect(ctx)
	findings = append(findings, detectionFindings...)
	run.Environment = EvalEnvironment{OS: runtime.GOOS, Architecture: runtime.GOARCH}
	if observation.Executable != "" {
		run.Environment.Executable = stringPointer(observation.Executable)
	}
	if observation.Command != "" {
		run.Environment.Command = stringPointer(observation.Command)
	}
	if observation.Version != "" {
		run.HarnessVersion = stringPointer(observation.Version)
	}
	caseResults := pendingEvalCases(contract.Cases)
	if len(detectionFindings) == 0 && observation.Result == "observed" {
		setEvalCase(caseResults, EvalCaseResult{
			ID: "binary-version-detection", Status: "pass",
			Summary:  "Exact executable and bounded version output were observed.",
			Evidence: []EvalEvidence{deterministicEvalEvidence("runtime observation", observation)},
		})
	}
	lifecycleDigest, lifecycleErr := evaluateAdapterLifecycle(adapter, options.SkillProfile)
	if lifecycleErr != nil {
		for _, caseID := range []string{"clean-install", "update-and-rollback", "remove"} {
			setEvalCase(caseResults, failedEvalCase(caseID, lifecycleErr))
		}
	} else {
		for _, item := range []struct{ id, summary string }{
			{"clean-install", "Isolated deterministic install matched the exact candidate."},
			{"update-and-rollback", "Update and exact prior-projection rollback both verified."},
			{"remove", "Removal preserved unrelated state and removed every managed file."},
		} {
			setEvalCase(caseResults, EvalCaseResult{
				ID: item.id, Status: "pass", Summary: item.summary,
				Evidence: []EvalEvidence{{
					Kind: "deterministic", Summary: "adapter lifecycle fixture", Digest: lifecycleDigest,
				}},
			})
		}
	}
	driftDigest, driftErr := evaluateAdapterDrift(adapter, options.SkillProfile)
	if driftErr != nil {
		setEvalCase(caseResults, failedEvalCase("generated-projection-drift", driftErr))
	} else {
		setEvalCase(caseResults, EvalCaseResult{
			ID: "generated-projection-drift", Status: "pass",
			Summary: "Manual managed-file drift was detected without overwrite.",
			Evidence: []EvalEvidence{{
				Kind: "deterministic", Summary: "projection drift fixture", Digest: driftDigest,
			}},
		})
	}
	if (options.RuntimeEvidence != "" || options.RuntimeDriver != "") &&
		len(detectionFindings) == 0 && observation.Result == "observed" {
		expectation := runtimeEvidenceExpectation{
			Root: root, SkillProfile: options.SkillProfile,
			Harness: harnessID, HarnessVersion: observation.Version,
			Command: observation.Command, Executable: observation.Executable,
			ModelLabel: options.ModelLabel, ExecutionProfile: options.ExecutionProfile,
			Tools: normalizedTools(options.Tools), ContractVersion: contract.ContractVersion,
			ProfileDigest: run.Profile.Digest, HooksSupported: profile.Hooks.Supported,
		}
		var evidence RuntimeEvidence
		var evidenceFindings []domain.Finding
		if options.RuntimeDriver != "" {
			evidence, evidenceFindings = runRuntimeDriver(
				ctx, root,
				runtimeDriverOptions{
					Path: options.RuntimeDriver, EvidenceDirectory: options.EvidenceDirectory,
					Timeout: options.DriverTimeout, SkillProfile: options.SkillProfile,
				},
				expectation, schemas,
			)
		} else {
			evidence, evidenceFindings = loadRuntimeEvidence(
				options.RuntimeEvidence, expectation, schemas,
			)
		}
		findings = append(findings, evidenceFindings...)
		if len(evidenceFindings) == 0 {
			mergeRuntimeEvidence(&run, caseResults, evidence)
		}
	}
	run.Cases = orderedEvalCases(caseResults)
	run.Result = aggregateEvalResult(run.Cases, run.Metrics)
	digest, err := evalRunDigest(run)
	if err != nil {
		return run, []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_DIGEST_FAILED", "Cannot digest evaluation evidence.", err,
		)}
	}
	run.ResultDigest = digest
	findings = append(findings, validateEvalRun(run, schemas)...)
	if run.Result == "not-proven" {
		findings = append(findings, domain.Finding{
			Code: "GDS_HARNESS_EVAL_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  "Required model-dependent runtime cases and metrics remain unproven.",
			Evidence: map[string]any{"harness": harnessID, "evaluation_id": run.EvaluationID},
		})
	}
	if run.Result == "fail" {
		findings = append(findings, domain.Finding{
			Code: "GDS_HARNESS_EVAL_FAILED", Severity: domain.SeverityHigh,
			Message:  "At least one required harness evaluation case failed.",
			Evidence: map[string]any{"harness": harnessID, "evaluation_id": run.EvaluationID},
		})
	}
	sortFindings(findings)
	return run, findings
}

func loadRuntimeContract(
	root string,
	schemas *validation.Set,
) (RuntimeContractDocument, []domain.Finding) {
	path := filepath.Join(root, "tests", "harness", "runtime-contract.yaml")
	findings := schemas.ValidateFile("harness-runtime-contract", path)
	if len(findings) != 0 {
		return RuntimeContractDocument{}, findings
	}
	var document RuntimeContractDocument
	raw, err := os.ReadFile(path)
	if err == nil {
		err = serialization.DecodeInto(path, raw, &document)
	}
	if err != nil {
		return document, []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_CONTRACT_READ_FAILED", "Cannot decode the runtime contract.", err,
		)}
	}
	return document, nil
}

func pendingEvalCases(cases []RuntimeContractCase) map[string]EvalCaseResult {
	result := make(map[string]EvalCaseResult, len(cases))
	for _, item := range cases {
		result[item.ID] = EvalCaseResult{
			ID: item.ID, Status: "not-proven",
			Summary:  "No exact runtime transcript or deterministic assertion proves this case.",
			Evidence: []EvalEvidence{},
		}
	}
	return result
}

func evaluateAdapterLifecycle(adapter Adapter, skillProfile string) (string, error) {
	target, err := os.MkdirTemp("", "gds-harness-eval-target-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(target)
	prior, err := os.MkdirTemp("", "gds-harness-eval-prior-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(prior)
	request := RenderRequest{SkillProfile: skillProfile, Scope: "project"}
	installPlan, findings := adapter.PlanInstall(target, request)
	if len(findings) != 0 {
		return "", fmt.Errorf("plan install returned %d findings", len(findings))
	}
	installHandler, err := NewAdapterMaterializer(target, installPlan.Candidate())
	if err != nil {
		return "", err
	}
	if _, err := installHandler.Apply(context.Background(), evalStep(installPlan, MaterializeAdapterAction)); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(target, "user-owned.txt"), []byte("preserve\n"), 0o644); err != nil {
		return "", err
	}
	priorCandidate, findings := adapter.LoadInstalled(target, request)
	if len(findings) != 0 {
		return "", fmt.Errorf("load prior candidate returned %d findings", len(findings))
	}
	priorPlan := AdapterPlan{
		Operation: "install", Harness: priorCandidate.Harness, TargetRoot: prior,
		CandidateDigest: priorCandidate.CandidateDigest, Files: priorCandidate.Files,
		candidate: priorCandidate,
	}
	priorHandler, err := NewAdapterMaterializer(prior, priorCandidate)
	if err != nil {
		return "", err
	}
	if _, err := priorHandler.Apply(context.Background(), evalStep(priorPlan, MaterializeAdapterAction)); err != nil {
		return "", err
	}
	updatePlan, findings := adapter.PlanUpdate(target, request)
	if len(findings) != 0 {
		return "", fmt.Errorf("plan update returned %d findings", len(findings))
	}
	updateHandler, err := NewAdapterUpdater(
		target, "", "update", updatePlan.Candidate(), updatePlan.PreviousCandidate(),
	)
	if err != nil {
		return "", err
	}
	if _, err := updateHandler.Apply(context.Background(), evalStep(updatePlan, UpdateAdapterAction)); err != nil {
		return "", err
	}
	rollbackPlan, findings := adapter.PlanRollback(target, prior, priorCandidate)
	if len(findings) != 0 {
		return "", fmt.Errorf("plan rollback returned %d findings", len(findings))
	}
	rollbackHandler, err := NewAdapterUpdater(
		target, prior, "rollback", rollbackPlan.Candidate(), rollbackPlan.PreviousCandidate(),
	)
	if err != nil {
		return "", err
	}
	if _, err := rollbackHandler.Apply(
		context.Background(), evalStep(rollbackPlan, RollbackAdapterAction),
	); err != nil {
		return "", err
	}
	removePlan, findings := adapter.PlanRemove(target, request)
	if len(findings) != 0 {
		return "", fmt.Errorf("plan remove returned %d findings", len(findings))
	}
	removeHandler, err := NewAdapterRemover(target, removePlan.Candidate())
	if err != nil {
		return "", err
	}
	if _, err := removeHandler.Apply(context.Background(), evalStep(removePlan, RemoveAdapterAction)); err != nil {
		return "", err
	}
	content, err := os.ReadFile(filepath.Join(target, "user-owned.txt"))
	if err != nil || string(content) != "preserve\n" {
		return "", fmt.Errorf("unmanaged state was not preserved")
	}
	return canonicaljson.Digest(map[string]any{
		"install": installPlan.PlanDigest, "update": updatePlan.PlanDigest,
		"rollback": rollbackPlan.PlanDigest, "remove": removePlan.PlanDigest,
	})
}

func evaluateAdapterDrift(adapter Adapter, skillProfile string) (string, error) {
	target, err := os.MkdirTemp("", "gds-harness-eval-drift-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(target)
	request := RenderRequest{SkillProfile: skillProfile, Scope: "project"}
	plan, findings := adapter.PlanInstall(target, request)
	if len(findings) != 0 {
		return "", fmt.Errorf("plan install returned %d findings", len(findings))
	}
	handler, err := NewAdapterMaterializer(target, plan.Candidate())
	if err != nil {
		return "", err
	}
	if _, err := handler.Apply(context.Background(), evalStep(plan, MaterializeAdapterAction)); err != nil {
		return "", err
	}
	driftPath := ""
	for _, file := range plan.Files {
		if strings.HasSuffix(file.Path, "SKILL.md") {
			driftPath = file.Path
			break
		}
	}
	if driftPath == "" {
		return "", fmt.Errorf("candidate contains no skill document")
	}
	absolute := filepath.Join(target, filepath.FromSlash(driftPath))
	if err := os.WriteFile(absolute, []byte("manual drift\n"), 0o644); err != nil {
		return "", err
	}
	inspection, findings := adapter.Inspect(target, request)
	if len(findings) != 0 || inspection.Drift != 1 {
		return "", fmt.Errorf("manual drift count=%d findings=%d", inspection.Drift, len(findings))
	}
	return canonicaljson.Digest(map[string]any{
		"path": driftPath, "fingerprint": inspection.Fingerprint, "drift": inspection.Drift,
	})
}

func evalStep(plan AdapterPlan, action string) operations.Step {
	return operations.Step{
		StepID: plan.Operation + "-eval", RepositoryID: "repo_eval", Action: action,
		Parameters: AdapterParameters(plan),
	}
}

func defaultEvalMetrics() []EvalMetric {
	positiveThreshold := 0.9
	specificityThreshold := 0.95
	zero := 0
	return []EvalMetric{
		{ID: "discovery-exact-set", Status: "not-proven"},
		{ID: "explicit-invocation", Status: "not-proven"},
		{ID: "trigger-positive-recall", Status: "not-proven", Threshold: &positiveThreshold},
		{ID: "trigger-near-miss-specificity", Status: "not-proven", Threshold: &specificityThreshold},
		{ID: "output-hard-assertions", Status: "not-proven"},
		{ID: "critical-enforcement", Status: "not-proven", ForbiddenSuccesses: &zero},
	}
}

func normalizedTools(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return slices.Compact(result)
}

func deterministicEvalEvidence(summary string, value any) EvalEvidence {
	digest, _ := canonicaljson.Digest(value)
	return EvalEvidence{Kind: "runtime", Summary: summary, Digest: digest}
}

func failedEvalCase(id string, err error) EvalCaseResult {
	return EvalCaseResult{
		ID: id, Status: "fail", Summary: "Deterministic evaluation failed: " + err.Error(),
		Evidence: []EvalEvidence{},
	}
}

func setEvalCase(cases map[string]EvalCaseResult, result EvalCaseResult) {
	if _, known := cases[result.ID]; known {
		cases[result.ID] = result
	}
}

func orderedEvalCases(cases map[string]EvalCaseResult) []EvalCaseResult {
	result := make([]EvalCaseResult, 0, len(RuntimeCaseIDs))
	for _, id := range RuntimeCaseIDs {
		result = append(result, cases[id])
	}
	return result
}

func aggregateEvalResult(cases []EvalCaseResult, metrics []EvalMetric) string {
	result := "pass"
	for _, status := range append(caseStatuses(cases), metricStatuses(metrics)...) {
		if status == "fail" {
			return "fail"
		}
		if status == "not-proven" {
			result = "not-proven"
		}
	}
	return result
}

func caseStatuses(cases []EvalCaseResult) []string {
	result := make([]string, 0, len(cases))
	for _, item := range cases {
		result = append(result, item.Status)
	}
	return result
}

func metricStatuses(metrics []EvalMetric) []string {
	result := make([]string, 0, len(metrics))
	for _, item := range metrics {
		result = append(result, item.Status)
	}
	return result
}

func evalRunDigest(run EvalRun) (string, error) {
	run.ResultDigest = ""
	raw, err := json.Marshal(run)
	if err != nil {
		return "", err
	}
	value, err := serialization.Decode("harness-eval-run.json", raw)
	if err != nil {
		return "", err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("evaluation evidence root is not an object")
	}
	return canonicaljson.DigestObjectWithoutField(object, "result_digest")
}

func validateEvalRun(run EvalRun, schemas *validation.Set) []domain.Finding {
	raw, err := json.Marshal(run)
	if err != nil {
		return []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_ENCODE_FAILED", "Cannot encode evaluation evidence.", err,
		)}
	}
	value, err := serialization.Decode("harness-eval-run.json", raw)
	if err != nil {
		return []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_DECODE_FAILED", "Cannot decode evaluation evidence.", err,
		)}
	}
	findings := schemas.Validate("harness-eval-run", value, "in-memory-harness-eval-run")
	caseIDs := make([]string, 0, len(run.Cases))
	for _, item := range run.Cases {
		caseIDs = append(caseIDs, item.ID)
	}
	if !slices.Equal(caseIDs, RuntimeCaseIDs) {
		findings = append(findings, domain.Finding{
			Code: "GDS_HARNESS_EVAL_CASE_SET_INVALID", Severity: domain.SeverityHigh,
			Message: "Evaluation evidence must contain the exact ordered runtime case set.",
		})
	}
	expectedDigest, digestErr := evalRunDigest(run)
	if digestErr != nil || expectedDigest != run.ResultDigest {
		findings = append(findings, domain.Finding{
			Code: "GDS_HARNESS_EVAL_DIGEST_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Evaluation result digest does not match its exact content.",
		})
	}
	return findings
}

func stringPointer(value string) *string { return &value }

func evalFinding(code, message string, err error) domain.Finding {
	if err != nil {
		message += " " + err.Error()
	}
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
