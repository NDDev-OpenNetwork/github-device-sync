package releaseconsumer

import (
	"context"
	"errors"
	"fmt"
	"os"
)

const installScopeLockName = ".gds-install.lock"

var (
	ErrInstallScopeLock     = errors.New("release installation scope lock failed")
	ErrActivationConflict   = errors.New("release activation compare-and-swap failed")
	ErrActivationIntegrity  = errors.New("release activation integrity failed")
	ErrActivationAcceptance = errors.New("release activation acceptance failed")
	ErrActivationRecovery   = errors.New("release activation recovery failed")
)

// withInstallScopeLock serializes cooperative lifecycle mutations for one
// canonical install root. The current pointer is root-wide, so every trust
// domain deliberately shares the same physical lock; ResolveInstallScope still
// validates the candidate's canonical root and trust-domain identity.
func withInstallScopeLock(
	ctx context.Context,
	candidate InstallCandidate,
	action func() error,
) error {
	if ctx == nil || action == nil {
		return fmt.Errorf("%w: lock inputs are invalid", ErrInstallScopeLock)
	}
	rootPath, _, err := ResolveInstallScope(candidate.InstallRoot, candidate.Record.TrustDomain)
	if err != nil || rootPath != candidate.InstallRoot {
		return fmt.Errorf("%w: installation scope is invalid", ErrInstallScopeLock)
	}
	if err := ensureInstallRoot(rootPath); err != nil {
		return fmt.Errorf("%w: prepare installation root: %w", ErrInstallScopeLock, err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("%w: open installation root: %w", ErrInstallScopeLock, err)
	}
	lockFile, openErr := openInstallScopeLockFile(root, installScopeLockName)
	rootCloseErr := root.Close()
	if openErr != nil {
		return fmt.Errorf("%w: open lock file: %w", ErrInstallScopeLock, openErr)
	}
	if rootCloseErr != nil {
		_ = lockFile.Close()
		return fmt.Errorf("%w: close installation root: %w", ErrInstallScopeLock, rootCloseErr)
	}
	info, err := lockFile.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = lockFile.Close()
		return fmt.Errorf("%w: lock path is not a regular file", ErrInstallScopeLock)
	}
	if err := acquireInstallScopeLock(ctx, lockFile); err != nil {
		_ = lockFile.Close()
		return fmt.Errorf("%w: acquire lock: %w", ErrInstallScopeLock, err)
	}
	actionErr := action()
	closeErr := lockFile.Close()
	if actionErr != nil {
		if closeErr != nil {
			return errors.Join(
				actionErr,
				fmt.Errorf("%w: release lock: %w", ErrInstallScopeLock, closeErr),
			)
		}
		return actionErr
	}
	if closeErr != nil {
		return fmt.Errorf("%w: release lock: %w", ErrInstallScopeLock, closeErr)
	}
	return nil
}
