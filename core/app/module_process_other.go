//go:build !darwin && !linux

package app

import (
	"errors"
	"os/exec"
)

func configureModuleProcess(_ *exec.Cmd) (func() (bool, error), error) {
	return nil, errors.New("module command process ownership is supported only on Linux and macOS")
}
