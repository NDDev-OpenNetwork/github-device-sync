package releasebuilder

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- legacy Git object identity requires SHA-1.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitauthority"
)

const (
	// Commit metadata is not part of the tracked-content budget. Bounding the
	// exact commit object before fetch also bounds export-subst source material.
	maxSourceSnapshotCommitObjectBytes = 1 << 20
	maxSourceSnapshotFileBytes         = 128 << 20
	maxSourceSnapshotContentBytes      = 512 << 20
	maxSourceSnapshotFiles             = 10000
	maxSourceSnapshotEntries           = maxSourceSnapshotFiles*4 + 1

	// The tar stream admits the tracked bytes, a deliberate export-subst
	// expansion budget, and bounded headers/padding for every accepted entry.
	maxSourceSnapshotExportSubstBytes      = 32 << 20
	maxSourceSnapshotTarEntryOverheadBytes = 2 << 10
	maxSourceSnapshotTarTrailerBytes       = 10 << 10
	maxSourceSnapshotArchiveBytes          = maxSourceSnapshotContentBytes +
		maxSourceSnapshotExportSubstBytes +
		maxSourceSnapshotEntries*maxSourceSnapshotTarEntryOverheadBytes +
		maxSourceSnapshotTarTrailerBytes
)

var (
	errSourceSnapshotCommitLimit  = errors.New("release source commit object exceeds size limit")
	errSourceSnapshotArchiveLimit = errors.New("release source archive exceeds size limit")
)

type sourceSnapshotLimits struct {
	commitObjectBytes int64
	archiveBytes      int64
}

func productionSourceSnapshotLimits() sourceSnapshotLimits {
	return sourceSnapshotLimits{
		commitObjectBytes: maxSourceSnapshotCommitObjectBytes,
		archiveBytes:      maxSourceSnapshotArchiveBytes,
	}
}

// materializeSourceSnapshot copies the exact inspected commit into a private,
// read-only tree. The intermediate bare repository prevents caller-local Git
// attributes and later worktree mutations from changing release inputs.
func materializeSourceSnapshot(
	ctx context.Context,
	repositoryRoot string,
	temporary string,
	commit string,
	home string,
) (snapshotRoot string, tracked []string, returnErr error) {
	authority, err := gitauthority.Discover()
	if err != nil {
		return "", nil, err
	}
	return materializeSourceSnapshotWithAuthority(
		ctx, authority, repositoryRoot, temporary, commit, home, productionSourceSnapshotLimits(),
	)
}

func materializeSourceSnapshotWithLimits(
	ctx context.Context,
	repositoryRoot string,
	temporary string,
	commit string,
	home string,
	limits sourceSnapshotLimits,
) (snapshotRoot string, tracked []string, returnErr error) {
	authority, err := gitauthority.Discover()
	if err != nil {
		return "", nil, err
	}
	return materializeSourceSnapshotWithAuthority(
		ctx, authority, repositoryRoot, temporary, commit, home, limits,
	)
}

