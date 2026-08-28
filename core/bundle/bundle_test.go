package bundle

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const testSourceCommit = "0123456789abcdef0123456789abcdef01234567"

func TestBuildIsReproducibleAndReleaseUnitVerifies(t *testing.T) {
	root := newPortableFixture(t)
	schemas := testSchemas(t)
	trust := testTrust()
	options := testBuildOptions(root)

	first, findings := Build(root, options, trust, schemas)
	if len(findings) != 0 {
		t.Fatalf("first build findings: %#v", findings)
	}
	second, findings := Build(root, options, trust, schemas)
	if len(findings) != 0 {
		t.Fatalf("second build findings: %#v", findings)
	}
	if !bytes.Equal(first.Artifact, second.Artifact) {
		t.Fatal("identical inputs produced different bundle bytes")
	}
	if first.Envelope != second.Envelope {
		t.Fatal("identical inputs produced different release envelopes")
	}
	if first.ExecutableFiles != 1 || first.Envelope.ExecutableFiles != 1 {
		t.Fatalf("unexpected executable count: candidate=%d envelope=%d", first.ExecutableFiles, first.Envelope.ExecutableFiles)
	}

	manifest, findings := VerifyReleaseUnit(first.Artifact, first.Envelope, schemas)
	if len(findings) != 0 {
		t.Fatalf("release verification findings: %#v", findings)
	}
	if manifest.ContentSetDigest != first.Manifest.ContentSetDigest {
		t.Fatal("verified manifest differs from built manifest")
	}
	for _, file := range manifest.Files {
		if len(file.Path) >= len("estate/") && file.Path[:len("estate/")] == "estate/" {
			t.Fatalf("private estate source leaked into bundle: %s", file.Path)
		}
	}
}

