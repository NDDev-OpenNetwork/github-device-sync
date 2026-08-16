package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type stateAuthority struct {
	root           *os.Root
	parentIdentity os.FileInfo
	fileIdentity   os.FileInfo
	lockFile       *os.File
	lockIdentity   os.FileInfo
	directory      string
	name           string
}

func openStateAuthority(path string) (*stateAuthority, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve state path: %w", err)
	}
	directory, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, fmt.Errorf("resolve state parent: %w", err)
	}
	root, parentIdentity, err := openPrivateStateRoot(directory)
	if err != nil {
		return nil, err
	}
	authority := &stateAuthority{
		root: root, parentIdentity: parentIdentity, directory: directory, name: filepath.Base(absolute),
	}
	identity, err := authority.root.Lstat(authority.name)
	if err != nil {
		_ = authority.Close()
		return nil, fmt.Errorf("inspect state database: %w", err)
	}
	if err := validateStateFile(identity, authority.path()); err != nil {
		_ = authority.Close()
		return nil, err
	}
	authority.fileIdentity = identity
	lockName := stateAuthorityLockName(authority.name)
	lockFile, err := openStateAuthorityLock(authority.root, lockName)
	if err != nil {
		_ = authority.Close()
		return nil, fmt.Errorf("acquire state lifetime authority lock: %w", err)
	}
	lockIdentity, err := lockFile.Stat()
	if err != nil || !lockIdentity.Mode().IsRegular() || lockIdentity.Mode().Perm()&0o077 != 0 ||
		!ownedByCurrentUser(lockIdentity) {
		_ = lockFile.Close()
		_ = authority.Close()
		return nil, errors.New("state lifetime authority lock is not private")
	}
	authority.lockFile = lockFile
	authority.lockIdentity = lockIdentity
	return authority, nil
}

func openPrivateStateRoot(directory string) (*os.Root, os.FileInfo, error) {
	before, err := os.Lstat(directory)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, nil, fmt.Errorf("state parent must be a real directory: %s", directory)
	}
	if before.Mode().Perm()&0o077 != 0 {
		return nil, nil, fmt.Errorf(
			"state directory permissions must not allow group or other access: %s", directory,
		)
	}
	if !ownedByCurrentUser(before) {
		return nil, nil, fmt.Errorf("state directory must be owned by the current user: %s", directory)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		return nil, nil, errors.New("state directory identity changed during open")
	}
	return root, opened, nil
}

func (authority *stateAuthority) path() string {
	return filepath.Join(authority.directory, authority.name)
}

func (authority *stateAuthority) verify() error {
	if authority == nil || authority.root == nil || authority.fileIdentity == nil ||
		authority.lockFile == nil || authority.lockIdentity == nil {
		return errors.New("state database authority is unavailable")
	}
	parent, err := authority.root.Stat(".")
	if err != nil || !os.SameFile(authority.parentIdentity, parent) ||
		parent.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(parent) {
		return errors.New("state directory authority changed while the database was open")
	}
	currentParent, err := os.Lstat(authority.directory)
	if err != nil || currentParent.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(authority.parentIdentity, currentParent) {
		return errors.New("state directory pathname no longer names the opened authority")
	}
	current, err := authority.root.Lstat(authority.name)
	if err != nil || !os.SameFile(authority.fileIdentity, current) {
		return errors.New("state database identity changed while it was open")
	}
	currentPath, err := os.Lstat(authority.path())
	if err != nil || currentPath.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(authority.fileIdentity, currentPath) {
		return errors.New("state database pathname no longer names the opened authority")
	}
	if err := validateStateFile(current, authority.path()); err != nil {
		return err
	}
	lockName := stateAuthorityLockName(authority.name)
	lockInfo, err := authority.root.Lstat(lockName)
	if err != nil || !os.SameFile(authority.lockIdentity, lockInfo) || !lockInfo.Mode().IsRegular() ||
		lockInfo.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(lockInfo) {
		return errors.New("state lifetime authority lock changed while the database was open")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		name := authority.name + suffix
		info, sidecarErr := authority.root.Lstat(name)
		if errors.Is(sidecarErr, os.ErrNotExist) {
			continue
		}
		if sidecarErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(info) {
			return fmt.Errorf("state database sidecar is not private and stable: %s", name)
		}
	}
	return nil
}

func stateAuthorityLockName(databaseName string) string {
	return databaseName + ".gds-authority.lock"
}

func (authority *stateAuthority) Close() error {
	if authority == nil || authority.root == nil {
		return nil
	}
	var lockErr error
	if authority.lockFile != nil {
		lockErr = authority.lockFile.Close()
		authority.lockFile = nil
	}
	err := authority.root.Close()
	authority.root = nil
	return errors.Join(lockErr, err)
}

func validateStateFile(info os.FileInfo, path string) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("state database must be a regular non-symlink file: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("state database permissions must be 0600 or stricter: %s", path)
	}
	if !ownedByCurrentUser(info) {
		return fmt.Errorf("state database must be owned by the current user: %s", path)
	}
	return nil
}
