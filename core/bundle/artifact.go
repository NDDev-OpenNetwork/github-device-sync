package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type archivedFile struct {
	content []byte
	mode    string
}

func VerifyReleaseUnit(
	artifact []byte,
	envelope ReleaseEnvelope,
	schemas *validation.Set,
) (Manifest, []domain.Finding) {
	if len(artifact) == 0 || len(artifact) > maxBundleBytes {
		return Manifest{}, []domain.Finding{bundleFinding(
			"GDS_BUNDLE_ARTIFACT_SIZE_INVALID", fmt.Errorf("artifact size is outside bundle bounds"),
		)}
	}
	if observed := digest(artifact); observed != envelope.ArtifactDigest {
		return Manifest{}, []domain.Finding{bundleFinding(
			"GDS_BUNDLE_ARTIFACT_DIGEST_MISMATCH", fmt.Errorf("artifact digest does not match release envelope"),
		)}
	}
	envelopeRaw, _ := json.Marshal(envelope)
	if findings := validateDocument(schemas, "release-envelope", envelopeRaw); len(findings) != 0 {
		return Manifest{}, findings
	}
	files, err := readArchive(artifact)
	if err != nil {
		return Manifest{}, []domain.Finding{bundleFinding("GDS_BUNDLE_ARCHIVE_INVALID", err)}
	}
	manifestFile, found := files["manifest.json"]
	if !found || manifestFile.mode != "0644" || digest(manifestFile.content) != envelope.ManifestDigest {
		return Manifest{}, []domain.Finding{bundleFinding(
			"GDS_BUNDLE_MANIFEST_DIGEST_MISMATCH", fmt.Errorf("manifest is missing or has the wrong digest"),
		)}
	}
	if findings := validateDocument(schemas, "bundle-manifest", manifestFile.content); len(findings) != 0 {
		return Manifest{}, findings
	}
	var manifest Manifest
	if err := serialization.DecodeInto("manifest.json", manifestFile.content, &manifest); err != nil {
		return Manifest{}, []domain.Finding{bundleFinding("GDS_BUNDLE_MANIFEST_INVALID", err)}
	}
	if manifest.BundleVersion != envelope.BundleVersion ||
		manifest.ReleaseSequence != envelope.ReleaseSequence ||
		manifest.Channel != envelope.Channel || manifest.SourceCommit != envelope.SourceCommit ||
		manifest.SourceRef != envelope.SourceRef {
		return Manifest{}, []domain.Finding{bundleFinding(
			"GDS_BUNDLE_ENVELOPE_BINDING_MISMATCH",
			fmt.Errorf("manifest version, sequence, or source commit differs from release envelope"),
		)}
	}
	executableFiles := 0
	for _, record := range manifest.Files {
		if record.Mode == "0755" {
			executableFiles++
		}
	}
	if executableFiles != envelope.ExecutableFiles {
		return Manifest{}, []domain.Finding{bundleFinding(
			"GDS_BUNDLE_EXECUTABLE_COUNT_MISMATCH",
			fmt.Errorf("manifest executable count differs from release envelope"),
		)}
	}
	if manifest.ContentSetDigest != digestJSON(manifest.Files) ||
		manifest.PolicyDigest != subsetDigest(manifest.Files, "policies/") ||
		manifest.SkillSetDigest != subsetDigest(manifest.Files, "skills/") ||
		manifest.HarnessProfilesDigest != subsetDigest(manifest.Files, "harnesses/") {
		return Manifest{}, []domain.Finding{bundleFinding(
			"GDS_BUNDLE_CONTENT_SET_DIGEST_MISMATCH", fmt.Errorf("manifest aggregate digest is invalid"),
		)}
	}
	expectedChecksums := bytes.Buffer{}
	previousPath := ""
	declared := map[string]struct{}{}
	for _, record := range manifest.Files {
		if previousPath != "" && record.Path <= previousPath {
			return Manifest{}, []domain.Finding{bundleFinding(
				"GDS_BUNDLE_FILE_ORDER_INVALID", fmt.Errorf("manifest file paths are not unique and sorted"),
			)}
		}
		previousPath = record.Path
		declared[record.Path] = struct{}{}
		file, found := files[record.Path]
		if !found || len(file.content) != record.Size || file.mode != record.Mode ||
			digest(file.content) != record.Digest {
			return Manifest{}, []domain.Finding{bundleFinding(
				"GDS_BUNDLE_FILE_DIGEST_MISMATCH", fmt.Errorf("bundle member %s does not match manifest", record.Path),
			)}
		}
		fmt.Fprintf(
			&expectedChecksums, "%s  %s\n",
			strings.TrimPrefix(record.Digest, "sha256:"), record.Path,
		)
	}
	checksums, found := files["checksums.txt"]
	if !found || checksums.mode != "0644" || !bytes.Equal(checksums.content, expectedChecksums.Bytes()) {
		return Manifest{}, []domain.Finding{bundleFinding(
			"GDS_BUNDLE_CHECKSUMS_MISMATCH", fmt.Errorf("checksums.txt does not match manifest file records"),
		)}
	}
	for name := range files {
		if name == "manifest.json" || name == "checksums.txt" {
			continue
		}
		if _, found := declared[name]; !found {
			return Manifest{}, []domain.Finding{bundleFinding(
				"GDS_BUNDLE_UNDECLARED_FILE", fmt.Errorf("bundle contains undeclared file %s", name),
			)}
		}
	}
	return manifest, nil
}

func readArchive(artifact []byte) (map[string]archivedFile, error) {
	compressed := bytes.NewReader(artifact)
	gzipReader, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, err
	}
	gzipReader.Multistream(false)
	tarReader := tar.NewReader(gzipReader)
	files := map[string]archivedFile{}
	total := 0
	count := 0
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("bundle member %s is not a regular file", header.Name)
		}
		if header.Name == "" || path.IsAbs(header.Name) || path.Clean(header.Name) != header.Name ||
			header.Name == ".." || strings.HasPrefix(header.Name, "../") ||
			strings.Contains(header.Name, "\\") {
			return nil, fmt.Errorf("bundle member path is unsafe")
		}
		if _, duplicate := files[header.Name]; duplicate {
			return nil, fmt.Errorf("bundle member %s is duplicated", header.Name)
		}
		count++
		if count > maxBundleFiles+2 {
			return nil, fmt.Errorf("bundle archive file count exceeds limit")
		}
		if header.Size < 0 || header.Size > maxBundleFileBytes {
			return nil, fmt.Errorf("bundle member %s exceeds size bound", header.Name)
		}
		content, err := io.ReadAll(io.LimitReader(tarReader, maxBundleFileBytes+1))
		if err != nil || int64(len(content)) != header.Size {
			return nil, fmt.Errorf("bundle member %s content is incomplete", header.Name)
		}
		total += len(content)
		if total > maxBundleBytes {
			return nil, fmt.Errorf("bundle uncompressed content exceeds aggregate bound")
		}
		mode := "0644"
		if header.Mode == 0o755 {
			mode = "0755"
		} else if header.Mode != 0o644 {
			return nil, fmt.Errorf("bundle member %s has unsupported mode", header.Name)
		}
		files[header.Name] = archivedFile{content: content, mode: mode}
	}
	if err := gzipReader.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() != 0 {
		return nil, fmt.Errorf("bundle archive has trailing compressed data")
	}
	return files, nil
}
