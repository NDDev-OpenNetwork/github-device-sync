package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

var backupNamePattern = regexp.MustCompile(`^state-[0-9]{8}T[0-9]{6}Z\.db$`)

type BackupManager struct {
	Store     *state.Store
	Directory string
	Retain    int
	Retention state.RetentionPolicy
	Now       func() time.Time
}

func (manager *BackupManager) Prepare() error {
	if manager == nil || manager.Store == nil || !filepath.IsAbs(manager.Directory) ||
		manager.Retain < 2 || manager.Retain > 90 ||
		manager.Retention.TerminalWebhookAge < 24*time.Hour ||
		manager.Retention.ReconciliationAge < 30*24*time.Hour {
		return fmt.Errorf("backup manager configuration is invalid")
	}
	if err := os.MkdirAll(manager.Directory, 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	info, err := os.Lstat(manager.Directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("backup directory must be a private real directory")
	}
	return nil
}

func (manager *BackupManager) Run(ctx context.Context) (state.BackupInfo, error) {
	if err := manager.Prepare(); err != nil {
		return state.BackupInfo{}, err
	}
	now := time.Now().UTC()
	if manager.Now != nil {
		now = manager.Now().UTC()
	}
	target := filepath.Join(manager.Directory, "state-"+now.Format("20060102T150405Z")+".db")
	backup, err := manager.Store.Backup(ctx, target)
	if err != nil {
		return state.BackupInfo{}, err
	}
	if err := manager.prune(); err != nil {
		return backup, err
	}
	if _, err := manager.Store.PruneControllerData(ctx, now, manager.Retention); err != nil {
		return backup, err
	}
	return backup, nil
}

func (manager *BackupManager) prune() error {
	entries, err := os.ReadDir(manager.Directory)
	if err != nil {
		return fmt.Errorf("list backup directory: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if backupNamePattern.MatchString(entry.Name()) {
			paths = append(paths, filepath.Join(manager.Directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	for _, path := range paths[:max(0, len(paths)-manager.Retain)] {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect retained backup: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup retention encountered an unsafe path")
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove expired backup: %w", err)
		}
	}
	return nil
}
