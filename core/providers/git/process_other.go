//go:build !darwin && !linux

package git

import (
	"os/exec"
	"time"
)

func configureCancellation(command *exec.Cmd) {
	command.WaitDelay = 2 * time.Second
}
