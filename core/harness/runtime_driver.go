package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/redaction"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const (
	defaultRuntimeDriverTimeout = 30 * time.Minute
	maximumRuntimeDriverTimeout = 2 * time.Hour
	maximumRuntimeDriverStderr  = 64 << 10
)

type RuntimeDriverRequest struct {
	SchemaVersion     int                        `json:"schema_version"`
	Harness           string                     `json:"harness"`
	HarnessVersion    string                     `json:"harness_version"`
	ModelLabel        string                     `json:"model_label"`
	ExecutionProfile  string                     `json:"execution_profile"`
	Tools             []string                   `json:"tools"`
	Environment       RuntimeEvidenceEnvironment `json:"environment"`
	GDSExecutable     string                     `json:"gds_executable"`
	SkillProfile      string                     `json:"skill_profile"`
	ContractVersion   string                     `json:"contract_version"`
	ProfileDigest     string                     `json:"profile_digest"`
	RepositoryRoot    string                     `json:"repository_root"`
	EvidenceDirectory string                     `json:"evidence_directory"`
	ProfilePath       string                     `json:"profile_path"`
	RuntimeContract   string                     `json:"runtime_contract"`
	TriggerCorpus     string                     `json:"trigger_corpus"`
	OutputCorpus      string                     `json:"output_corpus"`
	EnforcementCorpus string                     `json:"enforcement_corpus"`
	EvidenceSchema    string                     `json:"evidence_schema"`
}

type runtimeDriverOptions struct {
	Path              string
	EvidenceDirectory string
	Timeout           time.Duration
	SkillProfile      string
}

func runRuntimeDriver(
	ctx context.Context,
	root string,
	options runtimeDriverOptions,
	expectation runtimeEvidenceExpectation,
	schemas *validation.Set,
) (RuntimeEvidence, []domain.Finding) {
	driverPath, evidenceDirectory, findings := validateRuntimeDriverInputs(options)
	if len(findings) != 0 {
		return RuntimeEvidence{}, findings
	}
	request := RuntimeDriverRequest{
		SchemaVersion: 1, Harness: expectation.Harness,
		HarnessVersion: expectation.HarnessVersion, ModelLabel: expectation.ModelLabel,
		ExecutionProfile: expectation.ExecutionProfile,
		Tools:            append([]string(nil), expectation.Tools...),
		Environment: RuntimeEvidenceEnvironment{
			OS: runtime.GOOS, Architecture: runtime.GOARCH,
			Executable: expectation.Executable, Command: expectation.Command,
		},
		SkillProfile: options.SkillProfile, ContractVersion: expectation.ContractVersion,
		ProfileDigest: expectation.ProfileDigest, RepositoryRoot: root,
		EvidenceDirectory: evidenceDirectory,
		ProfilePath:       filepath.Join(root, "harnesses", expectation.Harness, "profile.yaml"),
		RuntimeContract:   filepath.Join(root, "tests", "harness", "runtime-contract.yaml"),
		TriggerCorpus:     filepath.Join(root, "skills", "evals", "trigger", options.SkillProfile+".json"),
		OutputCorpus:      filepath.Join(root, "skills", "evals", "output", options.SkillProfile+".json"),
		EnforcementCorpus: filepath.Join(root, "skills", "evals", "enforcement", "common.json"),
		EvidenceSchema:    filepath.Join(root, "schemas", "v1", "harness-runtime-evidence.schema.json"),
	}
	gdsExecutable, err := os.Executable()
	if err != nil {
		return RuntimeEvidence{}, []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_DRIVER_REQUEST_FAILED", "Cannot resolve the active GDS executable.", err,
		)}
	}
	gdsExecutable, err = filepath.EvalSymlinks(gdsExecutable)
	if err != nil {
		return RuntimeEvidence{}, []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_DRIVER_REQUEST_FAILED", "Cannot resolve the active GDS executable identity.", err,
		)}
	}
	request.GDSExecutable = filepath.Clean(gdsExecutable)
	requestRaw, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return RuntimeEvidence{}, []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_DRIVER_REQUEST_FAILED", "Cannot encode the runtime driver request.", err,
		)}
	}
	requestRaw = append(requestRaw, '\n')
	requestPath := filepath.Join(evidenceDirectory, "driver-request.json")
	if err := writeExclusiveRegular(requestPath, requestRaw); err != nil {
		return RuntimeEvidence{}, []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_DRIVER_REQUEST_FAILED", "Cannot persist the exact runtime driver request.", err,
		)}
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultRuntimeDriverTimeout
	}
	driverContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(driverContext, driverPath)
	command.Dir = evidenceDirectory
	command.Env = runtimeDriverEnvironment()
	command.Stdin = bytes.NewReader(requestRaw)
	stdout := &strictLimitedBuffer{remaining: maximumRuntimeEvidenceBytes}
	stderr := &strictLimitedBuffer{remaining: maximumRuntimeDriverStderr}
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	if err != nil {
		message := "Native runtime driver failed without producing acceptable evidence."
		if driverContext.Err() != nil {
			message = "Native runtime driver exceeded its bounded execution timeout."
		}
		return RuntimeEvidence{}, []domain.Finding{runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_DRIVER_FAILED", message,
			map[string]any{
				"driver": filepath.Base(driverPath), "error_type": fmt.Sprintf("%T", err),
				"stderr": redaction.String(strings.TrimSpace(stderr.String())),
			},
		)}
	}
	evidence, evidenceFindings := validateRuntimeEvidenceBytes(
		stdout.Bytes(), "runtime-driver-stdout.json", evidenceDirectory, expectation, schemas,
	)
	if len(evidenceFindings) != 0 {
		return evidence, evidenceFindings
	}
	if err := writeExclusiveRegular(
		filepath.Join(evidenceDirectory, "runtime-evidence.json"), stdout.Bytes(),
	); err != nil {
		return RuntimeEvidence{}, []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_WRITE_FAILED",
			"Cannot persist validated native runtime evidence without overwrite.", err,
		)}
	}
	return evidence, nil
}