func materializeSourceSnapshotWithAuthority(
	ctx context.Context,
	authority *gitauthority.Authority,
	repositoryRoot string,
	temporary string,
	commit string,
	home string,
	limits sourceSnapshotLimits,
) (snapshotRoot string, tracked []string, returnErr error) {
	if !gitOID(commit) {
		return "", nil, errors.New("release snapshot commit is invalid")
	}
	if limits.commitObjectBytes < 1 || limits.commitObjectBytes > maxSourceSnapshotCommitObjectBytes ||
		limits.archiveBytes < 1 || limits.archiveBytes > maxSourceSnapshotArchiveBytes {
		return "", nil, errors.New("release source snapshot limits are invalid")
	}
	privateGit := filepath.Join(temporary, "source.git")
	archivePath := filepath.Join(temporary, "source.tar")
	snapshotRoot = filepath.Join(temporary, "source")
	environment := []string{"GIT_ATTR_NOSYSTEM=1", "HOME=" + home}
	sort.Strings(environment)
	if err := validateSnapshotCommitSize(
		ctx, authority, repositoryRoot, environment, commit, limits.commitObjectBytes,
	); err != nil {
		return "", nil, err
	}
	inspectedTree, err := inspectWorktreeSnapshotTree(
		ctx, authority, repositoryRoot, environment, commit,
	)
	if err != nil {
		return "", nil, err
	}
	initArguments := []string{"init", "--quiet", "--bare"}
	if len(commit) == 64 {
		initArguments = append(initArguments, "--object-format=sha256")
	}
	initArguments = append(initArguments, privateGit)
	if _, err := runGit(ctx, authority, temporary, environment, initArguments...); err != nil {
		return "", nil, err
	}
	if _, err := runGit(
		ctx, authority, temporary, environment, "--git-dir", privateGit,
		"fetch", "--quiet", "--depth=1", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules",
		"--no-auto-maintenance", "--no-write-commit-graph", "--", repositoryRoot, commit,
	); err != nil {
		return "", nil, err
	}
	treeFiles, err := inspectSnapshotTree(ctx, authority, temporary, environment, privateGit, commit)
	if err != nil {
		return "", nil, err
	}
	if !sameSnapshotTree(inspectedTree, treeFiles) {
		return "", nil, errors.New("release source tree changed during exact commit transfer")
	}
	if err := writeSourceArchive(
		ctx, authority, temporary, environment, privateGit, archivePath, commit, limits.archiveBytes,
	); err != nil {
		return "", nil, err
	}
	if err := os.Mkdir(snapshotRoot, 0o700); err != nil {
		return "", nil, err
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(snapshotRoot)
		}
	}()
	tracked, err = extractSourceSnapshot(archivePath, snapshotRoot)
	if err != nil {
		return "", nil, err
	}
	if err := verifySourceSnapshot(snapshotRoot, treeFiles, tracked, len(commit)); err != nil {
		return "", nil, err
	}
	if err := os.Remove(archivePath); err != nil {
		return "", nil, fmt.Errorf("remove release source archive: %w", err)
	}
	if err := os.RemoveAll(privateGit); err != nil {
		return "", nil, fmt.Errorf("remove private release object database: %w", err)
	}
	if err := lockSourceSnapshot(snapshotRoot); err != nil {
		return "", nil, err
	}
	return snapshotRoot, tracked, nil
}

func validateSnapshotCommitSize(
	ctx context.Context,
	authority *gitauthority.Authority,
	repositoryRoot string,
	environment []string,
	commit string,
	maximum int64,
) error {
	result, err := runGit(
		ctx, authority, repositoryRoot, environment, "--no-replace-objects", "cat-file", "-s", commit,
	)
	if err != nil {
		return err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(result.stdout)), 10, 64)
	if err != nil || size < 1 {
		return errors.New("release source commit object size is invalid")
	}
	if size > maximum {
		return errSourceSnapshotCommitLimit
	}
	return nil
}

func writeSourceArchive(
	ctx context.Context,
	authority *gitauthority.Authority,
	directory string,
	environment []string,
	privateGit string,
	archivePath string,
	commit string,
	maximum int64,
) error {
	if maximum < 1 || maximum > maxSourceSnapshotArchiveBytes {
		return errors.New("release source archive limit is invalid")
	}
	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	removePartial := func(primary error) error {
		closeErr := archive.Close()
		removeErr := os.Remove(archivePath)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(primary, closeErr, removeErr)
	}

	process, startErr := authority.StartStdout(ctx, gitauthority.RunRequest{
		Directory: directory,
		Arguments: []string{
			"--git-dir", privateGit, "-c", "tar.umask=0022", "-c", "core.attributesFile=/dev/null",
			"archive", "--format=tar", commit,
		},
		ExtraEnvironment: environment,
		Stderr:           io.Discard,
	})
	if startErr != nil {
		return removePartial(fmt.Errorf("start release source archive command: %w", startErr))
	}
	copyErr := copyBoundedArchive(archive, process.Stdout(), maximum)
	if copyErr != nil {
		_ = process.Kill()
		_ = process.Wait()
		if errors.Is(copyErr, errSourceSnapshotArchiveLimit) {
			return removePartial(errSourceSnapshotArchiveLimit)
		}
		return removePartial(fmt.Errorf("write release source archive: %w", copyErr))
	}
	if waitErr := process.Wait(); waitErr != nil {
		return removePartial(fmt.Errorf("release source archive command failed: %w", waitErr))
	}
	if err := archive.Sync(); err != nil {
		return removePartial(fmt.Errorf("sync release source archive: %w", err))
	}
	if err := archive.Close(); err != nil {
		removeErr := os.Remove(archivePath)
		return errors.Join(fmt.Errorf("close release source archive: %w", err), removeErr)
	}
	return nil
}

