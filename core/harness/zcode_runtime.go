package harness

// The zcode --json event family (turn.started/turn.completed/turn.failed plus
// message.upserted) and its message payload shape were mined from the zcode
// 0.15.2 bundle (/Applications/ZCode.app/Contents/Resources/glm/zcode.cjs): the
// dotted event names, the message.upserted payload schema (content string with
// optional toolCalls), and the {type:"tool_use",name,id,input} content-block
// shape are all taken from that bundle. Because Z.AI authentication is
// unavailable in this environment (zcode login fails and every --prompt returns
// "Turn execution failed"), the exact envelope nesting of message.upserted
// (nested "message" object versus inlined fields) could not be observed live;
// ParseZcodeStreamJSON therefore tolerates both shapes and awaits live
// validation against a credentialed zcode runtime, exactly as the codex and
// claude live turns are gated behind their own authenticated runtimes.

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
	maximumZcodeRuntimeBytes     = 8 << 20
	maximumZcodeRuntimeLineBytes = maximumZcodeRuntimeBytes
	maximumZcodeRuntimeStderr    = 64 << 10
)

// zcodeMutatingTools are zcode native tools that write to the working tree. A
// read-only probe must never see one complete, so their appearance in the
// tool-use stream counts as a mutation attempt even when plan mode blocks the
// actual edit. Write and Edit are confirmed from the zcode 0.15.2 tool registry
// mined from zcode.cjs; MultiEdit, NotebookEdit, Apply, and Patch are inferred
// from the codex/claude tool families and covered defensively — they are not
// present in the mined 0.15.2 registry and await live confirmation.
var zcodeMutatingTools = map[string]struct{}{
	"Write": {}, "Edit": {},
	"MultiEdit": {}, "NotebookEdit": {}, "Apply": {}, "Patch": {},
}

// ZcodeRuntimeOptions configure one non-interactive, read-only zcode turn. The
// caller owns environment isolation and transcript persistence, and plan mode
// keeps the turn free of tree mutation.
type ZcodeRuntimeOptions struct {
	Executable     string
	RepositoryRoot string
	ModelLabel     string
	Prompt         string
	Timeout        time.Duration
	Environment    []string
}

type ZcodeRuntimeResult struct {
	Transcript  []byte
	Observation ZcodeRuntimeObservation
}

// ZcodeRuntimeObservation is a bounded, deterministic projection of one zcode
// --json NDJSON transcript. The raw NDJSON remains the durable evidence; this
// value only exposes facts the harness evaluator consumes.
type ZcodeRuntimeObservation struct {
	Initialized      bool
	Completed        bool
	IsError          bool
	SkillReads       []string
	SkillInvocations []string
	Commands         []string
	ToolUses         []string
	Messages         []string
	ResultText       string
}

// zcodeStreamEvent is one NDJSON line. message.upserted carries the message
// either as a nested "message" object or inlined on the event; both shapes are
// accepted (see the file doc comment) because the exact envelope could not be
// confirmed live.
type zcodeStreamEvent struct {
	Type      string                `json:"type"`
	Message   *zcodeStreamMessage   `json:"message,omitempty"`
	Role      string                `json:"role,omitempty"`
	Content   json.RawMessage       `json:"content,omitempty"`
	Parts     []zcodeStreamPart     `json:"parts,omitempty"`
	ToolCalls []zcodeStreamToolCall `json:"toolCalls,omitempty"`
}

type zcodeStreamMessage struct {
	Role      string                `json:"role"`
	Content   json.RawMessage       `json:"content"`
	Parts     []zcodeStreamPart     `json:"parts"`
	ToolCalls []zcodeStreamToolCall `json:"toolCalls"`
}

