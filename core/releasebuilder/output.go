package releasebuilder

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitauthority"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const (
	releaseEnvelopeName       = "release-envelope.json"
	releaseManifestName       = "manifest.json"
	releaseSBOMName           = "sbom.spdx.json"
	releaseTrustName          = "bundle-trust.yaml"
	releaseChecksumsName      = "SHA256SUMS"
	maxReleaseOutputFileBytes = 512 << 20
)

func releaseOutputFiles(
	request Request,
	source Source,
	goVersion string,
	gitIdentity gitauthority.Identity,
	candidate bundle.Candidate,
	sbom []byte,
	trust []byte,
) (Result, map[string][]byte, error) {
	artifactName := "gds-bundle-v" + request.Version + ".tar.gz"
	envelope, err := indentedJSON(candidate.Envelope)
	if err != nil {
		return Result{}, nil, err
	}
	files := map[string][]byte{
		artifactName:        candidate.Artifact,
		releaseEnvelopeName: envelope,
		releaseManifestName: candidate.ManifestBytes,
		releaseSBOMName:     sbom,
		releaseTrustName:    trust,
	}
	checksums, err := renderChecksums(files)
	if err != nil {
		return Result{}, nil, err
	}
	files[releaseChecksumsName] = checksums
	return Result{
		SchemaVersion: domain.SchemaVersion, Version: request.Version,
		ReleaseSequence: request.ReleaseSequence, Channel: request.Channel,
		Source: source, GoVersion: goVersion,
		GitVersion: gitIdentity.Version, GitDigest: gitIdentity.Digest, ArtifactName: artifactName,
		ArtifactDigest: candidate.Envelope.ArtifactDigest,
		ManifestDigest: candidate.Envelope.ManifestDigest,
		SBOMDigest:     digestBytes(sbom), Reproducible: true,
	}, files, nil
}

func writeOutput(destination string, files map[string][]byte) error {
	return writeOutputWithPostRenameHook(destination, files, nil)
}

// writeOutputWithPostRenameHook stages the release files, atomically renames the
// staging directory onto destination, then durably syncs the parent. Once the
// rename commits, any later failure (parent open/sync, or an injected
// postRename hook) rolls the publication back through an identity-checked
// removal so a failed build never leaves an ambiguous destination that blocks a
// retry, and never deletes a directory whose identity no longer matches the one
// this call published. postRename is nil in production and only set by tests to
// inject a controlled post-commit failure.
func writeOutputWithPostRenameHook(
	destination string,
	files map[string][]byte,
	postRename func() error,
) (returnErr error) {
	if err := validateOutputMap(files); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	base := filepath.Base(destination)
	temporary, err := os.MkdirTemp(parent, "."+base+".tmp-")
	if err != nil {
		return err
	}
	staging, err := os.Lstat(temporary)
	if err != nil || !staging.IsDir() || staging.Mode()&os.ModeSymlink != 0 {
		_ = os.RemoveAll(temporary)
		return errors.New("release staging directory is invalid")
	}
	published := false
	defer func() {
		if returnErr == nil {
			return
		}
		if !published {
			_ = os.RemoveAll(temporary)
			return
		}
		if err := removeReleaseOutputIfSame(destination, staging); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("roll back unpublished release output: %w", err))
		}
	}()
	root, err := os.OpenRoot(temporary)
	if err != nil {
		return err
	}
	paths := sortedKeys(files)
	for _, name := range paths {
		output, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			_ = root.Close()
			return err
		}
		if _, err := output.Write(files[name]); err != nil {
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
	stagingDirectory, err := os.Open(temporary)
	if err != nil {
		return err
	}
	if err := stagingDirectory.Sync(); err != nil {
		_ = stagingDirectory.Close()
		return err
	}
	if err := stagingDirectory.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return err
	}
	published = true
	if postRename != nil {
		if err := postRename(); err != nil {
			return err
		}
	}
	parentDirectory, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer parentDirectory.Close()
	return parentDirectory.Sync()
}

// removeReleaseOutputIfSame removes a just-published release directory only when
// its on-disk identity still matches the staging directory this process
// renamed into place. A mismatch (a concurrently replaced destination) is
// reported instead of deleted, so rollback can never destroy a foreign
// directory. It mirrors the identity-checked rollback used by state.Backup.
func removeReleaseOutputIfSame(destination string, expected os.FileInfo) error {
	observed, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if expected == nil || observed.Mode()&os.ModeSymlink != 0 || !observed.IsDir() ||
		!os.SameFile(expected, observed) {
		return errors.New("release output identity changed")
	}
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	parentDirectory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	syncErr := parentDirectory.Sync()
	closeErr := parentDirectory.Close()
	return errors.Join(syncErr, closeErr)
}