func copyBoundedArchive(destination io.Writer, source io.Reader, maximum int64) error {
	writer := &boundedArchiveWriter{writer: destination, remaining: maximum}
	_, err := io.CopyBuffer(writer, source, make([]byte, 32<<10))
	return err
}

type boundedArchiveWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *boundedArchiveWriter) Write(value []byte) (int, error) {
	allowed := int64(len(value))
	exceeds := allowed > writer.remaining
	if exceeds {
		allowed = writer.remaining
	}
	written, err := writer.writer.Write(value[:int(allowed)])
	writer.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	if int64(written) != allowed {
		return written, io.ErrShortWrite
	}
	if exceeds {
		return written, errSourceSnapshotArchiveLimit
	}
	return written, nil
}

type snapshotTreeFile struct {
	path       string
	oid        string
	size       int64
	executable bool
}

func inspectWorktreeSnapshotTree(
	ctx context.Context,
	authority *gitauthority.Authority,
	repositoryRoot string,
	environment []string,
	commit string,
) ([]snapshotTreeFile, error) {
	result, err := runGit(
		ctx, authority, repositoryRoot, environment, "--no-replace-objects",
		"ls-tree", "-r", "-z", "-l", "--full-tree", commit,
	)
	if err != nil {
		return nil, err
	}
	return parseSnapshotTree(result.stdout)
}

func inspectSnapshotTree(
	ctx context.Context,
	authority *gitauthority.Authority,
	directory string,
	environment []string,
	privateGit string,
	commit string,
) ([]snapshotTreeFile, error) {
	result, err := runGit(
		ctx, authority, directory, environment, "--git-dir", privateGit,
		"ls-tree", "-r", "-z", "-l", "--full-tree", commit,
	)
	if err != nil {
		return nil, err
	}
	return parseSnapshotTree(result.stdout)
}

func parseSnapshotTree(output []byte) ([]snapshotTreeFile, error) {
	files := []snapshotTreeFile{}
	seen := map[string]struct{}{}
	total := int64(0)
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, errors.New("release source tree entry is malformed")
		}
		identity := strings.Fields(string(parts[0]))
		if len(identity) == 4 && identity[0] == "160000" && identity[1] == "commit" {
			// Submodule gitlink: the module is a separately released repository
			// pinned by commit, not embedded content. The control-plane release
			// snapshot captures its own blobs only; git ls-tree does not recurse
			// into submodules, so this entry is the module's whole tree footprint.
			continue
		}
		relative, pathErr := safeSnapshotPath(string(parts[1]), false)
		if pathErr != nil || len(identity) != 4 || identity[1] != "blob" || !gitOID(identity[2]) {
			return nil, errors.New("release source tree entry is not a regular blob")
		}
		size, sizeErr := strconv.ParseInt(identity[3], 10, 64)
		if sizeErr != nil || size < 0 || size > maxSourceSnapshotFileBytes ||
			total > maxSourceSnapshotContentBytes-size {
			return nil, fmt.Errorf("release source tree content exceeds limits: %s", relative)
		}
		executable := false
		switch identity[0] {
		case "100644":
		case "100755":
			executable = true
		default:
			return nil, fmt.Errorf("release source tree entry has unsupported mode: %s", relative)
		}
		if _, duplicate := seen[relative]; duplicate {
			return nil, fmt.Errorf("release source tree path is duplicated: %s", relative)
		}
		if len(files) >= maxSourceSnapshotFiles {
			return nil, errors.New("release source tree file count exceeds limit")
		}
		seen[relative] = struct{}{}
		total += size
		files = append(files, snapshotTreeFile{
			path: relative, oid: identity[2], size: size, executable: executable,
		})
	}
	if len(files) == 0 {
		return nil, errors.New("release source has no tracked regular files")
	}
	sort.Slice(files, func(left, right int) bool { return files[left].path < files[right].path })
	return files, nil
}

