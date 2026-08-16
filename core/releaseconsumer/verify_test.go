package releaseconsumer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type staticAttestationVerifier struct {
	mutate func(*bundle.AttestationEvidence)
}

func (verifier staticAttestationVerifier) Verify(
	_ context.Context,
	request AttestationRequest,
) (bundle.AttestationEvidence, error) {
	evidence := bundle.AttestationEvidence{
		Verified: true, ArtifactDigest: request.ArtifactDigest,
		SourceOwner: request.SourceOwner, SourceRepository: request.SourceRepository,
		Workflow: request.Workflow, SourceRef: request.SourceRef,
		SourceCommit: request.SourceCommit, SBOMVerified: true, OfflineMaterial: true,
		VerifierName: "github-cli", VerifierVersion: "2.96.0",
		VerifierOS: "linux", VerifierArch: "amd64", VerifierPath: "/usr/bin/gh",
		VerifierDigest: "sha256:" + strings.Repeat("f", 64),
	}
	if verifier.mutate != nil {
		verifier.mutate(&evidence)
	}
	return evidence, nil
}

func TestVerifierBindsStructureTrustEvidenceAndVersion(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	release, evidence, trust := writeVerificationFixture(t, schemas)
	request := Request{
		ReleaseDirectory: release, EvidenceDirectory: evidence,
		TrustPolicyPath: trust, ConsumerVersion: "1.0.0",
		TargetOS: "linux", TargetArch: "amd64",
	}
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	verified, findings := (Verifier{
		Schemas: schemas, Attestations: staticAttestationVerifier{},
	}).Verify(context.Background(), request, bundle.AcceptanceState{}, nil, now)
	if len(findings) != 0 || verified.Status != "verified" || verified.Policy.Status != "accepted" {
		t.Fatalf("verified=%+v findings=%+v", verified, findings)
	}
	if verified.Envelope.SourceRef != "refs/tags/gds-v1.2.3" ||
		verified.Directory.ArtifactDigest != verified.Evidence.ArtifactDigest {
		t.Fatalf("verification identity is incomplete: %+v", verified)
	}

	request.ConsumerVersion = "0.9.9"
	_, findings = (Verifier{
		Schemas: schemas, Attestations: staticAttestationVerifier{},
	}).Verify(context.Background(), request, bundle.AcceptanceState{}, nil, now)
	assertReleaseFinding(t, findings, "GDS_RELEASE_CLI_VERSION_BLOCKED")

	request.ConsumerVersion = "1.0.0"
	_, findings = (Verifier{
		Schemas: schemas,
		Attestations: staticAttestationVerifier{mutate: func(value *bundle.AttestationEvidence) {
			value.SourceRef = "refs/heads/main"
		}},
	}).Verify(context.Background(), request, bundle.AcceptanceState{}, nil, now)
	assertReleaseFinding(t, findings, "GDS_BUNDLE_ATTESTATION_IDENTITY_MISMATCH")
}

func TestVerifierRejectsTrustedRootOutsideLocalPin(t *testing.T) {
	t.Parallel()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	release, evidence, trust := writeVerificationFixture(t, schemas)
	if err := os.WriteFile(
		filepath.Join(evidence, TrustedRootName), []byte("substituted-root\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	_, findings := (Verifier{
		Schemas: schemas, Attestations: staticAttestationVerifier{},
	}).Verify(context.Background(), Request{
		ReleaseDirectory: release, EvidenceDirectory: evidence, TrustPolicyPath: trust,
		ConsumerVersion: "1.0.0", TargetOS: "linux", TargetArch: "amd64",
	}, bundle.AcceptanceState{}, nil, time.Now().UTC())
	assertReleaseFinding(t, findings, "GDS_RELEASE_TRUSTED_ROOT_NOT_PROVEN")
}

func TestVerifierRejectsEveryIncompleteExecutableMatrix(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, omitted := range bundle.RequiredReleaseExecutablePaths() {
		omitted := omitted
		t.Run(omitted, func(t *testing.T) {
			release, evidence, trust := writeVerificationFixtureVersion(t, schemas, "1.2.3", 7, omitted)
			_, findings := (Verifier{
				Schemas: schemas, Attestations: staticAttestationVerifier{},
			}).Verify(context.Background(), Request{
				ReleaseDirectory: release, EvidenceDirectory: evidence, TrustPolicyPath: trust,
				ConsumerVersion: "1.0.0", TargetOS: "linux", TargetArch: "amd64",
			}, bundle.AcceptanceState{}, nil, time.Now().UTC())
			assertReleaseFinding(t, findings, "GDS_RELEASE_EXECUTABLE_MATRIX_INVALID")
		})
	}
}

