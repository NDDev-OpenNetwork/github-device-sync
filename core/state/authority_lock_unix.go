//go:build darwin || linux

package state

import (
	"errors"
	"os"
	"syscall"
)

func openStateAuthorityLock(root *os.Root, name string) (*os.File, error) {
	file, err := root.OpenFile(
		name, os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600,
	)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.New("state authority is exclusively locked for a cooperative pathname mutation")
		}
		return nil, err
	}
	return file, nil
}
