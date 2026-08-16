// Package deviceevidence defines signed compact operational device truth.
package deviceevidence

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/freshness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

const SignatureDomain = "gds-device-evidence/v1"

type HarnessState struct {
	ID                  string `json:"id"`
	ExecutableVersion   string `json:"executable_version"`
	SetupContractDigest string `json:"setup_contract_digest"`
	State               string `json:"state"`
}

type Payload struct {
	SchemaVersion            int            `json:"schema_version"`
	EvidenceID               string         `json:"evidence_id"`
	DeviceID                 string         `json:"device_id"`
	DeviceProfile            string         `json:"device_profile"`
	ObservedAt               time.Time      `json:"observed_at"`
	ExpiresAt                time.Time      `json:"expires_at"`
	GDSVersion               string         `json:"gds_version"`
	GDSSourceSHA             string         `json:"gds_source_sha"`
	WorkspaceInventoryDigest string         `json:"workspace_inventory_digest"`
	RepositoryStateDigest    string         `json:"repository_state_digest"`
	Harnesses                []HarnessState `json:"harnesses"`
	ProviderRefreshDigest    string         `json:"provider_refresh_digest"`
	FindingsDigest           string         `json:"findings_digest"`
	FreshnessPolicyDigest    string         `json:"freshness_policy_digest"`
	ActorID                  string         `json:"actor_id"`
}

type Artifact struct {
	Payload        Payload         `json:"payload"`
	EvidenceDigest string          `json:"evidence_digest"`
	Signature      trust.Signature `json:"signature"`
}

type Verifier struct {
	Trust trust.Verifier
	Now   func() time.Time
}

func (verifier Verifier) Verify(artifact Artifact) (freshness.Assessment, error) {
	if verifier.Now == nil {
		return freshness.Assessment{}, errors.New("device evidence verifier clock is missing")
	}
	p := artifact.Payload
	digest, err := canonicaljson.Digest(p)
	policyDigest, policyErr := freshness.DefaultPolicy().Digest()
	if err != nil || digest != artifact.EvidenceDigest || p.SchemaVersion != 1 ||
		p.EvidenceID == "" || p.DeviceID == "" || p.DeviceProfile == "" ||
		p.GDSVersion == "" || p.GDSSourceSHA == "" || p.WorkspaceInventoryDigest == "" ||
		p.RepositoryStateDigest == "" || policyErr != nil || p.FreshnessPolicyDigest != policyDigest ||
		!p.ExpiresAt.After(p.ObservedAt) || p.ExpiresAt.Sub(p.ObservedAt) > 6*time.Hour {
		return freshness.Assessment{}, errors.New("device evidence identity or digest is invalid")
	}
	seen := map[string]struct{}{}
	for _, item := range p.Harnesses {
		if item.ID == "" || item.ExecutableVersion == "" || item.SetupContractDigest == "" ||
			(item.State != "proven" && item.State != "drifted" && item.State != "not-proven" && item.State != "installed-paused") {
			return freshness.Assessment{}, errors.New("device harness evidence is invalid")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return freshness.Assessment{}, errors.New("device harness evidence repeats an identity")
		}
		seen[item.ID] = struct{}{}
	}
	if err := verifier.Trust.Verify(SignatureDomain, p.ActorID, "device-evidence", p.ObservedAt, p, artifact.Signature); err != nil {
		return freshness.Assessment{}, err
	}
	assessment, err := freshness.DefaultPolicy().Evaluate(freshness.DeviceInventory, p.ObservedAt, verifier.Now().UTC(), "sqlite:device-evidence", false)
	if err == nil && !verifier.Now().UTC().Before(p.ExpiresAt) {
		assessment.State = "stale"
	}
	return assessment, err
}

func Sign(payload Payload, keyID string, privateKey ed25519.PrivateKey) (Artifact, error) {
	if keyID == "" || len(privateKey) != ed25519.PrivateKeySize {
		return Artifact{}, errors.New("device evidence signing identity is invalid")
	}
	digest, err := canonicaljson.Digest(payload)
	if err != nil {
		return Artifact{}, err
	}
	raw, err := trust.SigningBytes(SignatureDomain, payload)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Payload: payload, EvidenceDigest: digest, Signature: trust.Signature{
		Algorithm: trust.Ed25519, KeyID: keyID,
		Value: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, raw)),
	}}, nil
}
