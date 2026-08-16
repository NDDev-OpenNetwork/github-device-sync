package releaseconsumer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/releasebuilder"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/semver"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type Verifier struct {
	Schemas      *validation.Set
	Attestations AttestationVerifier
}

func (verifier Verifier) Verify(
	ctx context.Context,
	request Request,
	state bundle.AcceptanceState,
	rollback *bundle.RollbackAuthorization,
	now time.Time,
) (VerifiedRelease, []domain.Finding) {
	if verifier.Schemas == nil || verifier.Attestations == nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_VERIFIER_UNAVAILABLE", "Release schemas or attestation verifier are unavailable.",
		)}
	}
	verificationRequest, snapshotRoot, err := snapshotVerificationInputs(request)
	if err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_SNAPSHOT_INVALID", "Release verification inputs could not be captured immutably.",
		)}
	}
	snapshotOwned := true
	defer func() {
		if snapshotOwned {
			_ = os.RemoveAll(snapshotRoot)
		}
	}()
	request = verificationRequest
	directory, err := releasebuilder.VerifyDirectory(request.ReleaseDirectory, verifier.Schemas)
	if err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_DIRECTORY_INVALID", "Release directory failed deterministic verification.",
		)}
	}
	manifest, err := loadManifest(request.ReleaseDirectory, verifier.Schemas)
	if err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_MANIFEST_INVALID", "Detached release manifest is invalid.",
		)}
	}
	envelope, err := loadEnvelope(request.ReleaseDirectory, verifier.Schemas)
	if err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_ENVELOPE_INVALID", "Detached release envelope is invalid.",
		)}
	}
	trust, err := bundle.LoadTrustFile(request.TrustPolicyPath, verifier.Schemas)
	if err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_TRUST_POLICY_INVALID", "Local consumer trust policy is invalid.",
		)}
	}
	evidenceFiles, trustDigest, err := verificationInputFiles(request)
	if err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_OFFLINE_INPUT_INVALID", "Offline evidence or local trust input is invalid.",
		)}
	}
	if compared, valid := semver.Compare(request.ConsumerVersion, manifest.MinimumCLIVersion); !valid || compared < 0 {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_CLI_VERSION_BLOCKED", "Current GDS CLI is below the release verification floor.",
		)}
	}
	if err := bundle.ValidateReleaseExecutableMatrix(manifest); err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_EXECUTABLE_MATRIX_INVALID", "Release does not contain the complete portable executable matrix.",
		)}
	}
	if !bundle.ReleaseTargetSupported(request.TargetOS, request.TargetArch) {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_PLATFORM_UNSUPPORTED", "Requested release target is not supported.",
		)}
	}
	if err := validateSBOMCoverage(request.ReleaseDirectory, manifest); err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_SBOM_COVERAGE_INVALID", "Release SBOM does not cover every executable bundle member.",
		)}
	}
	if len(trust.Source.AllowedWorkflows) != 1 {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_WORKFLOW_POLICY_AMBIGUOUS", "Consumer trust must name one exact release workflow.",
		)}
	}
	if _, err := releasebuilder.VerifyTrustedRootDigest(
		filepath.Join(request.EvidenceDirectory, TrustedRootName),
		trust.Verification.TrustedRootDigest, trust.TrustDomain,
	); err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_TRUSTED_ROOT_NOT_PROVEN", "Offline trusted root does not match the independent local trust policy.",
		)}
	}
	evidence, err := verifier.Attestations.Verify(ctx, AttestationRequest{
		ReleaseDirectory: request.ReleaseDirectory, EvidenceDirectory: request.EvidenceDirectory,
		ArtifactName: directory.ArtifactName, ArtifactDigest: directory.ArtifactDigest,
		SourceCommit: directory.SourceCommit, SourceRef: directory.SourceRef,
		SourceOwner: trust.Source.Owner, SourceRepository: trust.Source.Repository,
		Workflow: trust.Source.AllowedWorkflows[0],
	})
	if err != nil {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_ATTESTATION_NOT_PROVEN", "Offline provenance or SBOM verification failed.",
		)}
	}
	afterEvidence, afterTrustDigest, err := verificationInputFiles(request)
	if err != nil || afterTrustDigest != trustDigest || !sameOutputFiles(evidenceFiles, afterEvidence) {
		return VerifiedRelease{}, []domain.Finding{finding(
			"GDS_RELEASE_OFFLINE_INPUT_CHANGED", "Offline evidence or local trust changed during verification.",
		)}
	}
	policy, findings := bundle.Verify(envelope, trust, evidence, state, rollback, now.UTC())
	if len(findings) != 0 {
		return VerifiedRelease{
			SchemaVersion: domain.SchemaVersion, Status: "quarantined", Directory: directory, Envelope: envelope,
			Manifest: manifest, Trust: trust, TrustPolicyDigest: trustDigest,
			EvidenceFiles: evidenceFiles, Evidence: evidence, Policy: policy, VerifiedAt: now.UTC(),
		}, findings
	}
	verified := VerifiedRelease{
		SchemaVersion: domain.SchemaVersion, Status: "verified", Directory: directory, Envelope: envelope,
		Manifest: manifest, Trust: trust, TrustPolicyDigest: trustDigest,
		EvidenceFiles: evidenceFiles, Evidence: evidence, Policy: policy, VerifiedAt: now.UTC(),
		snapshotRoot: snapshotRoot, snapshotRequest: request,
	}
	snapshotOwned = false
	return verified, nil
}