func VerifyDirectory(directory string, schemas *validation.Set) (DirectoryVerification, error) {
	if schemas == nil {
		return DirectoryVerification{}, errors.New("release schema set is unavailable")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return DirectoryVerification{}, errors.New("release output is not a real directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return DirectoryVerification{}, err
	}
	if len(entries) != 6 {
		return DirectoryVerification{}, fmt.Errorf("release output contains %d files; expected 6", len(entries))
	}
	contents := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		entryInfo, err := os.Lstat(path)
		if err != nil || !entryInfo.Mode().IsRegular() || entryInfo.Mode()&os.ModeSymlink != 0 ||
			entryInfo.Size() < 0 || entryInfo.Size() > maxReleaseOutputFileBytes {
			return DirectoryVerification{}, fmt.Errorf("release output member is invalid: %s", entry.Name())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return DirectoryVerification{}, err
		}
		contents[entry.Name()] = raw
	}
	for _, required := range []string{
		releaseChecksumsName, releaseEnvelopeName, releaseManifestName, releaseSBOMName, releaseTrustName,
	} {
		if _, found := contents[required]; !found {
			return DirectoryVerification{}, fmt.Errorf("release output is missing %s", required)
		}
	}
	checksummed := make(map[string][]byte, len(contents)-1)
	for name, raw := range contents {
		if name != releaseChecksumsName {
			checksummed[name] = raw
		}
	}
	expectedChecksums, err := renderChecksums(checksummed)
	if err != nil || !bytes.Equal(expectedChecksums, contents[releaseChecksumsName]) {
		return DirectoryVerification{}, errors.New("release output checksums do not match the exact file set")
	}
	var envelope bundle.ReleaseEnvelope
	if err := decodeAndValidate(schemas, "release-envelope", releaseEnvelopeName, contents[releaseEnvelopeName], &envelope); err != nil {
		return DirectoryVerification{}, err
	}
	artifactName := "gds-bundle-v" + envelope.BundleVersion + ".tar.gz"
	artifact, found := contents[artifactName]
	if !found {
		return DirectoryVerification{}, fmt.Errorf("release output is missing exact artifact %s", artifactName)
	}
	manifest, findings := bundle.VerifyReleaseUnit(artifact, envelope, schemas)
	if len(findings) != 0 {
		return DirectoryVerification{}, findingError("verify materialized release bundle", findings)
	}
	externalManifest, err := indentedJSON(manifest)
	if err != nil || !bytes.Equal(externalManifest, contents[releaseManifestName]) ||
		digestBytes(contents[releaseManifestName]) != envelope.ManifestDigest {
		return DirectoryVerification{}, errors.New("detached manifest does not match the verified bundle manifest")
	}
	if err := validateSBOM(contents[releaseSBOMName]); err != nil {
		return DirectoryVerification{}, fmt.Errorf("detached SBOM is invalid: %w", err)
	}
	var trust bundle.TrustPolicy
	if err := decodeAndValidate(schemas, "bundle-trust", releaseTrustName, contents[releaseTrustName], &trust); err != nil {
		return DirectoryVerification{}, err
	}
	if !manifestRecordMatches(manifest, "sbom/gds.spdx.json", contents[releaseSBOMName], "0644") ||
		!manifestRecordMatches(manifest, "trust/bundle-trust.yaml", contents[releaseTrustName], "0644") {
		return DirectoryVerification{}, errors.New("detached SBOM or trust policy differs from its bundle member")
	}
	files := make([]OutputFile, 0, len(contents))
	for _, name := range sortedKeys(contents) {
		files = append(files, OutputFile{Path: name, Digest: digestBytes(contents[name]), Size: len(contents[name])})
	}
	return DirectoryVerification{
		SchemaVersion: domain.SchemaVersion, Status: "verified", Version: envelope.BundleVersion,
		ReleaseSequence: envelope.ReleaseSequence, Channel: envelope.Channel,
		SourceCommit: envelope.SourceCommit, SourceRef: envelope.SourceRef,
		ArtifactName:   artifactName,
		ArtifactDigest: envelope.ArtifactDigest, ManifestDigest: envelope.ManifestDigest,
		SBOMDigest: digestBytes(contents[releaseSBOMName]), Reproducible: true, Files: files,
	}, nil
}

func validateOutputMap(files map[string][]byte) error {
	if len(files) != 6 {
		return errors.New("release output must contain exactly six files")
	}
	for name, raw := range files {
		if name == "" || filepath.Base(name) != name || name == "." || name == ".." ||
			strings.ContainsAny(name, `/\\`) || len(raw) > maxReleaseOutputFileBytes {
			return fmt.Errorf("release output file is invalid: %s", name)
		}
	}
	return nil
}

func renderChecksums(files map[string][]byte) ([]byte, error) {
	if len(files) == 0 {
		return nil, errors.New("release checksum input is empty")
	}
	var output bytes.Buffer
	for _, name := range sortedKeys(files) {
		if name == releaseChecksumsName || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\n\r") {
			return nil, fmt.Errorf("release checksum name is invalid: %s", name)
		}
		fmt.Fprintf(&output, "%s  %s\n", strings.TrimPrefix(digestBytes(files[name]), "sha256:"), name)
	}
	return output.Bytes(), nil
}

func decodeAndValidate(schemas *validation.Set, schemaName, source string, raw []byte, target any) error {
	value, err := serialization.Decode(source, raw)
	if err != nil {
		return err
	}
	if findings := schemas.Validate(schemaName, value, source); len(findings) != 0 {
		return findingError("validate "+source, findings)
	}
	return serialization.DecodeInto(source, raw, target)
}

func manifestRecordMatches(manifest bundle.Manifest, path string, raw []byte, mode string) bool {
	for _, record := range manifest.Files {
		if record.Path == path {
			return record.Mode == mode && record.Size == len(raw) && record.Digest == digestBytes(raw)
		}
	}
	return false
}

func indentedJSON(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func digestBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
