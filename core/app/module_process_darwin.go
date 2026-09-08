//go:build darwin

package app

import (
	"errors"
	"syscall"
)

func moduleProcessGroupRunning(group int) (bool, error) {
	err := syscall.Kill(-group, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return true, err
}
