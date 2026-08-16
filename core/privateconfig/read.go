// Package privateconfig reads device-local security-sensitive configuration
// through one bounded stable-file contract.
package privateconfig

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

type ErrorKind string

const (
	ErrorUnavailable ErrorKind = "unavailable"
	ErrorSecurity    ErrorKind = "security"
)

type Error struct {
	Kind  ErrorKind
	Cause error
}

func (configError *Error) Error() string {
	if configError.Cause == nil {
		return fmt.Sprintf("private configuration failed (%s)", configError.Kind)
	}
	return fmt.Sprintf("private configuration failed (%s): %v", configError.Kind, configError.Cause)
}

func (configError *Error) Unwrap() error { return configError.Cause }

func Read(path string) (string, []byte, error) {
	if path == "" {
		return "", nil, &Error{
			Kind: ErrorUnavailable, Cause: fmt.Errorf("configuration path is required"),
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, &Error{Kind: ErrorUnavailable, Cause: fmt.Errorf("resolve path: %w", err)}
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", nil, &Error{Kind: ErrorUnavailable, Cause: fmt.Errorf("inspect path: %w", err)}
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || info.Size() < 1 || info.Size() > serialization.MaxInputBytes {
		return "", nil, &Error{
			Kind:  ErrorSecurity,
			Cause: fmt.Errorf("configuration must be a private bounded regular non-symlink file"),
		}
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", nil, &Error{Kind: ErrorUnavailable, Cause: fmt.Errorf("open path: %w", err)}
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) ||
		opened.Mode().Perm()&0o077 != 0 || opened.Size() < 1 ||
		opened.Size() > serialization.MaxInputBytes {
		return "", nil, &Error{Kind: ErrorSecurity, Cause: fmt.Errorf("file changed while opening")}
	}
	raw, err := io.ReadAll(io.LimitReader(file, serialization.MaxInputBytes+1))
	if err != nil || len(raw) > serialization.MaxInputBytes {
		return "", nil, &Error{Kind: ErrorUnavailable, Cause: fmt.Errorf("bounded read failed")}
	}
	return absolute, bytes.Clone(raw), nil
}