func validateRuntimeDriverInputs(
	options runtimeDriverOptions,
) (string, string, []domain.Finding) {
	driverPath, err := filepath.Abs(options.Path)
	if err != nil {
		return "", "", []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_DRIVER_INVALID", "Cannot resolve the runtime driver path.", err,
		)}
	}
	driverInfo, err := os.Lstat(driverPath)
	if err != nil || !driverInfo.Mode().IsRegular() || driverInfo.Mode()&os.ModeSymlink != 0 ||
		driverInfo.Mode()&0o111 == 0 {
		return "", "", []domain.Finding{runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_DRIVER_INVALID",
			"Runtime driver must be one executable non-symlink regular file.",
			map[string]any{"path": driverPath},
		)}
	}
	evidenceDirectory, err := filepath.Abs(options.EvidenceDirectory)
	if err != nil {
		return "", "", []domain.Finding{evalFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_DIRECTORY_INVALID",
			"Cannot resolve the runtime evidence directory.", err,
		)}
	}
	directoryInfo, err := os.Lstat(evidenceDirectory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", []domain.Finding{runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_DIRECTORY_INVALID",
			"Runtime evidence directory must be one existing non-symlink directory.",
			map[string]any{"path": evidenceDirectory},
		)}
	}
	entries, err := os.ReadDir(evidenceDirectory)
	if err != nil || len(entries) != 0 {
		return "", "", []domain.Finding{runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_EVIDENCE_DIRECTORY_NOT_EMPTY",
			"Runtime driver requires an empty evidence directory to prevent overwrite and evidence mixing.",
			map[string]any{"path": evidenceDirectory},
		)}
	}
	if options.Timeout < 0 || options.Timeout > maximumRuntimeDriverTimeout {
		return "", "", []domain.Finding{runtimeEvidenceFinding(
			"GDS_HARNESS_RUNTIME_DRIVER_TIMEOUT_INVALID",
			"Runtime driver timeout must be positive and no greater than two hours.", nil,
		)}
	}
	return filepath.Clean(driverPath), filepath.Clean(evidenceDirectory), nil
}

func runtimeDriverEnvironment() []string {
	allowed := map[string]struct{}{
		"CLAUDE_CONFIG_DIR": {}, "CODEX_HOME": {}, "HOME": {}, "KIMI_CODE_HOME": {},
		"LANG": {}, "LC_ALL": {}, "PATH": {}, "SHELL": {}, "TMPDIR": {}, "USER": {}, "ZCODE_HOME": {},
		"XDG_CACHE_HOME": {}, "XDG_CONFIG_HOME": {}, "XDG_DATA_HOME": {}, "XDG_STATE_HOME": {},
	}
	result := []string{"CI=1", "GDS_HARNESS_EVAL=1", "NO_COLOR=1"}
	for _, item := range os.Environ() {
		key, _, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if _, accepted := allowed[key]; accepted {
			result = append(result, item)
		}
	}
	sort.Strings(result)
	return result
}

func writeExclusiveRegular(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

type strictLimitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (buffer *strictLimitedBuffer) Write(value []byte) (int, error) {
	if len(value) > buffer.remaining {
		return 0, errors.New("runtime driver output exceeds its configured bound")
	}
	written, err := buffer.buffer.Write(value)
	buffer.remaining -= written
	return written, err
}

func (buffer *strictLimitedBuffer) Bytes() []byte  { return buffer.buffer.Bytes() }
func (buffer *strictLimitedBuffer) String() string { return buffer.buffer.String() }
