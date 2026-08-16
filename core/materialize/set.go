// Package materialize provides confined, atomic, rollback-capable file-set
// materialization for journaled GDS operations.
package materialize

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
)

const maxMaterializationFileBytes = 8 << 20

var (
	// ErrMaterializationConflict reports that a target or parent path changed
	// after GDS captured the state on which a write or rollback was conditioned.
	ErrMaterializationConflict = errors.New("materialization target state changed")
	// ErrMaterializationPartial reports that an apply failed and at least one
	// earlier write could not be durably rolled back.
	ErrMaterializationPartial = errors.New("materialization rollback incomplete")
)

type File struct {
	Path    string
	Content []byte
	Digest  string
}

type ObservedFile struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Digest string `json:"digest,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

type Set struct {
	root  string
	files []File
	hooks materializationHooks
}

// materializationHooks provides package-private synchronization points for
// deterministic race tests. Production sets leave both callbacks nil.
type materializationHooks struct {
	beforeWriteCompare    func(string)
	beforeRollbackCompare func(string)
}

type fileState struct {
	present        bool
	parentIdentity os.FileInfo
	identity       os.FileInfo
	mode           os.FileMode
	digest         string
}

type backup struct {
	path     string
	existed  bool
	content  []byte
	mode     os.FileMode
	expected fileState
	written  fileState
}

type writeResult struct {
	renamed bool
	written fileState
}

func NewSet(root string, files []File) (*Set, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve materialization root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("materialization root is not a directory: %s", absolute)
	}
	if len(files) == 0 {
		return nil, errors.New("materialization file set is empty")
	}
	copyOfFiles := make([]File, len(files))
	seen := map[string]struct{}{}
	for index, file := range files {
		if len(file.Content) > maxMaterializationFileBytes {
			return nil, fmt.Errorf(
				"materialization content exceeds %d bytes: %s",
				maxMaterializationFileBytes, file.Path,
			)
		}
		clean, err := safeRelativePath(file.Path)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[clean]; duplicate {
			return nil, fmt.Errorf("duplicate materialization path %q", clean)
		}
		seen[clean] = struct{}{}
		copyOfFiles[index] = File{
			Path: clean, Content: append([]byte(nil), file.Content...), Digest: file.Digest,
		}
	}
	sort.Slice(copyOfFiles, func(left, right int) bool {
		return copyOfFiles[left].Path < copyOfFiles[right].Path
	})
	return &Set{root: absolute, files: copyOfFiles}, nil
}

func (set *Set) Fingerprint(inputDigest string) (string, error) {
	observed, err := set.Observe()
	if err != nil {
		return "", err
	}
	return canonicaljson.Digest(map[string]any{
		"input_digest": inputDigest,
		"files":        observed,
	})
}

func (set *Set) Observe() ([]ObservedFile, error) {
	root, err := os.OpenRoot(set.root)
	if err != nil {
		return nil, fmt.Errorf("open materialization root: %w", err)
	}
	defer root.Close()
	return set.observe(root)
}

func (set *Set) observe(root *os.Root) ([]ObservedFile, error) {
	observed := make([]ObservedFile, 0, len(set.files))
	for _, file := range set.files {
		path := filepath.FromSlash(file.Path)
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			observed = append(observed, ObservedFile{Path: file.Path, State: "missing"})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect materialization %s: %w", file.Path, err)
		}
		item := ObservedFile{Path: file.Path, Mode: info.Mode().String()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.State = "symlink"
		case info.Mode().IsRegular():
			content, err := readStableRegular(root, path, info)
			if err != nil {
				return nil, fmt.Errorf("read materialization %s: %w", file.Path, err)
			}
			item.State = "regular"
			item.Digest = digest(content)
		default:
			item.State = "other"
		}
		observed = append(observed, item)
	}
	return observed, nil
}

func (set *Set) Apply() ([]ObservedFile, []ObservedFile, error) {
	root, err := os.OpenRoot(set.root)
	if err != nil {
		return nil, nil, fmt.Errorf("open materialization root: %w", err)
	}
	defer root.Close()
	before, err := set.observe(root)
	if err != nil {
		return nil, nil, err
	}
	backups := make([]backup, 0, len(set.files))
	for _, file := range set.files {
		current, err := captureBackup(root, file.Path)
		if err != nil {
			return before, nil, rollbackError(err, rollback(root, backups, set.hooks))
		}
		result, err := atomicWriteRoot(
			root, file.Path, file.Content, 0o644, current.expected,
			set.hooks.beforeWriteCompare,
		)
		if result.renamed {
			current.written = result.written
			backups = append(backups, current)
		}
		if err != nil {
			return before, nil, rollbackError(err, rollback(root, backups, set.hooks))
		}
	}
	if err := set.verify(root); err != nil {
		return before, nil, rollbackError(err, rollback(root, backups, set.hooks))
	}
	after, err := set.observe(root)
	if err != nil {
		return before, nil, rollbackError(err, rollback(root, backups, set.hooks))
	}
	return before, after, nil
}

func (set *Set) Verify() error {
	root, err := os.OpenRoot(set.root)
	if err != nil {
		return fmt.Errorf("open materialization root: %w", err)
	}
	defer root.Close()
	return set.verify(root)
}

func (set *Set) verify(root *os.Root) error {
	observed, err := set.observe(root)
	if err != nil {
		return err
	}
	expected := map[string]string{}
	for _, file := range set.files {
		expected[file.Path] = file.Digest
	}
	for _, file := range observed {
		if file.State != "regular" || file.Digest != expected[file.Path] {
			return fmt.Errorf("materialized file %s does not match its approved digest", file.Path)
		}
	}
	return nil
}

func safeRelativePath(relative string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, "../") || clean != relative {
		return "", fmt.Errorf("unsafe materialization path %q", relative)
	}
	return clean, nil
}

func captureBackup(root *os.Root, relative string) (backup, error) {
	parent, base, err := openSafeParent(root, relative, false)
	if errors.Is(err, os.ErrNotExist) {
		return backup{path: relative}, nil
	}
	if err != nil {
		return backup{}, fmt.Errorf("open existing materialization parent: %w", err)
	}
	defer parent.Close()
	parentInfo, err := parent.Stat(".")
	if err != nil {
		return backup{}, fmt.Errorf("inspect existing materialization parent: %w", err)
	}
	info, err := parent.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return backup{
			path: relative, expected: fileState{parentIdentity: parentInfo},
		}, nil
	}
	if err != nil {
		return backup{}, fmt.Errorf("inspect existing materialization: %w", err)
	}
	path := filepath.FromSlash(relative)
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return backup{}, fmt.Errorf("existing materialization is not a regular file: %s", path)
	}
	content, err := readStableRegular(parent, base, info)
	if err != nil {
		return backup{}, fmt.Errorf("read existing materialization: %w", err)
	}
	confirmed, err := parent.Lstat(base)
	if err != nil || !confirmed.Mode().IsRegular() || !os.SameFile(info, confirmed) {
		return backup{}, conflictError("existing materialization changed while capturing %s", relative)
	}
	mode := confirmed.Mode()
	return backup{
		path: relative, existed: true, content: content, mode: mode,
		expected: fileState{
			present: true, parentIdentity: parentInfo,
			identity: confirmed, mode: mode, digest: digest(content),
		},
	}, nil
}

func rollback(root *os.Root, backups []backup, hooks materializationHooks) error {
	errorsSeen := []error{}
	for index := len(backups) - 1; index >= 0; index-- {
		item := backups[index]
		if item.existed {
			if _, err := atomicWriteRoot(
				root, item.path, item.content, item.mode, item.written,
				hooks.beforeRollbackCompare,
			); err != nil {
				errorsSeen = append(errorsSeen, fmt.Errorf("restore %s: %w", item.path, err))
			}
			continue
		}
		if err := removeRootFile(
			root, item.path, item.written, hooks.beforeRollbackCompare,
		); err != nil {
			errorsSeen = append(errorsSeen, fmt.Errorf("remove %s: %w", item.path, err))
		}
	}
	if len(errorsSeen) != 0 {
		return errors.Join(errorsSeen...)
	}
	return nil
}

func rollbackError(applyErr, rollbackErr error) error {
	if rollbackErr == nil {
		return applyErr
	}
	return &partialMaterializationError{applyErr: applyErr, rollbackErr: rollbackErr}
}

type partialMaterializationError struct {
	applyErr    error
	rollbackErr error
}

func (err *partialMaterializationError) Error() string {
	return fmt.Sprintf(
		"%s: apply failed: %v; rollback failed: %v",
		ErrMaterializationPartial, err.applyErr, err.rollbackErr,
	)
}

func (err *partialMaterializationError) Unwrap() []error {
	return []error{ErrMaterializationPartial, err.applyErr, err.rollbackErr}
}

func atomicWriteRoot(
	root *os.Root,
	relative string,
	content []byte,
	mode os.FileMode,
	expected fileState,
	beforeCompare func(string),
) (writeResult, error) {
	parent, base, err := openSafeParent(root, relative, true)
	if err != nil {
		return writeResult{}, err
	}
	defer parent.Close()
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return writeResult{}, fmt.Errorf("allocate materialization temporary name: %w", err)
	}
	temporaryName := ".gds-materialize-" + hex.EncodeToString(random[:])
	temporary, err := parent.OpenFile(temporaryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return writeResult{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = parent.Remove(temporaryName)
	}()
	if err := temporary.Chmod(mode); err != nil {
		return writeResult{}, err
	}
	if _, err := temporary.Write(content); err != nil {
		return writeResult{}, err
	}
	if err := temporary.Sync(); err != nil {
		return writeResult{}, err
	}
	temporaryInfo, err := temporary.Stat()
	if err != nil {
		return writeResult{}, err
	}
	if err := temporary.Close(); err != nil {
		return writeResult{}, err
	}
	closed = true
	parentInfo, err := parent.Stat(".")
	if err != nil {
		return writeResult{}, err
	}
	written := fileState{
		present: true, parentIdentity: parentInfo,
		identity: temporaryInfo, mode: temporaryInfo.Mode(), digest: digest(content),
	}
	if beforeCompare != nil {
		beforeCompare(relative)
	}
	// This is an expected-state compare immediately before rename. Portable Go
	// and os.Root do not expose an atomic compare-and-replace primitive, so a
	// narrow residual TOCTOU remains between this check and Rename. Rechecking
	// here is still fail-closed for changes completed before the commit point.
	if err := compareParentPath(root, parent, relative); err != nil {
		return writeResult{}, err
	}
	if err := compareCurrentState(parent, base, relative, expected); err != nil {
		return writeResult{}, err
	}
	if err := parent.Rename(temporaryName, base); err != nil {
		return writeResult{}, err
	}
	result := writeResult{renamed: true, written: written}
	directoryHandle, err := parent.Open(".")
	if err != nil {
		return result, err
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return result, err
	}
	return result, nil
}

func openSafeParent(root *os.Root, relative string, create bool) (*os.Root, string, error) {
	clean, err := safeRelativePath(relative)
	if err != nil {
		return nil, "", err
	}
	path := filepath.FromSlash(clean)
	base := filepath.Base(path)
	parentPath := filepath.Dir(path)
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, "", fmt.Errorf("duplicate materialization root: %w", err)
	}
	if parentPath == "." {
		return current, base, nil
	}
	for _, part := range strings.Split(parentPath, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		info, err := current.Lstat(part)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := current.Mkdir(part, 0o755); err != nil {
				current.Close()
				return nil, "", fmt.Errorf("create materialization directory %s: %w", part, err)
			}
			info, err = current.Lstat(part)
		}
		if err != nil {
			current.Close()
			return nil, "", fmt.Errorf("inspect materialization directory %s: %w", part, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			current.Close()
			return nil, "", fmt.Errorf("materialization parent is not a regular directory: %s", part)
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			current.Close()
			return nil, "", fmt.Errorf("open materialization directory %s: %w", part, err)
		}
		openedInfo, err := next.Stat(".")
		if err != nil || !os.SameFile(info, openedInfo) {
			next.Close()
			current.Close()
			return nil, "", fmt.Errorf("materialization directory changed during secure open: %s", part)
		}
		current.Close()
		current = next
	}
	return current, base, nil
}

func readStableRegular(root *os.Root, path string, expected os.FileInfo) ([]byte, error) {
	if expected.Size() > maxMaterializationFileBytes {
		return nil, fmt.Errorf("materialization file exceeds %d bytes: %s", maxMaterializationFileBytes, path)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return nil, fmt.Errorf("materialization file changed during secure open: %s", path)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxMaterializationFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxMaterializationFileBytes {
		return nil, fmt.Errorf("materialization file exceeds %d bytes: %s", maxMaterializationFileBytes, path)
	}
	return content, nil
}

func compareCurrentState(
	parent *os.Root,
	base string,
	relative string,
	expected fileState,
) error {
	if expected.parentIdentity != nil {
		parentInfo, err := parent.Stat(".")
		if err != nil || !os.SameFile(expected.parentIdentity, parentInfo) {
			return conflictError("materialization parent identity changed before commit: %s", relative)
		}
	}
	info, err := parent.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		if expected.present {
			return conflictError("materialization disappeared before commit: %s", relative)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect materialization %s before commit: %w", relative, err)
	}
	if !expected.present {
		return conflictError("materialization appeared before commit: %s", relative)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return conflictError("materialization type changed before commit: %s", relative)
	}
	if expected.identity == nil || !os.SameFile(expected.identity, info) {
		return conflictError("materialization identity changed before commit: %s", relative)
	}
	if info.Mode() != expected.mode {
		return conflictError("materialization mode changed before commit: %s", relative)
	}
	content, err := readStableRegular(parent, base, info)
	if err != nil {
		return conflictError("materialization changed while comparing %s: %v", relative, err)
	}
	if digest(content) != expected.digest {
		return conflictError("materialization content changed before commit: %s", relative)
	}
	return nil
}

func compareParentPath(root, openedParent *os.Root, relative string) error {
	reopenedParent, _, err := openSafeParent(root, relative, false)
	if err != nil {
		return conflictError("materialization parent changed before commit for %s: %v", relative, err)
	}
	defer reopenedParent.Close()
	openedInfo, openedErr := openedParent.Stat(".")
	reopenedInfo, reopenedErr := reopenedParent.Stat(".")
	if openedErr != nil || reopenedErr != nil || !os.SameFile(openedInfo, reopenedInfo) {
		return conflictError("materialization parent identity changed before commit: %s", relative)
	}
	return nil
}

func removeRootFile(
	root *os.Root,
	relative string,
	expected fileState,
	beforeCompare func(string),
) error {
	parent, base, err := openSafeParent(root, relative, false)
	if err != nil {
		return conflictError("open materialization for rollback %s: %v", relative, err)
	}
	defer parent.Close()
	if beforeCompare != nil {
		beforeCompare(relative)
	}
	// As with compare-and-rename above, portable os.Root has no conditional
	// unlink. The compare is intentionally adjacent to Remove to minimize the
	// unavoidable check/remove window.
	if err := compareParentPath(root, parent, relative); err != nil {
		return err
	}
	if err := compareCurrentState(parent, base, relative, expected); err != nil {
		return err
	}
	if err := parent.Remove(base); err != nil {
		return err
	}
	directoryHandle, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}

func conflictError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrMaterializationConflict, fmt.Sprintf(format, arguments...))
}

func digest(content []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(content))
}
