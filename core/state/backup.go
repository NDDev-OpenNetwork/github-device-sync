package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type BackupInfo struct {
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	Digest        string `json:"digest"`
	SchemaVersion int    `json:"schema_version"`
	LogicalDigest string `json:"logical_digest"`
	Integrity     string `json:"integrity"`
}

func (store *Store) Backup(ctx context.Context, target string) (info BackupInfo, returnErr error) {
	if store == nil || store.db == nil || store.readOnly {
		return BackupInfo{}, fmt.Errorf("writable state store is required for backup")
	}
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || target == store.path {
		return BackupInfo{}, fmt.Errorf("backup target must be a distinct clean absolute path")
	}
	parent := filepath.Dir(target)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 ||
		parentInfo.Mode().Perm()&0o077 != 0 {
		return BackupInfo{}, fmt.Errorf("backup parent must be an existing private real directory")
	}
	if _, err := os.Lstat(target); err == nil || !os.IsNotExist(err) {
		return BackupInfo{}, fmt.Errorf("backup target must not already exist")
	}
	temporaryDirectory, err := os.MkdirTemp(parent, ".gds-backup-")
	if err != nil {
		return BackupInfo{}, fmt.Errorf("create private backup workspace: %w", err)
	}
	cleanupNeeded := true
	defer func() {
		if !cleanupNeeded {
			return
		}
		if err := os.RemoveAll(temporaryDirectory); err != nil {
			info = BackupInfo{}
			returnErr = errors.Join(returnErr, fmt.Errorf("clean backup workspace: %w", err))
		}
	}()
	candidate := filepath.Join(temporaryDirectory, "state.db")
	if _, err := store.db.ExecContext(ctx, "VACUUM INTO ?", candidate); err != nil {
		return BackupInfo{}, fmt.Errorf("create consistent SQLite backup: %w", err)
	}
	if err := os.Chmod(candidate, 0o600); err != nil {
		return BackupInfo{}, fmt.Errorf("protect state backup: %w", err)
	}
	file, err := os.Open(candidate)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("open state backup: %w", err)
	}
	digest := sha256.New()
	size, copyErr := io.Copy(digest, file)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return BackupInfo{}, fmt.Errorf("verify durable state backup")
	}
	candidateInfo, err := os.Lstat(candidate)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("inspect verified backup candidate: %w", err)
	}
	if !candidateInfo.Mode().IsRegular() || candidateInfo.Mode()&os.ModeSymlink != 0 {
		return BackupInfo{}, fmt.Errorf("verified backup candidate is not a regular file")
	}
	verification, err := Snapshot(ctx, candidate)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("verify state backup content: %w", err)
	}
	if verification.SchemaVersion != schemaVersion || verification.Integrity != "pass" ||
		verification.LogicalDigest == "" {
		return BackupInfo{}, fmt.Errorf(
			"state backup verification is incomplete: schema=%d integrity=%q logical_digest=%q",
			verification.SchemaVersion, verification.Integrity, verification.LogicalDigest,
		)
	}
	if err := os.Remove(filepath.Join(temporaryDirectory, stateAuthorityLockName("state.db"))); err != nil {
		return BackupInfo{}, fmt.Errorf("remove verified backup authority lock: %w", err)
	}
	directory, err := os.Open(parent)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("open backup directory: %w", err)
	}
	published := false
	committed := false
	defer func() {
		if !published || committed {
			return
		}
		if err := removeBackupIfSame(target, candidateInfo); err != nil {
			info = BackupInfo{}
			returnErr = errors.Join(returnErr, fmt.Errorf("roll back unpublished backup: %w", err))
		}
	}()
	if err := publishBackupCandidate(candidate, target); err != nil {
		_ = directory.Close()
		return BackupInfo{}, fmt.Errorf("publish backup without overwrite: %w", err)
	}
	published = true
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return BackupInfo{}, fmt.Errorf("sync published backup directory: %w", err)
	}
	if err := os.Remove(candidate); err != nil {
		_ = directory.Close()
		return BackupInfo{}, fmt.Errorf("remove linked backup candidate: %w", err)
	}
	if err := os.Remove(temporaryDirectory); err != nil {
		_ = directory.Close()
		return BackupInfo{}, fmt.Errorf("remove backup workspace: %w", err)
	}
	cleanupNeeded = false
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return BackupInfo{}, fmt.Errorf("sync backup workspace cleanup: %w", err)
	}
	if err := directory.Close(); err != nil {
		return BackupInfo{}, fmt.Errorf("close backup directory: %w", err)
	}
	committed = true
	return BackupInfo{
		Path: target, Size: size, Digest: fmt.Sprintf("sha256:%x", digest.Sum(nil)),
		SchemaVersion: verification.SchemaVersion, LogicalDigest: verification.LogicalDigest,
		Integrity: verification.Integrity,
	}, nil
}

func publishBackupCandidate(candidate string, target string) error {
	return os.Link(candidate, target)
}

func removeBackupIfSame(target string, expected os.FileInfo) error {
	observed, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if expected == nil || observed.Mode()&os.ModeSymlink != 0 || !observed.Mode().IsRegular() ||
		!os.SameFile(expected, observed) {
		return fmt.Errorf("backup target identity changed")
	}
	if err := os.Remove(target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
