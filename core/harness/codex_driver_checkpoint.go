package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

var runtimeDriverPathToken = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type runtimeDriverAttempt struct {
	SchemaVersion     int                   `json:"schema_version"`
	RequestDigest     string                `json:"request_digest"`
	Kind              runtimeDriverTaskKind `json:"kind"`
	CaseID            string                `json:"case_id"`
	MetricID          string                `json:"metric_id"`
	SampleID          string                `json:"sample_id"`
	RunIndex          int                   `json:"run_index"`
	Passed            bool                  `json:"passed"`
	MutationAttempted bool                  `json:"mutation_attempted"`
	MutationCompleted bool                  `json:"mutation_completed"`
	PromptDigest      string                `json:"prompt_digest"`
	FinalOutput       string                `json:"final_output"`
	SkillReads        []string              `json:"skill_reads"`
	Commands          []string              `json:"commands"`
	SubjectTranscript string                `json:"subject_transcript"`
	JudgeTranscript   string                `json:"judge_transcript,omitempty"`
	Details           map[string]any        `json:"details"`
	Reference         string                `json:"-"`
	Digest            string                `json:"-"`
	Bytes             int                   `json:"-"`
	Task              *runtimeDriverTask    `json:"-"`
}

func runtimeDriverAttemptReference(task runtimeDriverTask) string {
	parts := []string{
		runtimeDriverPathToken.ReplaceAllString(task.MetricID, "-"),
		runtimeDriverPathToken.ReplaceAllString(task.SampleID, "-"),
		fmt.Sprintf("%02d", task.RunIndex),
	}
	return filepath.ToSlash(filepath.Join("transcripts", strings.Join(parts, "--")+".json"))
}

func loadRuntimeDriverAttempt(
	directory, requestDigest string,
	task runtimeDriverTask,
) (runtimeDriverAttempt, bool, error) {
	reference := runtimeDriverAttemptReference(task)
	path := filepath.Join(directory, filepath.FromSlash(reference))
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return runtimeDriverAttempt{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > maximumRuntimeEvidenceBytes {
		return runtimeDriverAttempt{}, false, fmt.Errorf("checkpoint is not one bounded regular file: %s", reference)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return runtimeDriverAttempt{}, false, err
	}
	var attempt runtimeDriverAttempt
	if err := serialization.DecodeInto(path, raw, &attempt); err != nil {
		return runtimeDriverAttempt{}, false, fmt.Errorf("decode checkpoint %s: %w", reference, err)
	}
	if attempt.SchemaVersion != 1 || attempt.RequestDigest != requestDigest ||
		attempt.Kind != task.Kind || attempt.CaseID != task.CaseID ||
		attempt.MetricID != task.MetricID || attempt.SampleID != task.SampleID ||
		attempt.RunIndex != task.RunIndex || attempt.PromptDigest != bytesDigest([]byte(task.Prompt)) {
		return runtimeDriverAttempt{}, false, fmt.Errorf("checkpoint identity mismatch: %s", reference)
	}
	attempt.Reference = reference
	attempt.Digest = bytesDigest(raw)
	attempt.Bytes = len(raw)
	attempt.Task = &task
	return attempt, true, nil
}

func persistRuntimeDriverAttempt(
	directory string,
	attempt runtimeDriverAttempt,
) (runtimeDriverAttempt, error) {
	if attempt.Task == nil {
		return runtimeDriverAttempt{}, fmt.Errorf("runtime attempt task is missing")
	}
	reference := runtimeDriverAttemptReference(*attempt.Task)
	path := filepath.Join(directory, filepath.FromSlash(reference))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return runtimeDriverAttempt{}, err
	}
	attempt.SchemaVersion = 1
	attempt.Reference, attempt.Digest, attempt.Bytes = "", "", 0
	attempt.Task = nil
	if attempt.SkillReads == nil {
		attempt.SkillReads = []string{}
	}
	if attempt.Commands == nil {
		attempt.Commands = []string{}
	}
	if attempt.Details == nil {
		attempt.Details = map[string]any{}
	}
	raw, err := json.MarshalIndent(attempt, "", "  ")
	if err != nil {
		return runtimeDriverAttempt{}, err
	}
	raw = append(raw, '\n')
	if err := writeExclusiveRegular(path, raw); err != nil {
		return runtimeDriverAttempt{}, err
	}
	attempt.Reference = reference
	attempt.Digest = bytesDigest(raw)
	attempt.Bytes = len(raw)
	attempt.Task = nil
	return attempt, nil
}