func writeVerificationFixture(t *testing.T, schemas *validation.Set) (string, string, string) {
	return writeVerificationFixtureVersion(t, schemas, "1.2.3", 7)
}

func writeVerificationFixtureVersion(
	t *testing.T,
	schemas *validation.Set,
	version string,
	sequence int,
	omittedExecutables ...string,
) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	registry := filepath.Join(root, "skills", "registry.yaml")
	if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte("schema_version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trustRaw := []byte(`schema_version: 1
trust_domain: "gds-release"
source:
  owner: "example-user"
  repository: "github-device-sync"
  allowed_workflows: [".github/workflows/release-bundle.yml"]
  allowed_refs: ["refs/heads/main", "refs/tags/gds-v*"]
release:
  minimum_release_sequence: 1
  allowed_channels: ["canary", "stable", "frozen"]
verification:
  attestation: "required"
  sbom_for_executables: "required"
  offline_material: "required"
  trusted_root_digest: "sha256:e80b71cd14d3cbd65f4173abcbfcf01a545dbca32a72d575108b553a648cc96f"
  verifier:
    name: "github-cli"
    version: "2.96.0"
    executables:
      - os: "linux"
        arch: "amd64"
        digest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
`)
	trust := bundle.TrustPolicy{
		SchemaVersion: 1, TrustDomain: "gds-release",
		Source: bundle.TrustSource{
			Owner: "example-user", Repository: "github-device-sync",
			AllowedWorkflows: []string{".github/workflows/release-bundle.yml"},
			AllowedRefs:      []string{"refs/heads/main", "refs/tags/gds-v*"},
		},
		Release: bundle.TrustRelease{MinimumReleaseSequence: 1, AllowedChannels: []string{"canary", "stable", "frozen"}},
		Verification: bundle.TrustVerification{
			Attestation: "required", SBOMForExecutables: "required", OfflineMaterial: "required",
			TrustedRootDigest: "sha256:e80b71cd14d3cbd65f4173abcbfcf01a545dbca32a72d575108b553a648cc96f",
			Verifier: bundle.TrustVerifier{
				Name: "github-cli", Version: "2.96.0",
				Executables: []bundle.TrustVerifierExecutable{{
					OS: "linux", Arch: "amd64", Digest: "sha256:" + strings.Repeat("f", 64),
				}},
			},
		},
	}
	binary := []byte("fixture executable " + version)
	binaryDigest := strings.TrimPrefix(bytesDigest(binary), "sha256:")
	sbom := verificationSBOM(t, binaryDigest)
	additional := make([]bundle.AdditionalFile, 0, len(bundle.RequiredReleaseExecutablePaths())+2)
	for _, path := range bundle.RequiredReleaseExecutablePaths() {
		if len(omittedExecutables) != 0 && path == omittedExecutables[0] {
			continue
		}
		additional = append(additional, bundle.AdditionalFile{
			Path: path, Content: binary, Mode: "0755", ContentKind: bundle.AdditionalContentOpaqueExecutable,
		})
	}
	additional = append(additional,
		bundle.AdditionalFile{Path: "sbom/gds.spdx.json", Content: sbom, Mode: "0644"},
		bundle.AdditionalFile{Path: "trust/bundle-trust.yaml", Content: trustRaw, Mode: "0644"},
	)
	candidate, findings := bundle.Build(root, bundle.BuildOptions{
		BundleVersion: version, ReleaseSequence: sequence, Channel: "stable",
		SourceCommit: strings.Repeat("a", 40), SourceRef: "refs/tags/gds-v" + version,
		MinimumCLIVersion: "1.0.0", Workflow: trust.Source.AllowedWorkflows[0],
		HarnessEvidenceManifestDigest: "sha256:" + strings.Repeat("e", 64),
		TrackedSources:                []string{"skills/registry.yaml"},
		AdditionalFiles:               additional,
	}, trust, schemas)
	if len(findings) != 0 {
		t.Fatalf("bundle findings: %+v", findings)
	}
	release := filepath.Join(t.TempDir(), "release")
	if err := os.Mkdir(release, 0o755); err != nil {
		t.Fatal(err)
	}
	envelopeRaw, _ := json.MarshalIndent(candidate.Envelope, "", "  ")
	envelopeRaw = append(envelopeRaw, '\n')
	files := map[string][]byte{
		"gds-bundle-v" + version + ".tar.gz": candidate.Artifact,
		"release-envelope.json":              envelopeRaw,
		"manifest.json":                      candidate.ManifestBytes,
		"sbom.spdx.json":                     sbom,
		"bundle-trust.yaml":                  trustRaw,
	}
	checksums := ""
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		checksums += strings.TrimPrefix(bytesDigest(files[path]), "sha256:") + "  " + path + "\n"
		if err := os.WriteFile(filepath.Join(release, path), files[path], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(release, "SHA256SUMS"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
	evidence := t.TempDir()
	for _, name := range []string{ProvenanceBundleName, SBOMBundleName, TrustedRootName} {
		if err := os.WriteFile(filepath.Join(evidence, name), []byte("fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	trustPath := filepath.Join(t.TempDir(), "bundle-trust.yaml")
	if err := os.WriteFile(trustPath, trustRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	return release, evidence, trustPath
}

func verificationSBOM(t *testing.T, binaryDigest string) []byte {
	t.Helper()
	files := make([]map[string]any, 0, len(bundle.RequiredReleaseExecutablePaths()))
	relationships := []map[string]any{{
		"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES",
		"relatedSpdxElement": "SPDXRef-Package-gds",
	}}
	for index, path := range bundle.RequiredReleaseExecutablePaths() {
		id := fmt.Sprintf("SPDXRef-File-%02d", index)
		files = append(files, map[string]any{
			"fileName": "./" + path, "SPDXID": id,
			"checksums":        []map[string]string{{"algorithm": "SHA256", "checksumValue": binaryDigest}},
			"licenseConcluded": "NOASSERTION", "copyrightText": "NOASSERTION",
		})
		relationships = append(relationships, map[string]any{
			"spdxElementId": "SPDXRef-Package-gds", "relationshipType": "CONTAINS",
			"relatedSpdxElement": id,
		})
	}
	document := map[string]any{
		"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT",
		"name": "gds-fixture", "documentNamespace": "https://example.invalid/gds-fixture",
		"creationInfo": map[string]any{
			"created": "2026-07-11T00:00:00Z", "creators": []string{"Tool: fixture"},
		},
		"packages": []map[string]any{{
			"name": "gds", "SPDXID": "SPDXRef-Package-gds", "downloadLocation": "NOASSERTION",
			"filesAnalyzed": false, "licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION",
			"copyrightText": "NOASSERTION",
		}},
		"files": files, "relationships": relationships,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertReleaseFinding(t *testing.T, findings []domain.Finding, code string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("expected %s, got %+v", code, findings)
}

// The runbook contracts the evidence directory as exactly three files and warns
// that staging anything else into a consumer input breaks the contract. An input
// the operator is told is invalid must not verify.
func TestSnapshotRejectsAnExtraEvidenceFile(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	release, evidence, trust := writeVerificationFixture(t, schemas)
	if err := os.WriteFile(
		filepath.Join(evidence, "diagnostic.json"), []byte("{}\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	_, root, snapshotErr := snapshotVerificationInputs(Request{
		ReleaseDirectory: release, EvidenceDirectory: evidence, TrustPolicyPath: trust,
	})
	if root != "" {
		defer os.RemoveAll(root)
	}
	if snapshotErr == nil {
		t.Fatal("a fourth evidence entry must be rejected, not ignored")
	}
	if !strings.Contains(snapshotErr.Error(), "expected 3") {
		t.Fatalf("error must name the contracted count, got %v", snapshotErr)
	}
}

func TestSnapshotAcceptsTheExactThreeEvidenceFiles(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	release, evidence, trust := writeVerificationFixture(t, schemas)
	_, root, snapshotErr := snapshotVerificationInputs(Request{
		ReleaseDirectory: release, EvidenceDirectory: evidence, TrustPolicyPath: trust,
	})
	if root != "" {
		defer os.RemoveAll(root)
	}
	if snapshotErr != nil {
		t.Fatalf("the contracted evidence set must snapshot cleanly, got %v", snapshotErr)
	}
}
