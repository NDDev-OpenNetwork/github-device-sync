package bundle

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func Verify(
	envelope ReleaseEnvelope,
	trust TrustPolicy,
	evidence AttestationEvidence,
	state AcceptanceState,
	rollback *RollbackAuthorization,
	now time.Time,
) (VerificationResult, []domain.Finding) {
	findings := []domain.Finding{}
	if !contains(trust.Release.AllowedChannels, envelope.Channel) {
		findings = append(findings, verificationFinding(
			"GDS_BUNDLE_CHANNEL_BLOCKED", "Bundle channel is outside the consumer trust policy.",
		))
	}
	if !evidence.Verified || evidence.ArtifactDigest != envelope.ArtifactDigest {
		findings = append(findings, verificationFinding(
			"GDS_BUNDLE_ATTESTATION_INVALID", "Artifact provenance or digest was not verified.",
		))
	}
	if evidence.SourceOwner != trust.Source.Owner ||
		evidence.SourceRepository != trust.Source.Repository ||
		!contains(trust.Source.AllowedWorkflows, evidence.Workflow) ||
		!allowedRef(trust.Source.AllowedRefs, evidence.SourceRef) || evidence.SourceRef != envelope.SourceRef ||
		evidence.SourceCommit != envelope.SourceCommit {
		findings = append(findings, verificationFinding(
			"GDS_BUNDLE_ATTESTATION_IDENTITY_MISMATCH",
			"Attestation owner, repository, workflow, ref, or source commit is outside trust policy.",
		))
	}
	verifierDigestApproved := false
	for _, executable := range trust.Verification.Verifier.Executables {
		if executable.OS == evidence.VerifierOS && executable.Arch == evidence.VerifierArch &&
			executable.Digest == evidence.VerifierDigest {
			verifierDigestApproved = true
		}
	}
	if evidence.VerifierName != trust.Verification.Verifier.Name ||
		evidence.VerifierVersion != trust.Verification.Verifier.Version ||
		!verifierDigestApproved || !filepath.IsAbs(evidence.VerifierPath) {
		findings = append(findings, verificationFinding(
			"GDS_BUNDLE_VERIFIER_IDENTITY_MISMATCH",
			"Attestation verifier name, version, absolute path, or digest is outside trust policy.",
		))
	}
	expectedIdentity := digestJSON(map[string]any{
		"owner": evidence.SourceOwner, "repository": evidence.SourceRepository,
		"workflow": evidence.Workflow, "ref": evidence.SourceRef,
		"source_commit": evidence.SourceCommit,
	})
	if expectedIdentity != envelope.ExpectedAttestationIdentityDigest {
		findings = append(findings, verificationFinding(
			"GDS_BUNDLE_ATTESTATION_IDENTITY_DIGEST_MISMATCH",
			"Attestation identity digest does not match the detached release envelope.",
		))
	}
	if trust.Verification.SBOMForExecutables == "required" && envelope.ExecutableFiles > 0 &&
		!evidence.SBOMVerified {
		findings = append(findings, verificationFinding(
			"GDS_BUNDLE_SBOM_NOT_PROVEN", "Required executable SBOM evidence is missing.",
		))
	}
	if trust.Verification.OfflineMaterial == "required" && !evidence.OfflineMaterial {
		findings = append(findings, verificationFinding(
			"GDS_BUNDLE_OFFLINE_EVIDENCE_NOT_PROVEN", "Required offline verification material is missing.",
		))
	}
	rollbackAccepted := false
	rollbackAuthorized := validRollback(rollback, envelope, now)
	if envelope.ReleaseSequence < trust.Release.MinimumReleaseSequence {
		if rollbackAuthorized {
			rollbackAccepted = true
		} else {
			findings = append(findings, verificationFinding(
				"GDS_BUNDLE_SEQUENCE_BELOW_TRUST_FLOOR", "Release sequence is below the trust policy floor.",
			))
		}
	}
	if envelope.ReleaseSequence < state.HighestSequence {
		if rollbackAuthorized {
			rollbackAccepted = true
		} else {
			findings = append(findings, verificationFinding(
				"GDS_BUNDLE_ROLLBACK_BLOCKED", "Bundle sequence is lower than the highest accepted sequence.",
			))
		}
	}
	if accepted := state.AcceptedDigests[envelope.ReleaseSequence]; accepted != "" &&
		accepted != envelope.ArtifactDigest {
		findings = append(findings, verificationFinding(
			"GDS_BUNDLE_SEQUENCE_DIGEST_CONFLICT",
			"A release sequence is already bound to a different artifact digest.",
		))
	}
	status := "accepted"
	if len(findings) != 0 {
		status = "quarantined"
	}
	return VerificationResult{
		Status: status, ReleaseSequence: envelope.ReleaseSequence,
		ArtifactDigest: envelope.ArtifactDigest, Rollback: rollbackAccepted,
	}, findings
}

func validRollback(
	authorization *RollbackAuthorization,
	envelope ReleaseEnvelope,
	now time.Time,
) bool {
	return authorization != nil && authorization.TargetSequence == envelope.ReleaseSequence &&
		authorization.TargetDigest == envelope.ArtifactDigest &&
		strings.TrimSpace(authorization.RolloutID) != "" &&
		strings.TrimSpace(authorization.ScopeDigest) != "" &&
		strings.TrimSpace(authorization.Reason) != "" &&
		strings.TrimSpace(authorization.ApprovalRef) != "" &&
		authorization.ExpiresAt.After(now)
}

func verificationFinding(code, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