func TestBuildRejectsPrivateMarkersAndSymlinks(t *testing.T) {
	t.Run("private-marker", func(t *testing.T) {
		root := newPortableFixture(t)
		path := filepath.Join(root, "policies", "test.txt")
		if err := os.WriteFile(path, []byte("private path: /"+"Users/example/project\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, findings := Build(root, testBuildOptions(root), testTrust(), testSchemas(t))
		assertFinding(t, findings, "GDS_BUNDLE_SOURCE_INVALID")
	})

	t.Run("symlink", func(t *testing.T) {
		root := newPortableFixture(t)
		target := filepath.Join(root, "target.txt")
		if err := os.WriteFile(target, []byte("target\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "policies", "link.txt")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink is unavailable: %v", err)
		}
		_, findings := Build(root, testBuildOptions(root), testTrust(), testSchemas(t))
		assertFinding(t, findings, "GDS_BUNDLE_SOURCE_INVALID")
	})
}

func TestVerifyReleaseUnitRejectsOuterAndInnerTampering(t *testing.T) {
	root := newPortableFixture(t)
	schemas := testSchemas(t)
	candidate, findings := Build(root, testBuildOptions(root), testTrust(), schemas)
	if len(findings) != 0 {
		t.Fatalf("build findings: %#v", findings)
	}

	outer := append([]byte(nil), candidate.Artifact...)
	outer[len(outer)-1] ^= 0xff
	_, findings = VerifyReleaseUnit(outer, candidate.Envelope, schemas)
	assertFinding(t, findings, "GDS_BUNDLE_ARTIFACT_DIGEST_MISMATCH")

	files, err := readArchive(candidate.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	records := make([]sourceFile, 0, len(candidate.Manifest.Files))
	mutated := false
	for _, record := range candidate.Manifest.Files {
		content := append([]byte(nil), files[record.Path].content...)
		if !mutated {
			content[0] ^= 0xff
			mutated = true
		}
		records = append(records, sourceFile{record: record, content: content})
	}
	inner, err := writeArchive(records, candidate.ManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	envelope := candidate.Envelope
	envelope.ArtifactDigest = digest(inner)
	_, findings = VerifyReleaseUnit(inner, envelope, schemas)
	assertFinding(t, findings, "GDS_BUNDLE_FILE_DIGEST_MISMATCH")
}

func TestMaterializeProjectionSourceExposesOnlyVerifiedCanonicalInputs(t *testing.T) {
	root := newPortableFixture(t)
	candidate, findings := Build(root, testBuildOptions(root), testTrust(), testSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("build findings: %#v", findings)
	}
	materialized, manifest, cleanup, findings := MaterializeProjectionSource(
		candidate.Artifact, candidate.Envelope, testSchemas(t),
	)
	defer cleanup()
	if len(findings) != 0 || manifest.BundleVersion != "1.0.0" {
		t.Fatalf("manifest=%#v findings=%#v", manifest, findings)
	}
	for _, required := range []string{"policies", "schemas", "templates"} {
		if info, err := os.Stat(filepath.Join(materialized, required)); err != nil || !info.IsDir() {
			t.Fatalf("required source %s is unavailable: %v", required, err)
		}
	}
	if _, err := os.Stat(filepath.Join(materialized, "plugins")); !os.IsNotExist(err) {
		t.Fatalf("non-projection plugin tree was materialized: %v", err)
	}
}

func TestVerifyAttestationAndAntiRollbackPolicy(t *testing.T) {
	root := newPortableFixture(t)
	candidate, findings := Build(root, testBuildOptions(root), testTrust(), testSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("build findings: %#v", findings)
	}
	evidence := testEvidence(candidate)
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)

	result, findings := Verify(candidate.Envelope, testTrust(), evidence, AcceptanceState{}, nil, now)
	if len(findings) != 0 || result.Status != "accepted" || result.Rollback {
		t.Fatalf("valid evidence was not accepted: result=%#v findings=%#v", result, findings)
	}

	state := AcceptanceState{HighestSequence: 2, AcceptedDigests: map[int]string{2: "sha256:" + string(bytes.Repeat([]byte{'b'}, 64))}}
	result, findings = Verify(candidate.Envelope, testTrust(), evidence, state, nil, now)
	assertFinding(t, findings, "GDS_BUNDLE_ROLLBACK_BLOCKED")
	if result.Status != "quarantined" {
		t.Fatalf("downgrade was not quarantined: %#v", result)
	}

	authorization := &RollbackAuthorization{
		RolloutID:      "rollout_01J00000000000000000000000",
		TargetSequence: candidate.Envelope.ReleaseSequence,
		TargetDigest:   candidate.Envelope.ArtifactDigest,
		ScopeDigest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Reason:         "test rollback authorization",
		ApprovalRef:    "approval:test-only",
		ExpiresAt:      now.Add(time.Hour),
	}
	result, findings = Verify(candidate.Envelope, testTrust(), evidence, state, authorization, now)
	if len(findings) != 0 || result.Status != "accepted" || !result.Rollback {
		t.Fatalf("exact rollback authorization was not accepted: result=%#v findings=%#v", result, findings)
	}

	forward := candidate.Envelope
	forward.ReleaseSequence = 3
	forward.BundleVersion = "0.9.0"
	state = AcceptanceState{
		HighestSequence:  2,
		AcceptedDigests:  map[int]string{2: digest(bytes.Repeat([]byte{'b'}, 64))},
		AcceptedVersions: map[int]string{2: "1.0.0"},
	}
	result, findings = Verify(forward, testTrust(), evidence, state, nil, now)
	assertFinding(t, findings, "GDS_BUNDLE_VERSION_REGRESSION")
	if result.Status != "quarantined" {
		t.Fatalf("semantic version regression was not quarantined: %#v", result)
	}

	evidence.Verified = false
	result, findings = Verify(candidate.Envelope, testTrust(), evidence, AcceptanceState{}, nil, now)
	assertFinding(t, findings, "GDS_BUNDLE_ATTESTATION_INVALID")
	if result.Status != "quarantined" {
		t.Fatalf("unverified provenance was not quarantined: %#v", result)
	}

	evidence = testEvidence(candidate)
	evidence.SourceRef = "refs/tags/gds-v1.0.0"
	result, findings = Verify(candidate.Envelope, testTrust(), evidence, AcceptanceState{}, nil, now)
	assertFinding(t, findings, "GDS_BUNDLE_ATTESTATION_IDENTITY_MISMATCH")
	if result.Status != "quarantined" {
		t.Fatalf("mismatched source ref was not quarantined: %#v", result)
	}
}

func TestBuildRejectsPolicyOutsideTrust(t *testing.T) {
	root := newPortableFixture(t)
	options := testBuildOptions(root)
	options.SourceRef = "refs/heads/untrusted"
	_, findings := Build(root, options, testTrust(), testSchemas(t))
	assertFinding(t, findings, "GDS_BUNDLE_BUILD_POLICY_BLOCKED")
}

func TestBuildUsesOnlyTrackedPortableSources(t *testing.T) {
	root := newPortableFixture(t)
	options := testBuildOptions(root)
	ignored := filepath.Join(root, "plugins", "gds-core", ".DS_Store")
	if err := os.WriteFile(ignored, []byte("ignored metadata\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate, findings := Build(root, options, testTrust(), testSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	for _, file := range candidate.Manifest.Files {
		if file.Path == "plugins/gds-core/.DS_Store" {
			t.Fatal("untracked ignored metadata entered the release bundle")
		}
	}
	options.TrackedSources = nil
	_, findings = Build(root, options, testTrust(), testSchemas(t))
	assertFinding(t, findings, "GDS_BUNDLE_SOURCE_INVALID")
}

func TestBuildIncludesBoundedAdditionalReleaseFiles(t *testing.T) {
	root := newPortableFixture(t)
	options := testBuildOptions(root)
	options.AdditionalFiles = []AdditionalFile{
		{Path: "bin/linux/amd64/gds", Content: []byte("binary\n"), Mode: "0755", ContentKind: AdditionalContentOpaqueExecutable},
		{Path: "sbom/gds.spdx.json", Content: []byte("{}\n"), Mode: "0644"},
	}
	first, findings := Build(root, options, testTrust(), testSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	second, findings := Build(root, options, testTrust(), testSchemas(t))
	if len(findings) != 0 || !bytes.Equal(first.Artifact, second.Artifact) {
		t.Fatalf("second findings=%#v reproducible=%v", findings, bytes.Equal(first.Artifact, second.Artifact))
	}
	manifest, findings := VerifyReleaseUnit(first.Artifact, first.Envelope, testSchemas(t))
	if len(findings) != 0 {
		t.Fatalf("verify findings=%#v", findings)
	}
	if manifest.ExecutableFileCount() != 2 {
		t.Fatalf("executable count=%d", manifest.ExecutableFileCount())
	}

	for _, invalid := range []AdditionalFile{
		{Path: "../escape", Content: []byte("x"), Mode: "0644"},
		{Path: "policies/test.txt", Content: []byte("duplicate"), Mode: "0644"},
		{Path: "bin/gds", Content: []byte("x"), Mode: "0777"},
		{Path: "docs/gds", Content: []byte("x"), Mode: "0755", ContentKind: AdditionalContentOpaqueExecutable},
		{Path: "bin/gds", Content: []byte("x"), Mode: "0644", ContentKind: AdditionalContentOpaqueExecutable},
		{Path: "bin/gds", Content: []byte("x"), Mode: "0755", ContentKind: "unknown"},
	} {
		bad := options
		bad.AdditionalFiles = []AdditionalFile{invalid}
		_, findings := Build(root, bad, testTrust(), testSchemas(t))
		assertFinding(t, findings, "GDS_BUNDLE_ADDITIONAL_FILE_INVALID")
	}
}

func TestBuildRejectsExcessiveAdditionalFileCount(t *testing.T) {
	root := newPortableFixture(t)
	options := testBuildOptions(root)
	options.AdditionalFiles = make([]AdditionalFile, maxBundleFiles+1)
	_, findings := Build(root, options, testTrust(), testSchemas(t))
	assertFinding(t, findings, "GDS_BUNDLE_ADDITIONAL_FILE_INVALID")
}

func newPortableFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, relative := range portableRoots {
		directory := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "test.txt"), []byte(relative+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, relative := range portableFiles {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("schema_version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(root, "plugins", "gds-core", "run.sh")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "estate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "estate", "private.txt"), []byte("must-not-ship\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func testSchemas(t *testing.T) *validation.Set {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	return schemas
}

func testTrust() TrustPolicy {
	return TrustPolicy{
		SchemaVersion: 1,
		TrustDomain:   "gds-release",
		Source: TrustSource{
			Owner: "example-user", Repository: "github-device-sync",
			AllowedWorkflows: []string{".github/workflows/release-bundle.yml"},
			AllowedRefs:      []string{"refs/heads/main", "refs/tags/gds-v*"},
		},
		Release: TrustRelease{MinimumReleaseSequence: 1, AllowedChannels: []string{"canary", "stable"}},
		Verification: TrustVerification{
			Attestation: "required", SBOMForExecutables: "required", OfflineMaterial: "required",
			TrustedRootDigest: "sha256:65ca537f6ed8a47fd0e560c421baa1f6c1efb8b25fc200d8c5c02c0e92eb2b9c",
			Verifier: TrustVerifier{
				Name: "github-cli", Version: "2.96.0",
				Executables: []TrustVerifierExecutable{{
					OS: "linux", Arch: "amd64", Digest: "sha256:" + strings.Repeat("f", 64),
				}},
			},
		},
	}
}

func testBuildOptions(root string) BuildOptions {
	return BuildOptions{
		BundleVersion: "1.0.0", ReleaseSequence: 1, Channel: "canary",
		SourceCommit: testSourceCommit, MinimumCLIVersion: "1.0.0",
		Workflow: ".github/workflows/release-bundle.yml", SourceRef: "refs/heads/main",
		TrackedSources:             fixtureTrackedSources(root),
		HarnessEvidenceProvisional: true,
	}
}

func fixtureTrackedSources(root string) []string {
	paths := []string{}
	for _, relativeRoot := range portableRoots {
		directory := filepath.Join(root, filepath.FromSlash(relativeRoot))
		_ = filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			relative, relativeErr := filepath.Rel(root, path)
			if relativeErr != nil {
				return relativeErr
			}
			paths = append(paths, filepath.ToSlash(relative))
			return nil
		})
	}
	paths = append(paths, portableFiles...)
	sort.Strings(paths)
	return paths
}

func testEvidence(candidate Candidate) AttestationEvidence {
	return AttestationEvidence{
		Verified: true, ArtifactDigest: candidate.Envelope.ArtifactDigest,
		SourceOwner: "example-user", SourceRepository: "github-device-sync",
		Workflow: ".github/workflows/release-bundle.yml", SourceRef: "refs/heads/main",
		SourceCommit: testSourceCommit, SBOMVerified: true, OfflineMaterial: true,
		VerifierName: "github-cli", VerifierVersion: "2.96.0",
		VerifierOS: "linux", VerifierArch: "amd64", VerifierPath: "/usr/bin/gh",
		VerifierDigest: "sha256:" + strings.Repeat("f", 64),
	}
}

func assertFinding(t *testing.T, findings []domain.Finding, code string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("expected finding %s, got %#v", code, findings)
}
