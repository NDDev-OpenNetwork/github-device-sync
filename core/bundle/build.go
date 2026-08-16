package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/security"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const (
	maxBundleFileBytes = 128 << 20
	maxBundleBytes     = 512 << 20
	maxBundleFiles     = 10000
)

type sourceFile struct {
	record  FileRecord
	content []byte
}

var portableRoots = []string{
	"schemas/v1",
	"schemas/migrations",
	"policies",
	"skills/canonical",
	"harnesses",
	"templates/agents",
	"templates/harnesses",
	"plugins/gds-core",
	"plugins/gds-estate-admin",
	"plugins/gds-module",
}

var portableFiles = []string{"skills/registry.yaml"}

func LoadTrust(root string, schemas *validation.Set) (TrustPolicy, error) {
	return LoadTrustFile(filepath.Join(root, "requirements", "bundle-trust.yaml"), schemas)
}

func LoadTrustFile(path string, schemas *validation.Set) (TrustPolicy, error) {
	if schemas == nil {
		return TrustPolicy{}, fmt.Errorf("bundle trust schema set is unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > 1<<20 {
		return TrustPolicy{}, fmt.Errorf("bundle trust policy is not a bounded regular file")
	}
	if findings := schemas.ValidateFile("bundle-trust", path); len(findings) != 0 {
		return TrustPolicy{}, fmt.Errorf("bundle trust policy has %d validation findings", len(findings))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return TrustPolicy{}, err
	}
	var trust TrustPolicy
	if err := serialization.DecodeInto(path, raw, &trust); err != nil {
		return TrustPolicy{}, err
	}
	return trust, nil
}

func Build(
	root string,
	options BuildOptions,
	trust TrustPolicy,
	schemas *validation.Set,
) (Candidate, []domain.Finding) {
	if finding := validateBuildOptions(options, trust); finding != nil {
		return Candidate{}, []domain.Finding{*finding}
	}
	files, err := collectPortableFiles(root, options.TrackedSources)
	if err != nil {
		return Candidate{}, []domain.Finding{bundleFinding("GDS_BUNDLE_SOURCE_INVALID", err)}
	}
	files, err = appendAdditionalFiles(files, options.AdditionalFiles)
	if err != nil {
		return Candidate{}, []domain.Finding{bundleFinding("GDS_BUNDLE_ADDITIONAL_FILE_INVALID", err)}
	}
	records := make([]FileRecord, 0, len(files))
	executableFiles := 0
	for _, file := range files {
		records = append(records, file.record)
		if file.record.Mode == "0755" {
			executableFiles++
		}
	}
	manifest := Manifest{
		SchemaVersion: domain.SchemaVersion, BundleVersion: options.BundleVersion,
		ReleaseSequence: options.ReleaseSequence, Channel: options.Channel,
		SourceCommit: options.SourceCommit, SourceRef: options.SourceRef,
		MinimumCLIVersion:             options.MinimumCLIVersion,
		ContentSetDigest:              digestJSON(records),
		PolicyDigest:                  subsetDigest(records, "policies/"),
		SkillSetDigest:                subsetDigest(records, "skills/"),
		HarnessProfilesDigest:         subsetDigest(records, "harnesses/"),
		HarnessEvidenceManifestDigest: options.HarnessEvidenceManifestDigest,
		HarnessEvidenceProvisional:    options.HarnessEvidenceProvisional,
		Files:                         records,
		SupplyChain:                   SupplyChain{AttestationRequired: true, SBOMRequiredForExecutables: true},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Candidate{}, []domain.Finding{bundleFinding("GDS_BUNDLE_MANIFEST_ENCODE_FAILED", err)}
	}
	manifestBytes = append(manifestBytes, '\n')
	if findings := validateDocument(schemas, "bundle-manifest", manifestBytes); len(findings) != 0 {
		return Candidate{}, findings
	}
	artifact, err := writeArchive(files, manifestBytes)
	if err != nil {
		return Candidate{}, []domain.Finding{bundleFinding("GDS_BUNDLE_ARCHIVE_FAILED", err)}
	}
	identityDigest := digestJSON(map[string]any{
		"owner": trust.Source.Owner, "repository": trust.Source.Repository,
		"workflow": options.Workflow, "ref": options.SourceRef,
		"source_commit": options.SourceCommit,
	})
	envelope := ReleaseEnvelope{
		SchemaVersion: domain.SchemaVersion, BundleVersion: options.BundleVersion,
		ReleaseSequence: options.ReleaseSequence, Channel: options.Channel,
		SourceCommit: options.SourceCommit, SourceRef: options.SourceRef,
		ExecutableFiles: executableFiles,
		ManifestDigest:  digest(manifestBytes), ArtifactDigest: digest(artifact),
		ExpectedAttestationIdentityDigest: identityDigest,
	}
	envelopeBytes, _ := json.Marshal(envelope)
	if findings := validateDocument(schemas, "release-envelope", envelopeBytes); len(findings) != 0 {
		return Candidate{}, findings
	}
	return Candidate{
		Manifest: manifest, Envelope: envelope, ArtifactSize: len(artifact),
		ExecutableFiles: executableFiles, Artifact: artifact, ManifestBytes: manifestBytes,
	}, nil
}

func appendAdditionalFiles(files []sourceFile, additional []AdditionalFile) ([]sourceFile, error) {
	if len(files)+len(additional) > maxBundleFiles {
		return nil, fmt.Errorf("bundle file count exceeds limit")
	}
	result := append([]sourceFile(nil), files...)
	seen := make(map[string]struct{}, len(result)+len(additional))
	total := 0
	for _, file := range result {
		seen[file.record.Path] = struct{}{}
		total += len(file.content)
	}
	for _, file := range additional {
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(file.Path)))
		contentKind := file.ContentKind
		if contentKind == "" {
			contentKind = AdditionalContentText
		}
		if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") ||
			strings.ContainsRune(path, 0) || (file.Mode != "0644" && file.Mode != "0755") ||
			len(file.Content) > maxBundleFileBytes {
			return nil, fmt.Errorf("additional bundle file contract is invalid: %s", file.Path)
		}
		if contentKind != AdditionalContentText &&
			(contentKind != AdditionalContentOpaqueExecutable || file.Mode != "0755" ||
				!strings.HasPrefix(path, "bin/") || len(file.Content) == 0) {
			return nil, fmt.Errorf("additional bundle content kind is invalid: %s", file.Path)
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("additional bundle file path is duplicated: %s", path)
		}
		if contentKind == AdditionalContentText {
			if err := scanPortableContent(path, file.Content); err != nil {
				return nil, err
			}
		}
		seen[path] = struct{}{}
		total += len(file.Content)
		if total > maxBundleBytes {
			return nil, fmt.Errorf("bundle content exceeds aggregate size limit")
		}
		result = append(result, sourceFile{
			record:  FileRecord{Path: path, Digest: digest(file.Content), Size: len(file.Content), Mode: file.Mode},
			content: append([]byte(nil), file.Content...),
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].record.Path < result[right].record.Path })
	return result, nil
}

