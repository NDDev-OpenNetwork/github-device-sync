// Package gitauthority owns the executable and process environment used for
// every security-sensitive Git subprocess in GDS.
package gitauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const maxExecutableBytes = 256 << 20

var discoveryCandidates = []string{
	"/usr/bin/git",
	"/bin/git",
	"/opt/homebrew/bin/git",
	"/usr/local/bin/git",
}

var (
	discoverOnce     sync.Once
	discovered       *Authority
	discoveryFailure error
)

// Identity is stable evidence for the exact Git executable used by one
// authority. Version is populated by InspectVersion.
type Identity struct {
	Path    string
	Version string
	Digest  string
}

// RunRequest describes one bounded Git invocation. ExtraEnvironment accepts
// only the explicit keys documented by controlledEnvironment.
type RunRequest struct {
	Directory        string
	Arguments        []string
	ExtraEnvironment []string
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	Configure        func(*exec.Cmd)
}

type RunResult struct {
	ExitCode int
}

// StdoutProcess keeps streaming commands inside the same authority boundary.
// Wait must be called exactly once; it revalidates executable identity.
type StdoutProcess struct {
	command   *exec.Cmd
	stdout    io.ReadCloser
	authority *Authority
}

type Authority struct {
	path     string
	identity os.FileInfo
	digest   string
}

// Discover deliberately ignores caller PATH. GDS supports darwin and Linux,
// where Git must be installed in one of these reviewed system locations.
func Discover() (*Authority, error) {
	discoverOnce.Do(func() {
		discovered, discoveryFailure = discover()
	})
	return discovered, discoveryFailure
}

func discover() (*Authority, error) {
	var failures []error
	for _, candidate := range discoveryCandidates {
		authority, err := Open(candidate)
		if err == nil {
			err = validateDiscoveryPath(authority.path)
		}
		if err == nil {
			return authority, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		return nil, fmt.Errorf("locate trusted Git executable: %w", errors.Join(failures...))
	}
	return nil, errors.New("locate trusted Git executable: no approved system path exists")
}

// Open creates an explicit authority for tests and dependency injection. The
// path is resolved once and every run verifies that it still names the same
// regular executable.
func Open(path string) (*Authority, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("Git executable path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	info, digest, err := inspectExecutable(resolved)
	if err != nil {
		return nil, err
	}
	return &Authority{path: resolved, identity: info, digest: digest}, nil
}

func (authority *Authority) Identity() Identity {
	if authority == nil {
		return Identity{}
	}
	return Identity{Path: authority.path, Digest: authority.digest}
}

func (authority *Authority) InspectVersion(ctx context.Context) (Identity, error) {
	if authority == nil {
		return Identity{}, errors.New("Git authority is unavailable")
	}
	var stdout strings.Builder
	_, err := authority.Run(ctx, RunRequest{
		Directory: filepath.Dir(authority.path), Arguments: []string{"version"}, Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		return Identity{}, fmt.Errorf("inspect Git version: %w", err)
	}
	version := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(version, "git version ") || strings.ContainsAny(version, "\x00\r\n") {
		return Identity{}, errors.New("Git executable returned an invalid version identity")
	}
	identity := authority.Identity()
	identity.Version = strings.TrimPrefix(version, "git version ")
	return identity, nil
}

func (authority *Authority) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	command, err := authority.command(ctx, request)
	if err != nil {
		return RunResult{}, err
	}
	runErr := command.Run()
	exitCode := 0
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	identityErr := authority.verify()
	return RunResult{ExitCode: exitCode}, errors.Join(runErr, identityErr)
}

func (authority *Authority) StartStdout(
	ctx context.Context,
	request RunRequest,
) (*StdoutProcess, error) {
	if request.Stdout != nil {
		return nil, errors.New("streaming Git request cannot preconfigure stdout")
	}
	command, err := authority.command(ctx, request)
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		return nil, errors.Join(err, authority.verify())
	}
	return &StdoutProcess{command: command, stdout: stdout, authority: authority}, nil
}

func (process *StdoutProcess) Stdout() io.Reader {
	if process == nil {
		return strings.NewReader("")
	}
	return process.stdout
}

func (process *StdoutProcess) Kill() error {
	if process == nil || process.command == nil || process.command.Process == nil {
		return nil
	}
	return process.command.Process.Kill()
}

func (process *StdoutProcess) Wait() error {
	if process == nil || process.command == nil || process.authority == nil {
		return errors.New("streaming Git process is unavailable")
	}
	waitErr := process.command.Wait()
	identityErr := process.authority.verify()
	process.command = nil
	return errors.Join(waitErr, identityErr)
}

func (authority *Authority) command(ctx context.Context, request RunRequest) (*exec.Cmd, error) {
	if ctx == nil || authority == nil || len(request.Arguments) == 0 {
		return nil, errors.New("Git authority request is incomplete")
	}
	if err := authority.verify(); err != nil {
		return nil, err
	}
	directory, err := filepath.Abs(request.Directory)
	if err != nil {
		return nil, fmt.Errorf("resolve Git command directory: %w", err)
	}
	environment, err := controlledEnvironment(directory, request.ExtraEnvironment)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, authority.path, request.Arguments...)
	command.Dir = directory
	command.Env = environment
	command.Stdin = request.Stdin
	command.Stdout = request.Stdout
	command.Stderr = request.Stderr
	if request.Configure != nil {
		request.Configure(command)
	}
	return command, nil
}

