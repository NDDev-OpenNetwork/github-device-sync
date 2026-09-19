// Package sessionevidence defines the signed artifact GDS produces when a
// supported agent harness runs in a repository. The artifact binds a session
// identity to the repository's observed Git state at capture time: head,
// branch, upstream position, change counts, changed paths and every Git module
// inside the repository boundary. It records what was observed, not whether the
// session's work was correct.
//
// Evidence is a private artifact: it names repository paths and a session
// identity, so it belongs on the device that produced it, never in the
// repository itself. The signature proves integrity and the signing identity;
// the repository binding is proven by the recorded OIDs, which any later
// verifier can compare against the actual repository.
package sessionevidence

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

const (
	// SignatureDomain separates session evidence signatures from every other
	// signed GDS artifact.
	SignatureDomain = "gds-session-evidence/v1"
	// SignatureRole is the trust role required of a signing key.
	SignatureRole = "session-evidence"
	// SchemaName identifies the embedded validation schema.
	SchemaName = "session-evidence"
	// SchemaVersion is the payload schema version this package emits.
	SchemaVersion = 1
)

var (
	// ErrMalformed reports an artifact that cannot be decoded into the expected
	// shape.
	ErrMalformed = errors.New("session evidence is malformed")
	// ErrDigestMismatch reports an artifact whose payload digest does not match
	// the recorded evidence digest.
	ErrDigestMismatch = errors.New("session evidence digest mismatch")
	// ErrInvalidSignature reports a signature the configured trust policy does
	// not accept.
	ErrInvalidSignature = errors.New("session evidence signature is invalid")
)

// SubmoduleEvidence binds one Git module inside the repository boundary to the
// OID recorded in the parent tree and the OID actually checked out. An
// uninitialized or absent module is still recorded, with no OIDs, so the
// artifact enumerates the whole boundary.
type SubmoduleEvidence struct {
	Path          string `json:"path"`
	GitlinkOID    string `json:"gitlink_oid,omitempty"`
	CurrentOID    string `json:"current_oid,omitempty"`
	WorktreeState string `json:"worktree_state"`
}

// Baseline is the observed Git state of the repository at capture time. Change
// counts and paths describe the working tree, including state that predates the
// session; the artifact does not claim the session produced them.
type Baseline struct {
	HeadOID        string              `json:"head_oid"`
	HeadMode       string              `json:"head_mode"`
	Branch         string              `json:"branch,omitempty"`
	Upstream       string              `json:"upstream,omitempty"`
	UpstreamState  string              `json:"upstream_state"`
	Ahead          int                 `json:"ahead"`
	Behind         int                 `json:"behind"`
	Diverged       bool                `json:"diverged"`
	Staged         int                 `json:"staged"`
	Unstaged       int                 `json:"unstaged"`
	Untracked      int                 `json:"untracked"`
	Conflicted     int                 `json:"conflicted"`
	Classification string              `json:"classification"`
	ChangedPaths   []string            `json:"changed_paths"`
	StatusDigest   string              `json:"status_digest"`
	Submodules     []SubmoduleEvidence `json:"submodules"`
}

// Payload is the signed body of a session evidence artifact.
type Payload struct {
	SchemaVersion  int       `json:"schema_version"`
	EvidenceID     string    `json:"evidence_id"`
	RepositoryID   string    `json:"repository_id"`
	RepositoryName string    `json:"repository_name"`
	DeviceID       string    `json:"device_id"`
	SessionID      string    `json:"session_id"`
	HarnessID      string    `json:"harness_id"`
	HarnessVersion string    `json:"harness_version"`
	ActorID        string    `json:"actor_id"`
	ObservedAt     time.Time `json:"observed_at"`
	GDSVersion     string    `json:"gds_version"`
	Baseline       Baseline  `json:"baseline"`
	PreviousDigest string    `json:"previous_evidence_digest,omitempty"`
}

// Artifact is a payload plus its canonical digest and signature.
type Artifact struct {
	Payload        Payload         `json:"payload"`
	EvidenceDigest string          `json:"evidence_digest"`
	Signature      trust.Signature `json:"signature"`
}

// Assessment reports what verification established.
type Assessment struct {
	EvidenceID     string    `json:"evidence_id"`
	RepositoryID   string    `json:"repository_id"`
	RepositoryName string    `json:"repository_name"`
	SessionID      string    `json:"session_id"`
	ObservedAt     time.Time `json:"observed_at"`
	KeyID          string    `json:"key_id"`
	ActorID        string    `json:"actor_id"`
}

