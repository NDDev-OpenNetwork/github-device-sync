// Package bundle builds and verifies immutable portable GDS bundles.
package bundle

import "time"

type TrustPolicy struct {
	SchemaVersion int               `json:"schema_version"`
	TrustDomain   string            `json:"trust_domain"`
	Source        TrustSource       `json:"source"`
	Release       TrustRelease      `json:"release"`
	Verification  TrustVerification `json:"verification"`
}

type TrustSource struct {
	Owner            string   `json:"owner"`
	Repository       string   `json:"repository"`
	AllowedWorkflows []string `json:"allowed_workflows"`
	AllowedRefs      []string `json:"allowed_refs"`
}

type TrustRelease struct {
	MinimumReleaseSequence int      `json:"minimum_release_sequence"`
	AllowedChannels        []string `json:"allowed_channels"`
}

type TrustVerification struct {
	Attestation        string        `json:"attestation"`
	SBOMForExecutables string        `json:"sbom_for_executables"`
	OfflineMaterial    string        `json:"offline_material"`
	TrustedRootDigest  string        `json:"trusted_root_digest"`
	Verifier           TrustVerifier `json:"verifier"`
}

type TrustVerifier struct {
	Name        string                    `json:"name"`
	Version     string                    `json:"version"`
	Executables []TrustVerifierExecutable `json:"executables"`
}

type TrustVerifierExecutable struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Digest string `json:"digest"`
}

type Manifest struct {
	SchemaVersion                 int          `json:"schema_version"`
	BundleVersion                 string       `json:"bundle_version"`
	ReleaseSequence               int          `json:"release_sequence"`
	Channel                       string       `json:"channel"`
	SourceCommit                  string       `json:"source_commit"`
	SourceRef                     string       `json:"source_ref"`
	MinimumCLIVersion             string       `json:"minimum_cli_version"`
	ContentSetDigest              string       `json:"content_set_digest"`
	PolicyDigest                  string       `json:"policy_digest"`
	SkillSetDigest                string       `json:"skill_set_digest"`
	HarnessProfilesDigest         string       `json:"harness_profiles_digest"`
	HarnessEvidenceManifestDigest string       `json:"harness_evidence_manifest_digest,omitempty"`
	HarnessEvidenceProvisional    bool         `json:"harness_evidence_provisional"`
	Files                         []FileRecord `json:"files"`
	SupplyChain                   SupplyChain  `json:"supply_chain"`
}

func (manifest Manifest) ExecutableFileCount() int {
	count := 0
	for _, file := range manifest.Files {
		if file.Mode == "0755" {
			count++
		}
	}
	return count
}

type FileRecord struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
	Mode   string `json:"mode"`
}

type SupplyChain struct {
	AttestationRequired        bool `json:"attestation_required"`
	SBOMRequiredForExecutables bool `json:"sbom_required_for_executables"`
}

type ReleaseEnvelope struct {
	SchemaVersion                     int    `json:"schema_version"`
	BundleVersion                     string `json:"bundle_version"`
	ReleaseSequence                   int    `json:"release_sequence"`
	Channel                           string `json:"channel"`
	SourceCommit                      string `json:"source_commit"`
	SourceRef                         string `json:"source_ref"`
	ExecutableFiles                   int    `json:"executable_files"`
	ManifestDigest                    string `json:"manifest_digest"`
	ArtifactDigest                    string `json:"artifact_digest"`
	ExpectedAttestationIdentityDigest string `json:"expected_attestation_identity_digest"`
}

type BuildOptions struct {
	BundleVersion                 string
	ReleaseSequence               int
	Channel                       string
	SourceCommit                  string
	MinimumCLIVersion             string
	Workflow                      string
	SourceRef                     string
	TrackedSources                []string
	AdditionalFiles               []AdditionalFile
	HarnessEvidenceManifestDigest string
	HarnessEvidenceProvisional    bool
}

type AdditionalFile struct {
	Path        string
	Content     []byte
	Mode        string
	ContentKind string
}

const (
	AdditionalContentText             = "text"
	AdditionalContentOpaqueExecutable = "opaque-executable"
)

type Candidate struct {
	Manifest        Manifest        `json:"manifest"`
	Envelope        ReleaseEnvelope `json:"release_envelope"`
	ArtifactSize    int             `json:"artifact_size"`
	ExecutableFiles int             `json:"executable_files"`
	Artifact        []byte          `json:"-"`
	ManifestBytes   []byte          `json:"-"`
}

type AttestationEvidence struct {
	Verified         bool   `json:"verified"`
	ArtifactDigest   string `json:"artifact_digest"`
	SourceOwner      string `json:"source_owner"`
	SourceRepository string `json:"source_repository"`
	Workflow         string `json:"workflow"`
	SourceRef        string `json:"source_ref"`
	SourceCommit     string `json:"source_commit"`
	SBOMVerified     bool   `json:"sbom_verified"`
	OfflineMaterial  bool   `json:"offline_material"`
	VerifierName     string `json:"verifier_name"`
	VerifierVersion  string `json:"verifier_version"`
	VerifierOS       string `json:"verifier_os"`
	VerifierArch     string `json:"verifier_arch"`
	VerifierPath     string `json:"verifier_path"`
	VerifierDigest   string `json:"verifier_digest"`
}

type AcceptanceState struct {
	HighestSequence  int            `json:"highest_sequence"`
	AcceptedDigests  map[int]string `json:"accepted_digests"`
	AcceptedVersions map[int]string `json:"accepted_versions,omitempty"`
}

type RollbackAuthorization struct {
	RolloutID      string    `json:"rollout_id"`
	TargetSequence int       `json:"target_sequence"`
	TargetDigest   string    `json:"target_digest"`
	ScopeDigest    string    `json:"scope_digest"`
	Reason         string    `json:"reason"`
	ApprovalRef    string    `json:"approval_ref"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type VerificationResult struct {
	Status          string `json:"status"`
	ReleaseSequence int    `json:"release_sequence"`
	ArtifactDigest  string `json:"artifact_digest"`
	Rollback        bool   `json:"rollback"`
}