func (authority *Authority) verify() error {
	if authority == nil || authority.path == "" || authority.identity == nil || authority.digest == "" {
		return errors.New("Git executable authority is unavailable")
	}
	current, err := os.Lstat(authority.path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(authority.identity, current) ||
		current.Size() != authority.identity.Size() || current.Mode() != authority.identity.Mode() ||
		!current.ModTime().Equal(authority.identity.ModTime()) {
		return errors.New("Git executable identity changed after authority creation")
	}
	return nil
}

func inspectExecutable(path string) (os.FileInfo, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 ||
		info.Mode().Perm()&0o022 != 0 || !trustedExecutableOwner(info) ||
		info.Size() < 1 || info.Size() > maxExecutableBytes {
		return nil, "", errors.New("Git executable must be a bounded executable regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(hasher, io.LimitReader(file, maxExecutableBytes+1))
	closeErr := file.Close()
	after, statErr := os.Lstat(path)
	if copyErr != nil || closeErr != nil || statErr != nil {
		return nil, "", errors.Join(copyErr, closeErr, statErr)
	}
	if written != info.Size() || !os.SameFile(info, after) || after.Size() != info.Size() ||
		!after.ModTime().Equal(info.ModTime()) {
		return nil, "", errors.New("Git executable changed while its identity was captured")
	}
	return after, "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func validateDiscoveryPath(path string) error {
	executable, err := os.Lstat(path)
	if err != nil || !trustedDiscoveryOwner(executable) {
		return fmt.Errorf("Git discovery executable is not system-owned: %s", path)
	}
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
			info.Mode().Perm()&0o022 != 0 || !trustedDiscoveryOwner(info) {
			return fmt.Errorf("Git executable ancestor is not trusted: %s", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func controlledEnvironment(directory string, extra []string) ([]string, error) {
	values := map[string]string{
		"GIT_CONFIG_COUNT":                "1",
		"GIT_CONFIG_GLOBAL":               os.DevNull,
		"GIT_CONFIG_KEY_0":                "core.fsmonitor",
		"GIT_CONFIG_NOSYSTEM":             "1",
		"GIT_CONFIG_SYSTEM":               os.DevNull,
		"GIT_CONFIG_VALUE_0":              "false",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM": "0",
		"GIT_LITERAL_PATHSPECS":           "1",
		"GIT_OPTIONAL_LOCKS":              "0",
		"GIT_PAGER":                       "cat",
		"GIT_TERMINAL_PROMPT":             "0",
		"LANG":                            "C",
		"LC_ALL":                          "C",
		"PATH":                            "/usr/bin:/bin:/usr/sbin:/sbin",
	}
	for _, name := range []string{
		"ALL_PROXY", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSH_AUTH_SOCK",
		"SSL_CERT_DIR", "SSL_CERT_FILE", "all_proxy", "https_proxy", "http_proxy", "no_proxy",
	} {
		if value, found := os.LookupEnv(name); found && safeEnvironmentValue(value) {
			values[name] = value
		}
	}
	allowedExtra := map[string]struct{}{
		"GIT_ATTR_NOSYSTEM": {}, "GIT_AUTHOR_DATE": {}, "GIT_AUTHOR_EMAIL": {},
		"GIT_AUTHOR_NAME": {}, "GIT_COMMITTER_DATE": {}, "GIT_COMMITTER_EMAIL": {},
		"GIT_COMMITTER_NAME": {}, "GIT_MERGE_AUTOEDIT": {}, "HOME": {},
	}
	for _, entry := range extra {
		key, value, found := strings.Cut(entry, "=")
		if !found || !safeEnvironmentValue(value) {
			return nil, errors.New("Git extra environment contains an invalid value")
		}
		if _, allowed := allowedExtra[key]; !allowed {
			return nil, fmt.Errorf("Git extra environment key %s is not allowed", key)
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result, nil
}

func safeEnvironmentValue(value string) bool {
	return value != "" && !strings.ContainsAny(value, "\x00\r\n")
}
