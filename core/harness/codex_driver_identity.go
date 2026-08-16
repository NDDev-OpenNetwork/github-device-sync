package harness

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const maximumCodexDriverIdentityFiles = 512

type codexDriverInputIdentity struct {
	RequestDigest           string                    `json:"request_digest"`
	DriverDigest            string                    `json:"driver_digest"`
	GDSExecutableDigest     string                    `json:"gds_executable_digest"`
	HarnessExecutableDigest string                    `json:"harness_executable_digest"`
	AdapterCandidateDigest  string                    `json:"adapter_candidate_digest"`
	Files                   []codexDriverIdentityFile `json:"files"`
}

type codexDriverIdentityFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

func codexDriverInputDigest(
	request RuntimeDriverRequest,
	requestRaw []byte,
	driverRaw []byte,
	fixture CodexRuntimeFixture,
) (string, error) {
	identity := codexDriverInputIdentity{
		RequestDigest: bytesDigest(requestRaw), DriverDigest: bytesDigest(driverRaw),
		AdapterCandidateDigest: fixture.CandidateDigest,
		Files:                  []codexDriverIdentityFile{},
	}
	for label, path := range map[string]string{
		"gds-executable":     request.GDSExecutable,
		"harness-executable": request.Environment.Executable,
	} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("resolve %s identity: %w", label, err)
		}
		raw, err := readBoundedRegular(resolved, 128<<20)
		if err != nil {
			return "", fmt.Errorf("read %s identity: %w", label, err)
		}
		if label == "gds-executable" {
			identity.GDSExecutableDigest = bytesDigest(raw)
		} else {
			identity.HarnessExecutableDigest = bytesDigest(raw)
		}
	}

	files := map[string]string{
		"profile":                          request.ProfilePath,
		"runtime-contract":                 request.RuntimeContract,
		"trigger-corpus":                   request.TriggerCorpus,
		"output-corpus":                    request.OutputCorpus,
		"enforcement-corpus":               request.EnforcementCorpus,
		"evidence-schema":                  request.EvidenceSchema,
		"AGENTS.md":                        filepath.Join(request.RepositoryRoot, "AGENTS.md"),
		".gds/repository.yaml":             filepath.Join(request.RepositoryRoot, ".gds", "repository.yaml"),
		".gds/bundle.lock.yaml":            filepath.Join(request.RepositoryRoot, ".gds", "bundle.lock.yaml"),
		".gds/compiled-policy.json":        filepath.Join(request.RepositoryRoot, ".gds", "compiled-policy.json"),
		"skills/registry.yaml":             filepath.Join(request.RepositoryRoot, "skills", "registry.yaml"),
		".agents/plugins/marketplace.json": filepath.Join(request.RepositoryRoot, ".agents", "plugins", "marketplace.json"),
	}
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw, err := readBoundedRegular(files[key], maximumRuntimeEvidenceBytes)
		if err != nil {
			return "", fmt.Errorf("read Codex driver input %s: %w", key, err)
		}
		identity.Files = append(identity.Files, codexDriverIdentityFile{Path: key, Digest: bytesDigest(raw)})
	}
	pluginFiles, err := codexDriverTreeIdentity(
		filepath.Join(request.RepositoryRoot, "plugins", "gds-core"), "plugins/gds-core",
	)
	if err != nil {
		return "", err
	}
	identity.Files = append(identity.Files, pluginFiles...)

	raw, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode Codex driver input identity: %w", err)
	}
	return bytesDigest(raw), nil
}

func codexDriverTreeIdentity(root, label string) ([]codexDriverIdentityFile, error) {
	files := []codexDriverIdentityFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Codex driver input tree contains a symlink: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("Codex driver input tree contains a non-regular file: %s", path)
		}
		if len(files) >= maximumCodexDriverIdentityFiles {
			return fmt.Errorf("Codex driver input tree exceeds %d files", maximumCodexDriverIdentityFiles)
		}
		raw, err := readBoundedRegular(path, maximumRuntimeEvidenceBytes)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, codexDriverIdentityFile{
			Path: filepath.ToSlash(filepath.Join(label, relative)), Digest: bytesDigest(raw),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("digest Codex driver input tree %s: %w", label, err)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}
