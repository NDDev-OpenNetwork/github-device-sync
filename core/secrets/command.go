package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const commandTimeout = 10 * time.Second

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
	path, err := credentialHelperPath(executable)
	if err != nil {
		return nil, ErrUnavailable
	}
	commandContext, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(commandContext, path, arguments...)
	command.Env = commandEnvironment()
	var output bytes.Buffer
	command.Stdout = &boundedSecretBuffer{buffer: &output}
	command.Stderr = &discardSecretBuffer{}
	if err := command.Run(); err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, ErrUnavailable
		}
		if errors.Is(err, ErrInvalid) {
			return nil, ErrInvalid
		}
		return nil, ErrNotFound
	}
	return normalize(output.Bytes())
}

func credentialHelperPath(executable string) (string, error) {
	var candidates []string
	switch {
	case runtime.GOOS == "darwin" && executable == "security":
		candidates = []string{"/usr/bin/security"}
	case runtime.GOOS == "linux" && executable == "secret-tool":
		candidates = []string{"/usr/bin/secret-tool", "/bin/secret-tool"}
	default:
		return "", ErrUnavailable
	}
	for _, candidate := range candidates {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		info, err := os.Lstat(resolved)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 &&
			(info.Mode()&os.ModeSymlink) == 0 {
			return resolved, nil
		}
	}
	return "", ErrUnavailable
}

func commandEnvironment() []string {
	environment := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	for _, name := range []string{
		"DBUS_SESSION_BUS_ADDRESS", "HOME", "LANG", "LC_ALL", "LOGNAME", "USER",
		"XDG_RUNTIME_DIR",
	} {
		if value, found := os.LookupEnv(name); found && value != "" &&
			!strings.ContainsAny(value, "\x00\r\n") {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

type MacOSKeychainStore struct {
	Service  string
	Accounts map[string]string
	Runner   CommandRunner
}

func (store MacOSKeychainStore) Get(ctx context.Context, reference string) ([]byte, error) {
	if runtime.GOOS != "darwin" || store.Service == "" || validateReference(reference) != nil {
		return nil, ErrUnavailable
	}
	account, found := store.Accounts[reference]
	if !found || account == "" {
		return nil, ErrNotFound
	}
	runner := store.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	value, err := runner.Run(
		ctx, "security", "find-generic-password", "-w", "-s", store.Service, "-a", account,
	)
	if err != nil {
		return nil, classifyCommandStoreError(err)
	}
	return normalize(value)
}

type LinuxSecretServiceStore struct {
	CollectionAttribute string
	References          map[string]string
	Runner              CommandRunner
}

func (store LinuxSecretServiceStore) Get(ctx context.Context, reference string) ([]byte, error) {
	if runtime.GOOS != "linux" || store.CollectionAttribute == "" || validateReference(reference) != nil {
		return nil, ErrUnavailable
	}
	value, found := store.References[reference]
	if !found || value == "" {
		return nil, ErrNotFound
	}
	runner := store.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	secret, err := runner.Run(ctx, "secret-tool", "lookup", store.CollectionAttribute, value)
	if err != nil {
		return nil, classifyCommandStoreError(err)
	}
	return normalize(secret)
}

func classifyCommandStoreError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, ErrInvalid) {
		return ErrInvalid
	}
	return ErrUnavailable
}

type boundedSecretBuffer struct{ buffer *bytes.Buffer }

func (buffer *boundedSecretBuffer) Write(value []byte) (int, error) {
	if buffer.buffer.Len()+len(value) > MaxSecretBytes {
		return 0, ErrInvalid
	}
	return buffer.buffer.Write(value)
}

type discardSecretBuffer struct{}

func (*discardSecretBuffer) Write(value []byte) (int, error) { return len(value), nil }
