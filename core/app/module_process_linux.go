//go:build linux

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func moduleProcessGroupRunning(group int) (bool, error) {
	if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
		return false, nil
	} else if err != nil {
		return true, err
	}
	// Killed orphans may await init's reaping. Zombies cannot execute or write
	// the checkout; do not mistake their remaining group membership for work.
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true, err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return true, fmt.Errorf("inspect process group: %w", err)
		}
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			return true, errors.New("process status is incomplete")
		}
		fields := strings.Fields(string(raw)[end+1:])
		if len(fields) < 3 {
			return true, errors.New("process status is incomplete")
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil {
			return true, err
		}
		if pgid == group && fields[0] != "Z" && fields[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}