// Verifier checks artifact integrity and signature against a trust policy.
type Verifier struct {
	trust trust.Verifier
}

// NewVerifier constructs a verifier for the given policy.
func NewVerifier(policy trust.Policy) (*Verifier, error) {
	if policy.SchemaVersion != 1 || policy.PolicyID == "" {
		return nil, errors.New("session evidence trust policy is invalid")
	}
	return &Verifier{trust: trust.Verifier{Policy: policy}}, nil
}

// SignPayload canonicalizes the payload and returns the signature input.
func SignPayload(payload Payload) ([]byte, error) {
	return trust.SigningBytes(SignatureDomain, payload)
}

// Sign produces a signed artifact for the payload.
func Sign(payload Payload, keyID string, privateKey ed25519.PrivateKey) (Artifact, error) {
	if keyID == "" || len(privateKey) != ed25519.PrivateKeySize {
		return Artifact{}, errors.New("session evidence signing identity is invalid")
	}
	raw, err := SignPayload(payload)
	if err != nil {
		return Artifact{}, err
	}
	digest, err := DigestPayload(payload)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{
		Payload:        payload,
		EvidenceDigest: digest,
		Signature: trust.Signature{
			Algorithm: trust.Ed25519,
			KeyID:     keyID,
			Value:     base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, raw)),
		},
	}, nil
}

// DigestPayload returns the canonical digest recorded as evidence_digest.
func DigestPayload(payload Payload) (string, error) {
	return canonicaljson.Digest(payload)
}

// Verify validates one artifact end to end: digest, signature and structural
// invariants.
func (verifier *Verifier) Verify(ctx context.Context, artifact Artifact) (Assessment, error) {
	payload := artifact.Payload
	if payload.SchemaVersion != SchemaVersion {
		return Assessment{}, fmt.Errorf("%w: unsupported schema_version %d", ErrMalformed, payload.SchemaVersion)
	}
	if payload.EvidenceID == "" || payload.RepositoryID == "" || payload.DeviceID == "" ||
		payload.SessionID == "" || payload.HarnessID == "" || payload.ActorID == "" {
		return Assessment{}, fmt.Errorf("%w: payload identity fields are incomplete", ErrMalformed)
	}
	if payload.ObservedAt.IsZero() {
		return Assessment{}, fmt.Errorf("%w: observed_at is required", ErrMalformed)
	}
	if payload.Baseline.HeadOID == "" {
		return Assessment{}, fmt.Errorf("%w: baseline head_oid is required", ErrMalformed)
	}
	if payload.Baseline.StatusDigest == "" {
		return Assessment{}, fmt.Errorf("%w: baseline status_digest is required", ErrMalformed)
	}
	seen := make(map[string]struct{}, len(payload.Baseline.Submodules))
	for _, submodule := range payload.Baseline.Submodules {
		if submodule.Path == "" {
			return Assessment{}, fmt.Errorf("%w: submodule path is required", ErrMalformed)
		}
		if _, duplicate := seen[submodule.Path]; duplicate {
			return Assessment{}, fmt.Errorf("%w: duplicate submodule path %q", ErrMalformed, submodule.Path)
		}
		seen[submodule.Path] = struct{}{}
	}
	if !sort.StringsAreSorted(payload.Baseline.ChangedPaths) {
		return Assessment{}, fmt.Errorf("%w: changed_paths must be sorted", ErrMalformed)
	}
	expectedDigest, err := DigestPayload(payload)
	if err != nil {
		return Assessment{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if artifact.EvidenceDigest != expectedDigest {
		return Assessment{}, ErrDigestMismatch
	}
	if err := verifier.trust.Verify(
		SignatureDomain, payload.ActorID, SignatureRole, payload.ObservedAt, payload, artifact.Signature,
	); err != nil {
		return Assessment{}, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	return Assessment{
		EvidenceID:     payload.EvidenceID,
		RepositoryID:   payload.RepositoryID,
		RepositoryName: payload.RepositoryName,
		SessionID:      payload.SessionID,
		ObservedAt:     payload.ObservedAt,
		KeyID:          artifact.Signature.KeyID,
		ActorID:        payload.ActorID,
	}, nil
}
