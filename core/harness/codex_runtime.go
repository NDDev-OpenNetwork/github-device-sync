package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/redaction"
)

const (
	maximumCodexRuntimeBytes      = 8 << 20
	maximumCodexRuntimeLineBytes  = maximumCodexRuntimeBytes
	maximumCodexRuntimeStderr     = 64 << 10
	maximumCodexOutputSchemaBytes = 1 << 20
)

type CodexRuntimeOptions struct {
	Executable      string
	RepositoryRoot  string
	ModelLabel      string
	Prompt          string
	OutputSchema    string
	Timeout         time.Duration
	Environment     []string
	BypassHookTrust bool
}

type CodexRuntimeResult struct {
	Transcript  []byte
	Observation CodexRuntimeObservation
}

// CodexRuntimeObservation is a bounded, deterministic projection of one
// codex exec --json transcript. The raw JSONL remains the durable evidence;
// this value only exposes facts required by the harness evaluator.
type CodexRuntimeObservation struct {
	ThreadStarted bool
	TurnStarted   bool
	TurnCompleted bool
	SkillReads    []string
	Commands      []string
	Messages      []string
}

type codexRuntimeEvent struct {
	Type string            `json:"type"`
	Item *codexRuntimeItem `json:"item,omitempty"`
}

type codexRuntimeItem struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Text    string `json:"text,omitempty"`
	Status  string `json:"status,omitempty"`
}

// RunCodexRuntime executes one non-interactive read-only Codex turn without a
// shell. The caller owns environment isolation and transcript persistence.
func RunCodexRuntime(
	ctx context.Context,
	options CodexRuntimeOptions,
) (CodexRuntimeResult, error) {
	if options.Executable == "" || options.RepositoryRoot == "" ||
		strings.TrimSpace(options.Prompt) == "" {
		return CodexRuntimeResult{}, fmt.Errorf("codex executable, repository root, and prompt are required")
	}
	root, err := filepath.Abs(options.RepositoryRoot)
	if err != nil {
		return CodexRuntimeResult{}, fmt.Errorf("resolve repository root: %w", err)
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	arguments := []string{
		"exec", "--json", "--sandbox", "read-only",
		"-c", `approval_policy="never"`, "-C", root,
	}
	if strings.TrimSpace(options.ModelLabel) != "" {
		arguments = append(arguments, "--model", options.ModelLabel)
	}
	if options.BypassHookTrust {
		arguments = append(arguments, "--dangerously-bypass-hook-trust")
	}
	if options.OutputSchema != "" {
		schema, err := filepath.Abs(options.OutputSchema)
		if err != nil {
			return CodexRuntimeResult{}, fmt.Errorf("resolve Codex output schema: %w", err)
		}
		info, err := os.Lstat(schema)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Size() < 0 || info.Size() > maximumCodexOutputSchemaBytes {
			return CodexRuntimeResult{}, fmt.Errorf("Codex output schema is not one bounded regular file")
		}
		arguments = append(arguments, "--output-schema", schema)
	}
	arguments = append(arguments, options.Prompt)
	command := exec.CommandContext(runContext, options.Executable, arguments...)
	command.Dir = root
	command.Env = append([]string(nil), options.Environment...)
	command.Stdin = strings.NewReader("")
	stdout := &strictLimitedBuffer{remaining: maximumCodexRuntimeBytes}
	stderr := &strictLimitedBuffer{remaining: maximumCodexRuntimeStderr}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if runContext.Err() != nil {
			return CodexRuntimeResult{}, fmt.Errorf("codex runtime exceeded %s: %w", timeout, runContext.Err())
		}
		return CodexRuntimeResult{}, fmt.Errorf(
			"codex runtime failed: %w: %s", err,
			redaction.String(strings.TrimSpace(stderr.String())),
		)
	}
	transcript := append([]byte(nil), stdout.Bytes()...)
	observation, err := ParseCodexRuntimeJSONL(bytes.NewReader(transcript), root)
	if err != nil {
		return CodexRuntimeResult{}, err
	}
	return CodexRuntimeResult{Transcript: transcript, Observation: observation}, nil
}

