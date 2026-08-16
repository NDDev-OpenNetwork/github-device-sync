package releaseconsumer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	maximumReleaseSnapshotFile = 512 << 20
	maximumVerifierExecutable  = 256 << 20
)

func snapshotVerificationInputs(request Request) (Request, string, error) {
	root, err := os.MkdirTemp("", "gds-release-verification-")
	if err != nil {
		return Request{}, "", err
	}
	fail := func(cause error) (Request, string, error) {
		_ = os.RemoveAll(root)
		return Request{}, "", cause
	}
	destination, err := os.OpenRoot(root)
	if err != nil {
		return fail(err)
	}
	defer destination.Close()
	if err := destination.Mkdir("release", 0o700); err != nil {
		return fail(err)
	}
	if err := destination.Mkdir("evidence", 0o700); err != nil {
		return fail(err)
	}
	if err := snapshotFlatDirectory(
		request.ReleaseDirectory, destination, "release", 6, maximumReleaseSnapshotFile,
	); err != nil {
		return fail(fmt.Errorf("snapshot release directory: %w", err))
	}
	// The evidence directory is contracted as exactly these three files. Reading
	// only the three names would accept a directory that also holds something
	// else — a diagnostic dump, a second trusted root — and the runbook tells the
	// operator that staging such a file into a verification input breaks the
	// contract. An input described as invalid must not verify, so the count is
	// checked before any of the three is read.
	if err := requireExactEntries(request.EvidenceDirectory, len(evidenceFileNames)); err != nil {
		return fail(fmt.Errorf("snapshot offline evidence: %w", err))
	}
	for _, name := range evidenceFileNames {
		if err := snapshotPath(
			filepath.Join(request.EvidenceDirectory, name), destination,
			filepath.Join("evidence", name), maximumEvidenceFile,
		); err != nil {
			return fail(fmt.Errorf("snapshot offline evidence: %w", err))
		}
	}
	if err := snapshotPath(request.TrustPolicyPath, destination, "consumer-trust.yaml", 1<<20); err != nil {
		return fail(fmt.Errorf("snapshot consumer trust policy: %w", err))
	}
	request.ReleaseDirectory = filepath.Join(root, "release")
	request.EvidenceDirectory = filepath.Join(root, "evidence")
	request.TrustPolicyPath = filepath.Join(root, "consumer-trust.yaml")
	return request, root, nil
}

// evidenceFileNames is the offline evidence contract: exactly these, in this
// order, and nothing else.
var evidenceFileNames = []string{ProvenanceBundleName, SBOMBundleName, TrustedRootName}

// requireExactEntries rejects a consumer input directory that holds anything
// beyond the contracted entry count.
func requireExactEntries(source string, expected int) error {
	root, identity, err := openStableRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) != expected {
		return fmt.Errorf("input contains %d entries; expected %d", len(entries), expected)
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(identity, after) {
		return errors.New("input directory identity changed during snapshot")
	}
	return nil
}

func snapshotFlatDirectory(
	source string,
	destination *os.Root,
	destinationDirectory string,
	expectedEntries int,
	maximumFileBytes int64,
) error {
	root, identity, err := openStableRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) != expectedEntries {
		return fmt.Errorf("input contains %d entries; expected %d", len(entries), expectedEntries)
	}
	for _, entry := range entries {
		if entry.Name() == "." || entry.Name() == ".." || filepath.Base(entry.Name()) != entry.Name() {
			return errors.New("input contains an unsafe entry name")
		}
		if err := snapshotRootFile(
			root, entry.Name(), destination,
			filepath.Join(destinationDirectory, entry.Name()), maximumFileBytes,
		); err != nil {
			return err
		}
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(identity, after) {
		return errors.New("input directory identity changed during snapshot")
	}
	return nil
}

func snapshotPath(source string, destination *os.Root, destinationName string, maximum int64) error {
	parent, identity, err := openStableRoot(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := snapshotRootFile(parent, filepath.Base(source), destination, destinationName, maximum); err != nil {
		return err
	}
	after, err := parent.Stat(".")
	if err != nil || !os.SameFile(identity, after) {
		return errors.New("input parent identity changed during snapshot")
	}
	return nil
}

func snapshotRootFile(
	source *os.Root,
	sourceName string,
	destination *os.Root,
	destinationName string,
	maximum int64,
) error {
	before, err := source.Lstat(sourceName)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		before.Size() < 1 || before.Size() > maximum {
		return fmt.Errorf("input is not a bounded regular file: %s", sourceName)
	}
	input, err := source.Open(sourceName)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() ||
		opened.Size() != before.Size() {
		return fmt.Errorf("input identity changed during open: %s", sourceName)
	}
	output, err := destination.OpenFile(destinationName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maximum+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != opened.Size() || written > maximum {
		return fmt.Errorf("input size changed during snapshot: %s", sourceName)
	}
	after, err := input.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() ||
		!opened.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("input changed during snapshot: %s", sourceName)
	}
	return nil
}

func readStableRegularFile(path string, maximum int64, executable bool) ([]byte, error) {
	parent, identity, err := openStableRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := filepath.Base(path)
	before, err := parent.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		before.Size() < 1 || before.Size() > maximum || executable && before.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("input is not an approved bounded regular file")
	}
	file, err := parent.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("input identity changed during open")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) != opened.Size() || int64(len(raw)) > maximum {
		return nil, errors.New("input changed while it was read")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() ||
		!opened.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("input changed while it was read")
	}
	parentAfter, err := parent.Stat(".")
	if err != nil || !os.SameFile(identity, parentAfter) {
		return nil, errors.New("input parent identity changed while it was read")
	}
	return raw, nil
}

func openStableRoot(path string) (*os.Root, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, nil, errors.New("input parent is not a real directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		return nil, nil, errors.New("input parent identity changed during open")
	}
	return root, opened, nil
}