func collectPortableFiles(root string, trackedSources []string) ([]sourceFile, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if len(trackedSources) == 0 {
		return nil, fmt.Errorf("tracked portable source set is empty")
	}
	relativePaths := make([]string, 0, len(trackedSources))
	seen := map[string]struct{}{}
	for _, raw := range trackedSources {
		relative := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
		if relative == "." || filepath.IsAbs(relative) || relative == ".." ||
			strings.HasPrefix(relative, "../") || strings.ContainsRune(relative, 0) {
			return nil, fmt.Errorf("tracked source path is unsafe: %s", raw)
		}
		if _, duplicate := seen[relative]; duplicate {
			return nil, fmt.Errorf("tracked source path is duplicated: %s", relative)
		}
		seen[relative] = struct{}{}
		if portableSource(relative) {
			relativePaths = append(relativePaths, relative)
		}
	}
	for _, required := range portableFiles {
		if _, found := seen[required]; !found {
			return nil, fmt.Errorf("required portable source is not tracked: %s", required)
		}
	}
	sort.Strings(relativePaths)
	if len(relativePaths) > maxBundleFiles {
		return nil, fmt.Errorf("portable source file count exceeds limit")
	}
	files := make([]sourceFile, 0, len(relativePaths))
	total := 0
	for _, relative := range relativePaths {
		path := filepath.Join(absolute, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("portable source is not a regular file: %s", relative)
		}
		if info.Size() > maxBundleFileBytes {
			return nil, fmt.Errorf("portable source exceeds file limit: %s", relative)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := scanPortableContent(relative, content); err != nil {
			return nil, err
		}
		total += len(content)
		if total > maxBundleBytes {
			return nil, fmt.Errorf("portable source exceeds aggregate size limit")
		}
		mode := "0644"
		if info.Mode().Perm()&0o111 != 0 {
			mode = "0755"
		}
		files = append(files, sourceFile{
			record:  FileRecord{Path: relative, Digest: digest(content), Size: len(content), Mode: mode},
			content: append([]byte(nil), content...),
		})
	}
	return files, nil
}

func portableSource(relative string) bool {
	for _, file := range portableFiles {
		if relative == file {
			return true
		}
	}
	for _, root := range portableRoots {
		if strings.HasPrefix(relative, root+"/") {
			return true
		}
	}
	return false
}

func writeArchive(files []sourceFile, manifest []byte) ([]byte, error) {
	checksums := bytes.Buffer{}
	for _, file := range files {
		fmt.Fprintf(&checksums, "%s  %s\n", strings.TrimPrefix(file.record.Digest, "sha256:"), file.record.Path)
	}
	entries := append([]sourceFile(nil), files...)
	entries = append(entries,
		sourceFile{record: FileRecord{Path: "checksums.txt", Mode: "0644", Size: checksums.Len()}, content: checksums.Bytes()},
		sourceFile{record: FileRecord{Path: "manifest.json", Mode: "0644", Size: len(manifest)}, content: manifest},
	)
	sort.Slice(entries, func(left, right int) bool { return entries[left].record.Path < entries[right].record.Path })
	buffer := bytes.Buffer{}
	gzipWriter, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.Header.Name = ""
	gzipWriter.Header.Comment = ""
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		mode := int64(0o644)
		if entry.record.Mode == "0755" {
			mode = 0o755
		}
		header := &tar.Header{
			Name: entry.record.Path, Mode: mode, Size: int64(len(entry.content)),
			ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Unix(0, 0).UTC(),
			ChangeTime: time.Unix(0, 0).UTC(), Uid: 0, Gid: 0, Format: tar.FormatPAX,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tarWriter.Write(entry.content); err != nil {
			return nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func validateBuildOptions(options BuildOptions, trust TrustPolicy) *domain.Finding {
	if options.ReleaseSequence < trust.Release.MinimumReleaseSequence ||
		!contains(trust.Release.AllowedChannels, options.Channel) ||
		!contains(trust.Source.AllowedWorkflows, options.Workflow) ||
		!allowedRef(trust.Source.AllowedRefs, options.SourceRef) {
		finding := bundleFinding(
			"GDS_BUNDLE_BUILD_POLICY_BLOCKED",
			fmt.Errorf("release sequence, channel, workflow, or ref is outside trust policy"),
		)
		return &finding
	}
	if (options.Channel == "stable" || options.Channel == "frozen") &&
		(options.HarnessEvidenceManifestDigest == "" || options.HarnessEvidenceProvisional) {
		finding := bundleFinding("GDS_HARNESS_EVIDENCE_REQUIRED", fmt.Errorf("stable and frozen releases require exact non-provisional harness evidence"))
		return &finding
	}
	if options.Channel == "canary" && !options.HarnessEvidenceProvisional && options.HarnessEvidenceManifestDigest == "" {
		finding := bundleFinding("GDS_HARNESS_EVIDENCE_IDENTITY_MISSING", fmt.Errorf("non-provisional canary requires a bound harness evidence manifest"))
		return &finding
	}
	return nil
}

func validateDocument(schemas *validation.Set, schemaName string, raw []byte) []domain.Finding {
	value, err := serialization.Decode(schemaName+".json", raw)
	if err != nil {
		return []domain.Finding{bundleFinding("GDS_BUNDLE_DOCUMENT_INVALID", err)}
	}
	return schemas.Validate(schemaName, value, schemaName)
}

func scanPortableContent(path string, content []byte) error {
	if name, found := security.ScanContent(content); found {
		return fmt.Errorf("portable source %s contains forbidden %s marker", path, name)
	}
	if bytes.Contains(content, []byte("/Users/")) {
		return fmt.Errorf("portable source %s contains forbidden absolute home path marker", path)
	}
	return nil
}

func subsetDigest(records []FileRecord, prefix string) string {
	selected := []FileRecord{}
	for _, record := range records {
		if strings.HasPrefix(record.Path, prefix) {
			selected = append(selected, record)
		}
	}
	return digestJSON(selected)
}

func digestJSON(value any) string {
	raw, _ := json.Marshal(value)
	return digest(raw)
}

func digest(value []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(value))
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func allowedRef(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if pattern == value || (strings.HasSuffix(pattern, "*") &&
			strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))) {
			return true
		}
	}
	return false
}

func bundleFinding(code string, err error) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: err.Error(),
	}
}
