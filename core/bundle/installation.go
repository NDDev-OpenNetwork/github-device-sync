package bundle

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type InstallationCandidate struct {
	Manifest Manifest        `json:"manifest"`
	Envelope ReleaseEnvelope `json:"envelope"`
	Files    []FileRecord    `json:"files"`
	contents map[string]archivedFile
}

func PrepareInstallation(
	artifact []byte,
	envelope ReleaseEnvelope,
	schemas *validation.Set,
) (InstallationCandidate, []domain.Finding) {
	manifest, findings := VerifyReleaseUnit(artifact, envelope, schemas)
	if len(findings) != 0 {
		return InstallationCandidate{}, findings
	}
	contents, err := readArchive(artifact)
	if err != nil {
		return InstallationCandidate{}, []domain.Finding{bundleFinding("GDS_BUNDLE_ARCHIVE_INVALID", err)}
	}
	paths := make([]string, 0, len(contents))
	for path := range contents {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	files := make([]FileRecord, 0, len(paths))
	for _, path := range paths {
		file := contents[path]
		files = append(files, FileRecord{
			Path: path, Digest: digest(file.content), Size: len(file.content), Mode: file.mode,
		})
	}
	return InstallationCandidate{
		Manifest: manifest, Envelope: envelope, Files: files, contents: contents,
	}, nil
}

func (candidate InstallationCandidate) WriteNew(destination string) (returnErr error) {
	if len(candidate.contents) == 0 || len(candidate.Files) == 0 {
		return fmt.Errorf("bundle installation candidate is empty")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(absolute); err == nil || !os.IsNotExist(err) {
		return fmt.Errorf("bundle installation destination must not exist")
	}
	parent := filepath.Dir(absolute)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("bundle installation parent is not a real directory")
	}
	temporary, err := os.MkdirTemp(parent, ".gds-bundle.tmp-")
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(temporary)
		}
	}()
	root, err := os.OpenRoot(temporary)
	if err != nil {
		return err
	}
	for _, record := range candidate.Files {
		file := candidate.contents[record.Path]
		local := filepath.FromSlash(record.Path)
		if err := root.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			_ = root.Close()
			return err
		}
		output, err := root.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = root.Close()
			return err
		}
		if _, err := output.Write(file.content); err != nil {
			_ = output.Close()
			_ = root.Close()
			return err
		}
		mode := os.FileMode(0o644)
		if record.Mode == "0755" {
			mode = 0o755
		}
		if err := output.Chmod(mode); err != nil {
			_ = output.Close()
			_ = root.Close()
			return err
		}
		if err := output.Sync(); err != nil {
			_ = output.Close()
			_ = root.Close()
			return err
		}
		if err := output.Close(); err != nil {
			_ = root.Close()
			return err
		}
	}
	if err := root.Close(); err != nil {
		return err
	}
	if err := syncDirectory(temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, absolute); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func (candidate InstallationCandidate) Verify(directory string) error {
	if len(candidate.Files) == 0 {
		return fmt.Errorf("bundle installation candidate is empty")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("bundle installation root is invalid")
	}
	expected := make(map[string]FileRecord, len(candidate.Files))
	allowedDirectories := map[string]struct{}{".": {}}
	for _, record := range candidate.Files {
		expected[record.Path] = record
		for directory := filepath.Dir(filepath.FromSlash(record.Path)); directory != "."; directory = filepath.Dir(directory) {
			allowedDirectories[filepath.ToSlash(directory)] = struct{}{}
		}
	}
	observed := map[string]struct{}{}
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absolute {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle installation contains a symlink")
		}
		if entry.IsDir() {
			relative, err := filepath.Rel(absolute, path)
			if err != nil {
				return err
			}
			if _, allowed := allowedDirectories[filepath.ToSlash(relative)]; !allowed {
				return fmt.Errorf("bundle installation contains undeclared directory")
			}
			return nil
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		record, found := expected[relative]
		if !found {
			return fmt.Errorf("bundle installation contains undeclared file %s", relative)
		}
		fileInfo, err := entry.Info()
		if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Size() != int64(record.Size) {
			return fmt.Errorf("bundle installation member is invalid: %s", relative)
		}
		expectedMode := os.FileMode(0o644)
		if record.Mode == "0755" {
			expectedMode = 0o755
		}
		if fileInfo.Mode().Perm() != expectedMode {
			return fmt.Errorf("bundle installation member mode differs: %s", relative)
		}
		raw, err := os.ReadFile(path)
		if err != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != record.Digest {
			return fmt.Errorf("bundle installation member digest differs: %s", relative)
		}
		observed[relative] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(observed) != len(expected) {
		missing := []string{}
		for path := range expected {
			if _, found := observed[path]; !found {
				missing = append(missing, path)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("bundle installation is missing %s", strings.Join(missing, ", "))
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