func sameSnapshotTree(left []snapshotTreeFile, right []snapshotTreeFile) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func extractSourceSnapshot(archivePath string, snapshotRoot string) ([]string, error) {
	info, err := os.Lstat(archivePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > maxSourceSnapshotArchiveBytes {
		return nil, errors.New("release source archive is not a bounded regular file")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	root, err := os.OpenRoot(snapshotRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	reader := tar.NewReader(archive)
	seen := map[string]struct{}{}
	tracked := []string{}
	total := int64(0)
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release source archive: %w", err)
		}
		entries++
		if entries > maxSourceSnapshotEntries {
			return nil, errors.New("release source archive entry count exceeds limit")
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		relative, err := safeSnapshotPath(header.Name, header.Typeflag == tar.TypeDir)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[relative]; duplicate {
			return nil, fmt.Errorf("release source archive path is duplicated: %s", relative)
		}
		seen[relative] = struct{}{}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(filepath.FromSlash(relative), 0o700); err != nil {
				return nil, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if len(tracked) >= maxSourceSnapshotFiles || header.Size < 0 ||
				header.Size > maxSourceSnapshotFileBytes || total+header.Size > maxSourceSnapshotContentBytes {
				return nil, fmt.Errorf("release source archive file exceeds limits: %s", relative)
			}
			mode, err := snapshotFileMode(header.Mode)
			if err != nil {
				return nil, fmt.Errorf("release source archive mode is invalid for %s: %w", relative, err)
			}
			name := filepath.FromSlash(relative)
			parent := filepath.Dir(name)
			if parent != "." {
				if err := root.MkdirAll(parent, 0o700); err != nil {
					return nil, err
				}
			}
			file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return nil, err
			}
			written, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil || written != header.Size {
				return nil, fmt.Errorf("release source archive file is incomplete: %s", relative)
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if err := root.Chmod(name, mode); err != nil {
				return nil, err
			}
			total += header.Size
			tracked = append(tracked, relative)
		default:
			return nil, fmt.Errorf("release source archive member is not a regular file or directory: %s", relative)
		}
	}
	if len(tracked) == 0 {
		return nil, errors.New("release source has no tracked regular files")
	}
	sort.Strings(tracked)
	return tracked, nil
}

func safeSnapshotPath(value string, directory bool) (string, error) {
	if directory {
		value = strings.TrimSuffix(value, "/")
	}
	if value == "" || value == "." || path.IsAbs(value) || path.Clean(value) != value ||
		value == ".." || strings.HasPrefix(value, "../") || strings.Contains(value, "\\") ||
		strings.ContainsRune(value, 0) {
		return "", errors.New("release source archive path is unsafe")
	}
	return value, nil
}

func snapshotFileMode(value int64) (os.FileMode, error) {
	if value&^0o777 != 0 {
		return 0, errors.New("special permission bits are forbidden")
	}
	if value&0o111 == 0 {
		return 0o444, nil
	}
	if value&0o111 == 0o111 {
		return 0o555, nil
	}
	return 0, errors.New("partial executable permissions are forbidden")
}

func verifySourceSnapshot(
	snapshotRoot string,
	treeFiles []snapshotTreeFile,
	tracked []string,
	oidLength int,
) error {
	if len(treeFiles) != len(tracked) {
		return errors.New("release source archive differs from the exact commit tree")
	}
	root, err := os.OpenRoot(snapshotRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	for index, expected := range treeFiles {
		if tracked[index] != expected.path {
			return errors.New("release source archive paths differ from the exact commit tree")
		}
		name := filepath.FromSlash(expected.path)
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Size() != expected.size ||
			(info.Mode().Perm()&0o111 != 0) != expected.executable {
			return fmt.Errorf("release source snapshot file differs from commit metadata: %s", expected.path)
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(io.LimitReader(file, maxSourceSnapshotFileBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || int64(len(content)) != info.Size() {
			return fmt.Errorf("read release source snapshot file: %s", expected.path)
		}
		observed, err := gitBlobOID(content, oidLength)
		if err != nil || observed != expected.oid {
			return fmt.Errorf("release source snapshot content differs from commit blob: %s", expected.path)
		}
	}
	return nil
}

func gitBlobOID(content []byte, oidLength int) (string, error) {
	var digest hash.Hash
	switch oidLength {
	case 40:
		digest = sha1.New() // #nosec G401 -- this verifies the repository's Git object format.
	case 64:
		digest = sha256.New()
	default:
		return "", errors.New("release source Git object format is unsupported")
	}
	_, _ = fmt.Fprintf(digest, "blob %d%c", len(content), 0)
	_, _ = digest.Write(content)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func lockSourceSnapshot(root string) error {
	directories := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("release source snapshot contains a symlink: %s", path)
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("release source snapshot contains a non-regular file: %s", path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index], 0o500); err != nil {
			return err
		}
	}
	return nil
}

func cleanupReleaseTemporary(root string) error {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	writableErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			return os.Chmod(path, 0o600)
		}
		return nil
	})
	removeErr := os.RemoveAll(root)
	return errors.Join(writableErr, removeErr)
}
