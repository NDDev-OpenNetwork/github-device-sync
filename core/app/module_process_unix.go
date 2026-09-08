//go:build darwin || linux

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const moduleTerminationGrace = 250 * time.Millisecond
const moduleTerminationWait = 2 * time.Second

// Own a new group, never the caller's group. Cancel and normal-exit cleanup
// share one synchronous operation, so no delayed signal goroutine outlives the
// command or its verification workspace.
func configureModuleProcess(command *exec.Cmd) (func() (bool, error), error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var once sync.Once
	var present bool
	var stopErr error
	stop := func() (bool, error) {
		once.Do(func() {
			if command.Process == nil {
				return
			}
			pid := command.Process.Pid
			if pid <= 1 || pid == syscall.Getpgrp() {
				stopErr = errors.New("refusing to signal an unowned process group")
				return
			}
			err := syscall.Kill(-pid, syscall.SIGTERM)
			if errors.Is(err, syscall.ESRCH) {
				return
			}
			present = true
			if err != nil {
				stopErr = fmt.Errorf("terminate module process group: %w", err)
				return
			}
			time.Sleep(moduleTerminationGrace)
			if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				stopErr = fmt.Errorf("kill module process group: %w", err)
				return
			}
			deadline := time.Now().Add(moduleTerminationWait)
			for {
				running, err := moduleProcessGroupRunning(pid)
				if err != nil {
					stopErr = err
					return
				}
				if !running {
					return
				}
				if time.Now().After(deadline) {
					stopErr = errors.New("module process group did not stop within cleanup deadline")
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
		return present, stopErr
	}
	command.Cancel = func() error {
		present, err := stop()
		if !present && err == nil {
			return os.ErrProcessDone
		}
		return err
	}
	return stop, nil
}