// DecodeCodexFinalJSON decodes the last agent message as one strict JSON
// object. Earlier commentary cannot substitute for the schema-bound result.
func DecodeCodexFinalJSON(observation CodexRuntimeObservation, target any) error {
	if target == nil || len(observation.Messages) == 0 {
		return fmt.Errorf("Codex runtime has no final JSON target or message")
	}
	final := strings.TrimSpace(observation.Messages[len(observation.Messages)-1])
	decoder := json.NewDecoder(strings.NewReader(final))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode final Codex JSON message: %w", err)
	}
	if decoder.More() {
		return fmt.Errorf("final Codex JSON message contains multiple values")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("final Codex JSON message has trailing content")
	}
	return nil
}

// ParseCodexRuntimeJSONL validates the event stream shape and extracts exact
// skill-file reads. A path is accepted only when the completed command names a
// confined SKILL.md below the evaluated repository root.
func ParseCodexRuntimeJSONL(
	reader io.Reader,
	repositoryRoot string,
) (CodexRuntimeObservation, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return CodexRuntimeObservation{}, fmt.Errorf("resolve repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return CodexRuntimeObservation{}, fmt.Errorf("resolve repository root real path: %w", err)
	}
	limited := &io.LimitedReader{R: reader, N: maximumCodexRuntimeBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64*1024), maximumCodexRuntimeLineBytes)

	observation := CodexRuntimeObservation{
		SkillReads: []string{}, Commands: []string{}, Messages: []string{},
	}
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var event codexRuntimeEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return CodexRuntimeObservation{}, fmt.Errorf(
				"decode codex JSONL line %d: %w", lineNumber, err,
			)
		}
		switch event.Type {
		case "thread.started":
			observation.ThreadStarted = true
		case "turn.started":
			observation.TurnStarted = true
		case "turn.completed":
			observation.TurnCompleted = true
		case "item.completed":
			if event.Item == nil {
				return CodexRuntimeObservation{}, fmt.Errorf(
					"codex JSONL line %d completed an absent item", lineNumber,
				)
			}
			switch event.Item.Type {
			case "agent_message":
				observation.Messages = append(observation.Messages, event.Item.Text)
			case "command_execution":
				observation.Commands = append(observation.Commands, event.Item.Command)
				for _, candidate := range skillPathsInCommand(event.Item.Command) {
					if normalized, accepted := confinedSkillPath(root, candidate); accepted {
						observation.SkillReads = appendUnique(observation.SkillReads, normalized)
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return CodexRuntimeObservation{}, fmt.Errorf("scan codex JSONL: %w", err)
	}
	if limited.N <= 0 {
		return CodexRuntimeObservation{}, fmt.Errorf(
			"codex JSONL exceeds %d bytes", maximumCodexRuntimeBytes,
		)
	}
	if !observation.ThreadStarted || !observation.TurnStarted || !observation.TurnCompleted {
		return CodexRuntimeObservation{}, fmt.Errorf("codex JSONL is missing lifecycle completion")
	}
	return observation, nil
}

func skillPathsInCommand(command string) []string {
	fields := strings.Fields(command)
	paths := make([]string, 0, 1)
	for _, field := range fields {
		candidate := strings.Trim(field, "'\";()")
		if filepath.Base(candidate) == "SKILL.md" {
			paths = append(paths, filepath.Clean(candidate))
		}
	}
	return paths
}

func confinedSkillPath(root, candidate string) (string, bool) {
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err := filepath.EvalSymlinks(filepath.Clean(candidate))
	if err != nil {
		return "", false
	}
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	if filepath.Base(candidate) != "SKILL.md" {
		return "", false
	}
	return candidate, true
}

func appendUnique(values []string, candidate string) []string {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}
