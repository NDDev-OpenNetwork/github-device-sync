package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/redaction"
)

const (
	maximumClaudeRuntimeBytes     = 8 << 20
	maximumClaudeRuntimeLineBytes = maximumClaudeRuntimeBytes
	maximumClaudeRuntimeStderr    = 64 << 10
)

// claudeMutatingTools are Claude Code native tools that write to the working
// tree. A read-only probe must never see one complete, so their appearance in
// the tool-use stream counts as a mutation attempt even when plan mode blocks
// the actual edit.
var claudeMutatingTools = map[string]struct{}{
	"Write": {}, "Edit": {}, "MultiEdit": {}, "NotebookEdit": {},
}

// ClaudeRuntimeOptions configure one non-interactive, read-only Claude Code
// turn. The caller owns environment isolation and transcript persistence, and
// plan mode keeps the turn free of tree mutation.
type ClaudeRuntimeOptions struct {
	Executable     string
	RepositoryRoot string
	ModelLabel     string
	Prompt         string
	Timeout        time.Duration
	Environment    []string
	AllowedTools   []string
	BypassHooks    bool
}

type ClaudeRuntimeResult struct {
	Transcript  []byte
	Observation ClaudeRuntimeObservation
}

// ClaudeRuntimeObservation is a bounded, deterministic projection of one Claude
// Code stream-json transcript. The raw JSONL remains the durable evidence; this
// value only exposes facts the harness evaluator consumes.
type ClaudeRuntimeObservation struct {
	Initialized      bool
	Completed        bool
	IsError          bool
	SkillReads       []string
	SkillInvocations []string
	Commands         []string
	ToolUses         []string
	Messages         []string
	ResultText       string
	HookEvents       []string
}

type claudeStreamEvent struct {
	Type    string            `json:"type"`
	Subtype string            `json:"subtype,omitempty"`
	Message *claudeStreamBody `json:"message,omitempty"`
	Result  *string           `json:"result,omitempty"`
	IsError bool              `json:"is_error,omitempty"`
}

type claudeStreamBody struct {
	Role    string                `json:"role"`
	Content []claudeStreamContent `json:"content"`
}

type claudeStreamContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// RunClaudeRuntime executes one non-interactive read-only Claude Code turn
// without a shell. Plan mode denies tree mutation; the caller supplies the
// isolated environment.
func RunClaudeRuntime(
	ctx context.Context,
	options ClaudeRuntimeOptions,
) (ClaudeRuntimeResult, error) {
	if options.Executable == "" || options.RepositoryRoot == "" ||
		strings.TrimSpace(options.Prompt) == "" {
		return ClaudeRuntimeResult{}, fmt.Errorf("claude executable, repository root, and prompt are required")
	}
	root, err := filepath.Abs(options.RepositoryRoot)
	if err != nil {
		return ClaudeRuntimeResult{}, fmt.Errorf("resolve repository root: %w", err)
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	arguments := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "plan",
	}
	if !options.BypassHooks {
		arguments = append(arguments, "--disallowed-tools", "Write,Edit,MultiEdit,NotebookEdit")
	}
	if strings.TrimSpace(options.ModelLabel) != "" {
		arguments = append(arguments, "--model", options.ModelLabel)
	}
	if len(options.AllowedTools) != 0 {
		arguments = append(arguments, "--allowed-tools", strings.Join(options.AllowedTools, ","))
	}
	arguments = append(arguments, options.Prompt)
	command := exec.CommandContext(runContext, options.Executable, arguments...)
	command.Dir = root
	command.Env = append([]string(nil), options.Environment...)
	command.Stdin = strings.NewReader("")
	stdout := &strictLimitedBuffer{remaining: maximumClaudeRuntimeBytes}
	stderr := &strictLimitedBuffer{remaining: maximumClaudeRuntimeStderr}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if runContext.Err() != nil {
			return ClaudeRuntimeResult{}, fmt.Errorf("claude runtime exceeded %s: %w", timeout, runContext.Err())
		}
		return ClaudeRuntimeResult{}, fmt.Errorf(
			"claude runtime failed: %w: %s", err,
			redaction.String(strings.TrimSpace(stderr.String())),
		)
	}
	transcript := append([]byte(nil), stdout.Bytes()...)
	observation, err := ParseClaudeStreamJSON(bytes.NewReader(transcript), root)
	if err != nil {
		return ClaudeRuntimeResult{}, err
	}
	return ClaudeRuntimeResult{Transcript: transcript, Observation: observation}, nil
}

// DecodeClaudeFinalJSON decodes the final assistant result as one strict JSON
// object. Claude has no output-schema flag, so the prompt must require strict
// JSON and this validates it. Earlier commentary cannot substitute for it.
func DecodeClaudeFinalJSON(observation ClaudeRuntimeObservation, target any) error {
	if target == nil {
		return fmt.Errorf("Claude runtime has no final JSON target")
	}
	final := strings.TrimSpace(observation.ResultText)
	if final == "" && len(observation.Messages) != 0 {
		final = strings.TrimSpace(observation.Messages[len(observation.Messages)-1])
	}
	if final == "" {
		return fmt.Errorf("Claude runtime produced no final message")
	}
	decoder := json.NewDecoder(strings.NewReader(final))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode final Claude JSON message: %w", err)
	}
	if decoder.More() {
		return fmt.Errorf("final Claude JSON message contains multiple values")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("final Claude JSON message has trailing content")
	}
	return nil
}