func verificationInputFiles(request Request) ([]releasebuilder.OutputFile, string, error) {
	files := make([]releasebuilder.OutputFile, 0, 3)
	for _, name := range []string{ProvenanceBundleName, SBOMBundleName, TrustedRootName} {
		path := filepath.Join(request.EvidenceDirectory, name)
		if err := boundedRegular(path, maximumEvidenceFile); err != nil {
			return nil, "", err
		}
		digest, err := fileDigest(path)
		if err != nil {
			return nil, "", err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, "", err
		}
		files = append(files, releasebuilder.OutputFile{Path: name, Digest: digest, Size: int(info.Size())})
	}
	if err := boundedRegular(request.TrustPolicyPath, 1<<20); err != nil {
		return nil, "", err
	}
	trustDigest, err := fileDigest(request.TrustPolicyPath)
	return files, trustDigest, err
}

func sameOutputFiles(left, right []releasebuilder.OutputFile) bool {
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

type coverageSBOM struct {
	Files []struct {
		FileName  string `json:"fileName"`
		Checksums []struct {
			Algorithm     string `json:"algorithm"`
			ChecksumValue string `json:"checksumValue"`
		} `json:"checksums"`
	} `json:"files"`
}

func validateSBOMCoverage(directory string, manifest bundle.Manifest) error {
	raw, err := os.ReadFile(filepath.Join(directory, "sbom.spdx.json"))
	if err != nil {
		return err
	}
	var document coverageSBOM
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	covered := map[string]string{}
	for _, file := range document.Files {
		path := strings.TrimPrefix(file.FileName, "./")
		if path == "" || path == "." || filepath.IsAbs(filepath.FromSlash(path)) ||
			strings.Contains(path, "\\") || path == ".." || strings.HasPrefix(path, "../") ||
			path != filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) {
			return fmt.Errorf("SBOM file path is invalid")
		}
		if _, duplicate := covered[path]; duplicate {
			return fmt.Errorf("SBOM file path is duplicated")
		}
		for _, checksum := range file.Checksums {
			if checksum.Algorithm == "SHA256" {
				covered[path] = "sha256:" + checksum.ChecksumValue
			}
		}
	}
	for _, file := range manifest.Files {
		if file.Mode == "0755" && covered[file.Path] != file.Digest {
			return fmt.Errorf("SBOM does not bind executable %s", file.Path)
		}
	}
	return nil
}

func loadManifest(directory string, schemas *validation.Set) (bundle.Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return bundle.Manifest{}, err
	}
	value, err := serialization.Decode("manifest.json", raw)
	if err != nil {
		return bundle.Manifest{}, err
	}
	if findings := schemas.Validate("bundle-manifest", value, "manifest.json"); len(findings) != 0 {
		return bundle.Manifest{}, fmt.Errorf("manifest has %d validation findings", len(findings))
	}
	var manifest bundle.Manifest
	if err := serialization.DecodeInto("manifest.json", raw, &manifest); err != nil {
		return bundle.Manifest{}, err
	}
	return manifest, nil
}

func loadEnvelope(directory string, schemas *validation.Set) (bundle.ReleaseEnvelope, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "release-envelope.json"))
	if err != nil {
		return bundle.ReleaseEnvelope{}, err
	}
	value, err := serialization.Decode("release-envelope.json", raw)
	if err != nil {
		return bundle.ReleaseEnvelope{}, err
	}
	if findings := schemas.Validate("release-envelope", value, "release-envelope.json"); len(findings) != 0 {
		return bundle.ReleaseEnvelope{}, fmt.Errorf("release envelope has %d validation findings", len(findings))
	}
	var envelope bundle.ReleaseEnvelope
	if err := serialization.DecodeInto("release-envelope.json", raw, &envelope); err != nil {
		return bundle.ReleaseEnvelope{}, err
	}
	return envelope, nil
}

func finding(code, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