type zcodeStreamPart struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type zcodeStreamToolCall struct {
	ToolName string          `json:"toolName,omitempty"`
	Name     string          `json:"name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

// RunZcodeRuntime executes one non-interactive read-only zcode turn without a
// shell. Plan mode denies tree mutation; the caller supplies the isolated
// environment.
func RunZcodeRuntime(
	ctx context.Context,
	options ZcodeRuntimeOptions,
) (ZcodeRuntimeResult, error) {
	if options.Executable == "" || options.RepositoryRoot == "" ||
		strings.TrimSpace(options.Prompt) == "" {
		return ZcodeRuntimeResult{}, fmt.Errorf("zcode executable, repository root, and prompt are required")
	}
	root, err := filepath.Abs(options.RepositoryRoot)
	if err != nil {
		return ZcodeRuntimeResult{}, fmt.Errorf("resolve repository root: %w", err)
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// --mode plan is REQUIRED: the --prompt default mode is yolo (mutating);
	// plan mode is read-only. --json emits the NDJSON event stream. --disallowed-tools
	// names the confirmed native mutating tools as defense in depth beyond plan mode.
	arguments := []string{
		"--prompt", options.Prompt,
		"--mode", "plan",
		"--json",
		"--disallowed-tools", "Write,Edit",
	}
	command := exec.CommandContext(runContext, options.Executable, arguments...)
	command.Dir = root
	command.Env = append([]string(nil), options.Environment...)
	command.Stdin = strings.NewReader("")
	stdout := &strictLimitedBuffer{remaining: maximumZcodeRuntimeBytes}
	stderr := &strictLimitedBuffer{remaining: maximumZcodeRuntimeStderr}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if runContext.Err() != nil {
			return ZcodeRuntimeResult{}, fmt.Errorf("zcode runtime exceeded %s: %w", timeout, runContext.Err())
		}
		return ZcodeRuntimeResult{}, fmt.Errorf(
			"zcode runtime failed: %w: %s", err,
			redaction.String(strings.TrimSpace(stderr.String())),
		)
	}
	transcript := append([]byte(nil), stdout.Bytes()...)
	observation, err := ParseZcodeStreamJSON(bytes.NewReader(transcript), root)
	if err != nil {
		return ZcodeRuntimeResult{}, err
	}
	return ZcodeRuntimeResult{Transcript: transcript, Observation: observation}, nil
}

// DecodeZcodeFinalJSON decodes the final assistant message as one strict JSON
// object. zcode --json streams NDJSON events rather than a schema-constrained
// final answer, so the prompt must require strict JSON and this validates it.
// Earlier commentary cannot substitute for it.
func DecodeZcodeFinalJSON(observation ZcodeRuntimeObservation, target any) error {
	if target == nil {
		return fmt.Errorf("zcode runtime has no final JSON target")
	}
	final := strings.TrimSpace(observation.ResultText)
	if final == "" && len(observation.Messages) != 0 {
		final = strings.TrimSpace(observation.Messages[len(observation.Messages)-1])
	}
	if final == "" {
		return fmt.Errorf("zcode runtime produced no final message")
	}
	decoder := json.NewDecoder(strings.NewReader(final))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode final zcode JSON message: %w", err)
	}
	if decoder.More() {
		return fmt.Errorf("final zcode JSON message contains multiple values")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("final zcode JSON message has trailing content")
	}
	return nil
}

// ParseZcodeStreamJSON validates the NDJSON event shape and extracts the exact
// skill invocations, confined skill-file reads, tool uses, and lifecycle
// completion of one zcode turn. Lifecycle: session.created or turn.started opens
// the turn; turn.completed closes it; turn.failed marks a failed turn.
func ParseZcodeStreamJSON(
	reader io.Reader,
	repositoryRoot string,
) (ZcodeRuntimeObservation, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return ZcodeRuntimeObservation{}, fmt.Errorf("resolve repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return ZcodeRuntimeObservation{}, fmt.Errorf("resolve repository root real path: %w", err)
	}
	limited := &io.LimitedReader{R: reader, N: maximumZcodeRuntimeBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64*1024), maximumZcodeRuntimeLineBytes)

	observation := ZcodeRuntimeObservation{
		SkillReads: []string{}, SkillInvocations: []string{}, Commands: []string{},
		ToolUses: []string{}, Messages: []string{},
	}
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var event zcodeStreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return ZcodeRuntimeObservation{}, fmt.Errorf(
				"decode zcode stream-json line %d: %w", lineNumber, err,
			)
		}
		switch event.Type {
		case "session.created", "turn.started":
			observation.Initialized = true
		case "message.upserted":
			observeZcodeMessage(root, event, &observation)
		case "turn.completed":
			observation.Completed = true
		case "turn.failed":
			// A failed turn is still a terminal turn: mark completion so the
			// failed-turn rejection fires ahead of the lifecycle check, exactly as
			// a claude result event marks completion regardless of is_error.
			observation.Completed = true
			observation.IsError = true
		}
	}
	if err := scanner.Err(); err != nil {
		return ZcodeRuntimeObservation{}, fmt.Errorf("scan zcode stream-json: %w", err)
	}
	if limited.N <= 0 {
		return ZcodeRuntimeObservation{}, fmt.Errorf(
			"zcode stream-json exceeds %d bytes", maximumZcodeRuntimeBytes,
		)
	}
	if !observation.Initialized || !observation.Completed {
		return ZcodeRuntimeObservation{}, fmt.Errorf("zcode stream-json is missing lifecycle completion")
	}
	if observation.IsError {
		return ZcodeRuntimeObservation{}, fmt.Errorf("zcode stream-json reports a failed turn")
	}
	if observation.ResultText == "" && len(observation.Messages) != 0 {
		observation.ResultText = observation.Messages[len(observation.Messages)-1]
	}
	return observation, nil
}

// observeZcodeMessage folds one message.upserted event into the observation. It
// accepts the message either nested under "message" or inlined on the event.
func observeZcodeMessage(root string, event zcodeStreamEvent, observation *ZcodeRuntimeObservation) {
	message := event.Message
	if message == nil {
		message = &zcodeStreamMessage{
			Role: event.Role, Content: event.Content,
			Parts: event.Parts, ToolCalls: event.ToolCalls,
		}
	}
	assistant := message.Role == "" || message.Role == "assistant"
	for _, text := range zcodeMessageText(message.Content) {
		if strings.TrimSpace(text) != "" && assistant {
			observation.Messages = append(observation.Messages, text)
		}
	}
	blocks := append([]zcodeStreamPart(nil), message.Parts...)
	blocks = append(blocks, zcodeContentBlocks(message.Content)...)
	for _, block := range blocks {
		if block.Type == "text" {
			if strings.TrimSpace(block.Text) != "" && assistant {
				observation.Messages = append(observation.Messages, block.Text)
			}
			continue
		}
		if block.Type == "tool_use" || block.Type == "tool_call" {
			observeZcodeToolUse(root, block.Name, block.Input, observation)
		}
	}
	for _, call := range message.ToolCalls {
		name := call.ToolName
		if name == "" {
			name = call.Name
		}
		observeZcodeToolUse(root, name, call.Input, observation)
	}
}

// zcodeMessageText returns the assistant text when message content is a bare
// string; array content is handled through zcodeContentBlocks instead.
func zcodeMessageText(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []string{text}
	}
	return nil
}

// zcodeContentBlocks returns array-form content blocks when message content is a
// list of {type,text,name,input} blocks; string content yields nothing here.
func zcodeContentBlocks(raw json.RawMessage) []zcodeStreamPart {
	if len(raw) == 0 {
		return nil
	}
	var blocks []zcodeStreamPart
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

func observeZcodeToolUse(root, name string, input json.RawMessage, observation *ZcodeRuntimeObservation) {
	name = strings.TrimSpace(name)
	if name != "" {
		observation.ToolUses = append(observation.ToolUses, name)
	}
	if name == "Skill" {
		if skill := zcodeSkillFromInput(input); skill != "" {
			observation.SkillInvocations = appendUnique(observation.SkillInvocations, skill)
		}
	}
	for _, candidate := range zcodeSkillPathsInToolUse(input) {
		if normalized, accepted := confinedSkillPath(root, candidate); accepted {
			observation.SkillReads = appendUnique(observation.SkillReads, normalized)
		}
	}
	if command := zcodeShellCommand(name, input); command != "" {
		observation.Commands = append(observation.Commands, command)
	}
}

func zcodeSkillFromInput(raw json.RawMessage) string {
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

func zcodeShellCommand(toolName string, raw json.RawMessage) string {
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

func zcodeSkillPathsInToolUse(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var input struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Command  string `json:"command"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil
	}
	paths := []string{}
	for _, candidate := range []string{input.FilePath, input.Path} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && filepath.Base(candidate) == "SKILL.md" {
			paths = append(paths, filepath.Clean(candidate))
		}
	}
	paths = append(paths, skillPathsInCommand(input.Command)...)
	return paths
}

// zcodeCommandsAttemptMutation reports whether the observed turn attempted any
// tree mutation, either through a mutating shell command or a native editing
// tool. The shell heuristic reuses the shared runtime mutation grammar.
func zcodeCommandsAttemptMutation(observation ZcodeRuntimeObservation) bool {
	if codexCommandsAttemptMutation(observation.Commands) {
		return true
	}
	for _, tool := range observation.ToolUses {
		if _, mutating := zcodeMutatingTools[tool]; mutating {
			return true
		}
	}
	return false
}
