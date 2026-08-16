// Package releaseconsumer verifies immutable GDS release evidence before any
// installation or activation mutation is planned.
package releaseconsumer

import (
	"context"
	"os"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/releasebuilder"
)

const (
	ProvenanceBundleName = "provenance.sigstore.json"
	SBOMBundleName       = "sbom.sigstore.json"
	TrustedRootName      = "trusted-root.jsonl"
)

type Request struct {
	ReleaseDirectory  string
	EvidenceDirectory string
	TrustPolicyPath   string
	ConsumerVersion   string
	TargetOS          string
	TargetArch        string
}

type AttestationRequest struct {
	ReleaseDirectory  string
	EvidenceDirectory string
	ArtifactName      string
	ArtifactDigest    string
	SourceCommit      string
	SourceRef         string
	SourceOwner       string
	SourceRepository  string
	Workflow          string
}

type AttestationVerifier interface {
	Verify(context.Context, AttestationRequest) (bundle.AttestationEvidence, error)
}

type VerifiedRelease struct {
	SchemaVersion     int                                  `json:"schema_version"`
	Status            string                               `json:"status"`
	Directory         releasebuilder.DirectoryVerification `json:"directory"`
	Envelope          bundle.ReleaseEnvelope               `json:"envelope"`
	Manifest          bundle.Manifest                      `json:"manifest"`
	Trust             bundle.TrustPolicy                   `json:"trust"`
	TrustPolicyDigest string                               `json:"trust_policy_digest"`
	EvidenceFiles     []releasebuilder.OutputFile          `json:"evidence_files"`
	Evidence          bundle.AttestationEvidence           `json:"evidence"`
	Policy            bundle.VerificationResult            `json:"policy"`
	VerifiedAt        time.Time                            `json:"verified_at"`
	snapshotRoot      string
	snapshotRequest   Request
}

func (release *VerifiedRelease) Close() error {
	if release == nil || release.snapshotRoot == "" {
		return nil
	}
	root := release.snapshotRoot
	release.snapshotRoot = ""
	release.snapshotRequest = Request{}
	return os.RemoveAll(root)
}

func (release VerifiedRelease) installationRequest(fallback Request) Request {
	if release.snapshotRoot != "" {
		return release.snapshotRequest
	}
	return fallback
}
