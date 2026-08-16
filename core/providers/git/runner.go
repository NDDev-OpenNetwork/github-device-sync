// Package git provides bounded, cancellable, read-only access to Git plumbing.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitauthority"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/redaction"
)

const defaultOutputLimit = 8 << 20

var readOnlyCommands = map[string]struct{}{
	"rev-parse":    {},
	"status":       {},
	"submodule":    {},
	"worktree":     {},
	"config":       {},
	"diff":         {},
	"ls-files":     {},
	"remote":       {},
	"for-each-ref": {},
	"rev-list":     {},
}

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type Runner struct {
	authority    *gitauthority.Authority
	authorityErr error
	maxOutput    int
	extraEnv     []string
}

func NewRunner() (*Runner, error) {
	authority, err := gitauthority.Discover()
	if err != nil {
		return nil, fmt.Errorf("locate git executable: %w", err)
	}
	return &Runner{authority: authority, maxOutput: defaultOutputLimit}, nil
}

func NewRunnerForPath(path string, maxOutput int) *Runner {
	if maxOutput <= 0 {
		maxOutput = defaultOutputLimit
	}
	authority, err := gitauthority.Open(path)
	return &Runner{authority: authority, authorityErr: err, maxOutput: maxOutput}
}

func (runner *Runner) Run(ctx context.Context, directory string, args ...string) (CommandResult, error) {
	return runner.run(ctx, directory, map[int]struct{}{0: {}}, args...)
}

func (runner *Runner) run(
	ctx context.Context,
	directory string,
	allowedExitCodes map[int]struct{},
	args ...string,
) (CommandResult, error) {
	if runner == nil || runner.authority == nil {
		if runner != nil && runner.authorityErr != nil {
			return CommandResult{}, fmt.Errorf("git executable is unavailable: %w", runner.authorityErr)
		}
		return CommandResult{}, errors.New("git executable is unavailable")
	}
	if err := validateReadOnlyCommand(args); err != nil {
		return CommandResult{}, err
	}

	stdout := newLimitedBuffer(runner.maxOutput)
	stderr := newLimitedBuffer(runner.maxOutput)
	run, err := runner.authority.Run(ctx, gitauthority.RunRequest{
		Directory: directory, Arguments: args, ExtraEnvironment: runner.extraEnv,
		Stdout: stdout, Stderr: stderr, Configure: configureCancellation,
	})
	exitCode := run.ExitCode
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode}
	if stdout.Truncated() || stderr.Truncated() {
		return result, fmt.Errorf("git output exceeded %d bytes", runner.maxOutput)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("git %s: %w", args[0], ctxErr)
		}
		if _, allowed := allowedExitCodes[exitCode]; allowed {
			return result, nil
		}
		message := redaction.String(strings.TrimSpace(string(result.Stderr)))
		if message == "" {
			message = err.Error()
		}
		return result, fmt.Errorf("git %s failed: %s", args[0], message)
	}
	return result, nil
}

func validateReadOnlyCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("git command is empty")
	}
	if _, allowed := readOnlyCommands[args[0]]; !allowed {
		return fmt.Errorf("git command %q is not in the read-only allowlist", args[0])
	}
	if args[0] == "submodule" && (len(args) < 2 || args[1] != "status") {
		return errors.New("only git submodule status is allowed in read-only mode")
	}
	if args[0] == "worktree" && (len(args) < 2 || args[1] != "list") {
		return errors.New("only git worktree list is allowed in read-only mode")
	}
	switch args[0] {
	case "rev-parse":
		if err := validateRevParse(args); err != nil {
			return err
		}
	case "status":
		if err := validateStatus(args); err != nil {
			return err
		}
	case "config":
		submoduleRead := []string{
			"config", "--file", ".gitmodules", "--no-includes", "--null",
			"--get-regexp", submoduleConfigPattern,
		}
		remoteRead := []string{
			"config", "--local", "--no-includes", "--null", "--get-regexp", remoteConfigPattern,
		}
		if !equalArguments(args, submoduleRead) && !equalArguments(args, remoteRead) {
			return errors.New("only bounded .gitmodules or local remote reads are allowed via git config")
		}
	case "ls-files":
		if !equalArguments(args, []string{"ls-files", "--stage", "-z"}) {
			return errors.New("only git ls-files --stage -z is allowed")
		}
	case "remote":
		if err := validateRemoteRead(args); err != nil {
			return err
		}
	case "for-each-ref":
		if err := validateRefRead(args); err != nil {
			return err
		}
	case "rev-list":
		if err := validateRevList(args); err != nil {
			return err
		}
	case "diff":
		if err := validateDiff(args); err != nil {
			return err
		}
	}
	return nil
}

func validateStatus(args []string) error {
	if equalArguments(args, []string{"status", "--porcelain=v2", "--branch", "-z"}) {
		return nil
	}
	prefix := []string{"status", "--porcelain=v2", "-z", "--untracked-files=all", "--"}
	if len(args) <= len(prefix) || !equalArguments(args[:len(prefix)], prefix) {
		return errors.New("only bounded porcelain v2 status reads are allowed")
	}
	for _, path := range args[len(prefix):] {
		if !safeRepositoryPath(path) {
			return fmt.Errorf("repository path %q is outside the bounded status read", path)
		}
	}
	return nil
}

