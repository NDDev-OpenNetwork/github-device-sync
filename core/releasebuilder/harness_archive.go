package releasebuilder

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	maxHarnessEvidenceArchiveSize = 8 << 20
	maxHarnessEvidenceMemberSize  = 2 << 20
	maxHarnessEvidenceTotalSize   = 8 << 20
)

var harnessEvidenceMembers = []string{
	"antigravity.json", "claude-code.json", "codex.json",
	"cursor.json", "grok-build.json", "manifest.json",
	"opencode.json", "pi.json",
}

// MaterializeHarnessEvidenceArchive validates every header before extracting
// from the same bounded in-memory snapshot into a new private directory. The
// destination becomes visible only after the second complete validation pass.
func MaterializeHarnessEvidenceArchive(archivePath, destination string) (returnErr error) {
	return materializeHarnessEvidenceArchive(archivePath, destination, harnessArchiveHooks{})
}

type harnessArchiveHooks struct {
	afterParentInspect func()
	beforePublish      func()
}

type harnessTransactionBoundary struct {
	path        string
	destination string
	identity    os.FileInfo
	root        *os.Root
}

func materializeHarnessEvidenceArchive(
	archivePath, destination string,
	hooks harnessArchiveHooks,
) (returnErr error) {
	pathInfo, err := os.Lstat(archivePath)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		pathInfo.Size() < 1 || pathInfo.Size() > maxHarnessEvidenceArchiveSize {
		return errors.New("harness evidence archive is not a bounded regular file")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return errors.New("harness evidence archive is unavailable")
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) ||
		info.Size() < 1 || info.Size() > maxHarnessEvidenceArchiveSize {
		return errors.New("harness evidence archive is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(archive, maxHarnessEvidenceArchiveSize+1))
	if err != nil || int64(len(raw)) != info.Size() || len(raw) > maxHarnessEvidenceArchiveSize {
		return errors.New("harness evidence archive changed or exceeded its bound while reading")
	}
	if err := inspectHarnessEvidenceArchive(raw, nil); err != nil {
		return err
	}

	boundary, err := openHarnessTransactionBoundary(destination, hooks.afterParentInspect)
	if err != nil {
		return err
	}
	defer boundary.root.Close()
	stage, err := makeHarnessEvidenceStage(boundary.root)
	if err != nil {
		return err
	}
	defer func() {
		if stage != "" {
			returnErr = errors.Join(returnErr, boundary.root.RemoveAll(stage))
		}
	}()
	stageInfo, err := boundary.root.Lstat(stage)
	if err != nil || !stageInfo.IsDir() || stageInfo.Mode().Perm() != 0o700 {
		return errors.New("harness evidence stage identity is invalid")
	}
	stageRoot, err := boundary.root.OpenRoot(stage)
	if err != nil {
		return fmt.Errorf("open harness evidence stage: %w", err)
	}
	defer stageRoot.Close()
	openedStage, err := stageRoot.Stat(".")
	if err != nil || !os.SameFile(stageInfo, openedStage) {
		return errors.New("harness evidence stage changed during secure open")
	}
	if err := inspectHarnessEvidenceArchive(raw, stageRoot); err != nil {
		return err
	}
	if hooks.beforePublish != nil {
		hooks.beforePublish()
	}
	if err := boundary.revalidate(); err != nil {
		return err
	}
	if _, err := boundary.root.Lstat(boundary.destination); err == nil || !os.IsNotExist(err) {
		return errors.New("harness evidence destination appeared before publication")
	}
	if err := boundary.root.Rename(stage, boundary.destination); err != nil {
		return fmt.Errorf("publish staged harness evidence: %w", err)
	}
	published, err := boundary.root.Lstat(boundary.destination)
	if err != nil || !os.SameFile(stageInfo, published) {
		return errors.New("published harness evidence identity differs from the staged directory")
	}
	stage = ""
	return nil
}

func openHarnessTransactionBoundary(destination string, afterInspect func()) (*harnessTransactionBoundary, error) {
	destination, err := filepath.Abs(destination)
	if err != nil || filepath.Clean(destination) != destination {
		return nil, errors.New("harness evidence destination is invalid")
	}
	parentPath := filepath.Dir(destination)
	destinationName := filepath.Base(destination)
	if destinationName == "." || destinationName == ".." || strings.ContainsAny(destinationName, `/\\`) {
		return nil, errors.New("harness evidence destination name is invalid")
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !privateOwnedDirectory(parentInfo) {
		return nil, errors.New("harness evidence destination parent is not an owned non-writable real directory")
	}
	if afterInspect != nil {
		afterInspect()
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, fmt.Errorf("open harness evidence transaction root: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(parentInfo, opened) {
		root.Close()
		return nil, errors.New("harness evidence transaction root changed during secure open")
	}
	boundary := &harnessTransactionBoundary{
		path: parentPath, destination: destinationName, identity: parentInfo, root: root,
	}
	if err := boundary.revalidate(); err != nil {
		root.Close()
		return nil, err
	}
	if _, err := root.Lstat(destinationName); err == nil || !os.IsNotExist(err) {
		root.Close()
		return nil, errors.New("harness evidence destination must not exist")
	}
	return boundary, nil
}

func privateOwnedDirectory(info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func (boundary *harnessTransactionBoundary) revalidate() error {
	opened, openedErr := boundary.root.Stat(".")
	current, currentErr := os.Lstat(boundary.path)
	if openedErr != nil || currentErr != nil || !privateOwnedDirectory(current) ||
		!os.SameFile(boundary.identity, opened) || !os.SameFile(boundary.identity, current) {
		return errors.New("harness evidence transaction root identity changed")
	}
	return nil
}

func makeHarnessEvidenceStage(root *os.Root) (string, error) {
	for attempt := 0; attempt < 128; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := ".gds-harness-evidence-" + hex.EncodeToString(random[:])
		if err := root.Mkdir(name, 0o700); err == nil {
			return name, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", errors.New("cannot allocate a unique harness evidence stage")
}

func inspectHarnessEvidenceArchive(raw []byte, stage *os.Root) error {
	buffered := bufio.NewReader(bytes.NewReader(raw))
	compressed, err := gzip.NewReader(buffered)
	if err != nil {
		return fmt.Errorf("open harness evidence gzip stream: %w", err)
	}
	compressed.Multistream(false)
	reader := tar.NewReader(compressed)
	seen := make(map[string]struct{}, len(harnessEvidenceMembers))
	total := int64(0)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read harness evidence tar header: %w", err)
		}
		name := header.Name
		if name == "" || strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name ||
			strings.HasPrefix(name, "../") || !slices.Contains(harnessEvidenceMembers, name) {
			return fmt.Errorf("harness evidence archive contains unexpected path %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("harness evidence archive repeats path %q", name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("harness evidence archive member %q is not a regular file", name)
		}
		if header.Linkname != "" {
			return fmt.Errorf("harness evidence archive member %q declares a link target", name)
		}
		if header.Size < 2 || header.Size > maxHarnessEvidenceMemberSize || total > maxHarnessEvidenceTotalSize-header.Size {
			return fmt.Errorf("harness evidence archive member %q exceeds size limits", name)
		}
		total += header.Size
		if stage == nil {
			if _, err := io.CopyN(io.Discard, reader, header.Size); err != nil {
				return fmt.Errorf("validate harness evidence member %q: %w", name, err)
			}
		} else {
			output, err := stage.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
			if err != nil {
				return fmt.Errorf("create staged harness evidence member %q: %w", name, err)
			}
			written, copyErr := io.CopyN(output, reader, header.Size)
			closeErr := output.Close()
			if copyErr != nil || written != header.Size || closeErr != nil {
				return fmt.Errorf("materialize harness evidence member %q: %w", name, errors.Join(copyErr, closeErr))
			}
		}
		seen[name] = struct{}{}
	}
	if len(seen) != len(harnessEvidenceMembers) {
		return fmt.Errorf("harness evidence archive contains %d exact members, want %d", len(seen), len(harnessEvidenceMembers))
	}
	if _, err := io.Copy(io.Discard, compressed); err != nil {
		return fmt.Errorf("finish harness evidence gzip stream: %w", err)
	}
	if err := compressed.Close(); err != nil {
		return fmt.Errorf("verify harness evidence gzip stream: %w", err)
	}
	if _, err := buffered.ReadByte(); !errors.Is(err, io.EOF) {
		return errors.New("harness evidence archive contains trailing compressed data")
	}
	return nil
}
