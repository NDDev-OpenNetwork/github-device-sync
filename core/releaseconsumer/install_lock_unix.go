//go:build darwin || linux

package releaseconsumer

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

const installScopeLockPollInterval = 10 * time.Millisecond

func openInstallScopeLockFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(
		name,
		os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
}

func acquireInstallScopeLock(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(installScopeLockPollInterval)
	defer ticker.Stop()
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