func validateRevParse(args []string) error {
	allowed := [][]string{
		{"rev-parse", "--verify", "HEAD"},
		{"rev-parse", "--path-format=absolute", "--show-toplevel"},
		{"rev-parse", "--path-format=absolute", "--git-common-dir"},
		{"rev-parse", "--path-format=absolute", "--show-superproject-working-tree"},
	}
	for _, expected := range allowed {
		if equalArguments(args, expected) {
			return nil
		}
	}
	return errors.New("git rev-parse arguments are outside the bounded read allowlist")
}

func equalArguments(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func validateRemoteRead(args []string) error {
	if len(args) == 1 {
		return nil
	}
	valid := len(args) == 4 && args[1] == "get-url" && args[2] == "--all"
	validPush := len(args) == 5 && args[1] == "get-url" && args[2] == "--push" && args[3] == "--all"
	name := ""
	if valid {
		name = args[3]
	} else if validPush {
		name = args[4]
	}
	if !safeRemoteName(name) {
		return errors.New("only git remote listing and bounded get-url reads are allowed")
	}
	return nil
}

func safeRemoteName(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\x00\r\n /\\") {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func validateRefRead(args []string) error {
	if len(args) < 3 || args[1] != "--format=%(refname)%09%(objectname)" {
		return errors.New("only bounded ref reads are allowed")
	}
	// Retirement evidence has to enumerate what exists locally, not only what
	// tracks a remote: a branch or tag that was never pushed is exactly the
	// unfinished work a deletion must not step over. Reading the whole of
	// `refs/heads` and `refs/tags` is still a read, and naming the two namespaces
	// keeps it bounded -- `refs/*` would also hand back stashes, notes and
	// whatever else a tool has written under `refs/`.
	for _, reference := range args[2:] {
		switch reference {
		case "refs/heads", "refs/tags", "refs/remotes/origin", "refs/remotes/upstream":
			continue
		}
		if !safeRemoteTrackingRef(reference) {
			return fmt.Errorf("ref %q is outside the read allowlist", reference)
		}
	}
	return nil
}

func validateRevList(args []string) error {
	if len(args) == 4 && args[1] == "--max-count=1" && args[2] == "--format=%cI" &&
		safeObjectID(args[3]) {
		return nil
	}
	if len(args) >= 5 && args[1] == "--max-count=1" && safeRevision(args[2]) && args[3] == "--" {
		for _, path := range args[4:] {
			if !safeRepositoryPath(path) {
				return fmt.Errorf("repository path %q is outside the bounded source read", path)
			}
		}
		return nil
	}
	if len(args) == 4 && args[1] == "--parents" && args[2] == "--max-count=1" &&
		safeObjectID(args[3]) {
		return nil
	}
	// Commits reachable from some local ref and from no remote-tracking ref.
	// That set is "work that exists only on this device", which no status field
	// reports and which a retirement decision cannot be made without.
	if len(args) == 4 && args[1] == "--count" && args[2] == "--all" && args[3] == "--not" {
		return nil
	}
	if len(args) == 5 && args[1] == "--count" && args[2] == "--all" &&
		args[3] == "--not" && args[4] == "--remotes" {
		return nil
	}
	if len(args) != 4 || args[1] != "--left-right" || args[2] != "--count" {
		return errors.New("only bounded git rev-list reads are allowed")
	}
	left, right, found := strings.Cut(args[3], "...")
	if !found || !safeRemoteTrackingRef(left) || !safeRemoteTrackingRef(right) {
		return errors.New("rev-list comparison must use two bounded remote-tracking refs")
	}
	return nil
}

func validateDiff(args []string) error {
	if len(args) < 6 || args[1] != "--quiet" || !safeObjectID(args[2]) ||
		!safeObjectID(args[3]) || args[4] != "--" {
		return errors.New("only bounded commit source comparisons are allowed via git diff")
	}
	for _, path := range args[5:] {
		if !safeRepositoryPath(path) {
			return fmt.Errorf("repository path %q is outside the bounded source comparison", path)
		}
	}
	return nil
}

func safeRevision(value string) bool {
	return value == "HEAD" || safeObjectID(value)
}

func safeObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func safeRepositoryPath(path string) bool {
	if path == "" || path == "." || filepath.IsAbs(path) || strings.HasPrefix(path, ":") ||
		strings.ContainsAny(path, "\x00\r\n") {
		return false
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	return cleaned == path && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func safeRemoteTrackingRef(reference string) bool {
	if !strings.HasPrefix(reference, "refs/remotes/origin/") &&
		!strings.HasPrefix(reference, "refs/remotes/upstream/") {
		return false
	}
	if strings.ContainsAny(reference, "\x00\r\n ~^:?*[\\") || strings.Contains(reference, "..") ||
		strings.HasSuffix(reference, "/") || strings.HasSuffix(reference, ".") ||
		strings.Contains(reference, "@{") {
		return false
	}
	return true
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{remaining: limit}
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	if len(value) > buffer.remaining {
		value = value[:buffer.remaining]
		buffer.truncated = true
	}
	if len(value) > 0 {
		_, _ = buffer.buffer.Write(value)
		buffer.remaining -= len(value)
	}
	return originalLength, nil
}

func (buffer *limitedBuffer) Bytes() []byte   { return buffer.buffer.Bytes() }
func (buffer *limitedBuffer) Truncated() bool { return buffer.truncated }