// ParseClaudeStreamJSON validates the stream-json shape and extracts the exact
// skill invocations, confined skill-file reads, tool uses, and lifecycle
// completion of one Claude Code turn.
func ParseClaudeStreamJSON(
	reader io.Reader,
	repositoryRoot string,
) (ClaudeRuntimeObservation, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return ClaudeRuntimeObservation{}, fmt.Errorf("resolve repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return ClaudeRuntimeObservation{}, fmt.Errorf("resolve repository root real path: %w", err)
	}
	limited := &io.LimitedReader{R: reader, N: maximumClaudeRuntimeBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64*1024), maximumClaudeRuntimeLineBytes)

	observation := ClaudeRuntimeObservation{
		SkillReads: []string{}, SkillInvocations: []string{}, Commands: []string{},
		ToolUses: []string{}, Messages: []string{}, HookEvents: []string{},
	}
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var event claudeStreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return ClaudeRuntimeObservation{}, fmt.Errorf(
				"decode claude stream-json line %d: %w", lineNumber, err,
			)
		}
		switch event.Type {
		case "system":
			switch {
			case event.Subtype == "init":
				observation.Initialized = true
			case strings.HasPrefix(event.Subtype, "hook"):
				observation.HookEvents = appendUnique(observation.HookEvents, event.Subtype)
			}
		case "assistant":
			if event.Message == nil {
				continue
			}
			for _, block := range event.Message.Content {
				switch block.Type {
				case "text":
					if strings.TrimSpace(block.Text) != "" {
						observation.Messages = append(observation.Messages, block.Text)
					}
				case "tool_use":
					observeClaudeToolUse(root, block, &observation)
				}
			}
		case "result":
			observation.Completed = true
			observation.IsError = event.IsError
			if event.Result != nil {
				observation.ResultText = *event.Result
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return ClaudeRuntimeObservation{}, fmt.Errorf("scan claude stream-json: %w", err)
	}
	if limited.N <= 0 {
		return ClaudeRuntimeObservation{}, fmt.Errorf(
			"claude stream-json exceeds %d bytes", maximumClaudeRuntimeBytes,
		)
	}
	if !observation.Initialized || !observation.Completed {
		return ClaudeRuntimeObservation{}, fmt.Errorf("claude stream-json is missing lifecycle completion")
	}
	if observation.IsError {
		return ClaudeRuntimeObservation{}, fmt.Errorf("claude stream-json reports a failed turn")
	}
	return observation, nil
}

func observeClaudeToolUse(root string, block claudeStreamContent, observation *ClaudeRuntimeObservation) {
	name := strings.TrimSpace(block.Name)
	if name != "" {
		observation.ToolUses = append(observation.ToolUses, name)
	}
	if name == "Skill" {
		if skill := claudeSkillFromInput(block.Input); skill != "" {
			observation.SkillInvocations = appendUnique(observation.SkillInvocations, skill)
		}
	}
	for _, candidate := range claudeSkillPathsInToolUse(block) {
		if normalized, accepted := confinedSkillPath(root, candidate); accepted {
			observation.SkillReads = appendUnique(observation.SkillReads, normalized)
		}
	}
	if command := claudeShellCommand(name, block.Input); command != "" {
		observation.Commands = append(observation.Commands, command)
	}
}

func claudeSkillFromInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var input struct {
		Command string `json:"command"`
		Name    string `json:"name"`
		Skill   string `json:"skill"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}
	for _, candidate := range []string{input.Command, input.Name, input.Skill} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func claudeShellCommand(toolName string, raw json.RawMessage) string {
	if toolName != "Bash" || len(raw) == 0 {
		return ""
	}
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}
	return strings.TrimSpace(input.Command)
}

func claudeSkillPathsInToolUse(block claudeStreamContent) []string {
	if len(block.Input) == 0 {
		return nil
	}
	var input struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Command  string `json:"command"`
		Notebook string `json:"notebook_path"`
	}
	if err := json.Unmarshal(block.Input, &input); err != nil {
		return nil
	}
	paths := []string{}
	for _, candidate := range []string{input.FilePath, input.Path, input.Notebook} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && filepath.Base(candidate) == "SKILL.md" {
			paths = append(paths, filepath.Clean(candidate))
		}
	}
	paths = append(paths, skillPathsInCommand(input.Command)...)
	return paths
}

// claudeCommandsAttemptMutation reports whether the observed turn attempted any
// tree mutation, either through a mutating shell command or a native editing
// tool. The shell heuristic reuses the shared runtime mutation grammar.
func claudeCommandsAttemptMutation(observation ClaudeRuntimeObservation) bool {
	if codexCommandsAttemptMutation(observation.Commands) {
		return true
	}
	for _, tool := range observation.ToolUses {
		if _, mutating := claudeMutatingTools[tool]; mutating {
			return true
		}
	}
	return false
}
