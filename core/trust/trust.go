// Package trust verifies offline GDS signatures against explicit actor and role policy.
package trust

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

const Ed25519 = "ed25519"

type Policy struct {
	SchemaVersion   int                    `json:"schema_version"`
	PolicyID        string                 `json:"policy_id"`
	Identities      []Identity             `json:"identities"`
	HarnessEvidence *HarnessEvidencePolicy `json:"harness_evidence,omitempty"`
}

type HarnessEvidencePolicy struct {
	Producer ProducerIdentity  `json:"producer"`
	Modules  map[string]string `json:"modules"`
}

type ProducerIdentity struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	Commit     string `json:"commit"`
}

type Identity struct {
	ActorID string   `json:"actor_id"`
	Roles   []string `json:"roles"`
	Keys    []Key    `json:"keys"`
}

type Key struct {
	Algorithm  string    `json:"algorithm"`
	KeyID      string    `json:"key_id"`
	PublicKey  string    `json:"public_key"`
	ValidFrom  time.Time `json:"valid_from"`
	ValidUntil time.Time `json:"valid_until"`
	Status     string    `json:"status"`
}

type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Value     string `json:"value"`
}

type Verifier struct {
	Policy Policy
}

// SigningBytes domain-separates each signed GDS contract and canonicalizes its
// JSON representation. Callers must pass a payload with its signature omitted.
func SigningBytes(domain string, payload any) ([]byte, error) {
	if domain == "" {
		return nil, errors.New("signature domain is empty")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode signature payload: %w", err)
	}
	// Avoid capacity arithmetic over attacker-influenced payload lengths. The
	// append operations retain the exact domain-separated byte contract while
	// the runtime performs overflow-safe growth.
	result := append([]byte(nil), domain...)
	result = append(result, '\n')
	result = append(result, raw...)
	return result, nil
}

// Verify proves actor, role, key lifetime, and the detached Ed25519 signature.
func (verifier Verifier) Verify(
	domain string,
	actorID string,
	requiredRole string,
	issuedAt time.Time,
	payload any,
	signature Signature,
) error {
	if verifier.Policy.SchemaVersion != 1 || verifier.Policy.PolicyID == "" ||
		actorID == "" || requiredRole == "" || issuedAt.IsZero() {
		return errors.New("trust verification input is invalid")
	}
	var identity *Identity
	for index := range verifier.Policy.Identities {
		candidate := &verifier.Policy.Identities[index]
		if candidate.ActorID != actorID {
			continue
		}
		if identity != nil {
			return errors.New("trust policy repeats actor identity")
		}
		identity = candidate
	}
	if identity == nil || !slices.Contains(identity.Roles, requiredRole) {
		return errors.New("actor is not trusted for the required role")
	}
	if signature.Algorithm != Ed25519 || signature.KeyID == "" || signature.Value == "" {
		return errors.New("signature identity is invalid")
	}
	var key *Key
	for index := range identity.Keys {
		candidate := &identity.Keys[index]
		if candidate.KeyID != signature.KeyID {
			continue
		}
		if key != nil {
			return errors.New("trust policy repeats key identity")
		}
		key = candidate
	}
	if key == nil || key.Algorithm != Ed25519 || key.Status != "active" ||
		issuedAt.Before(key.ValidFrom) || !issuedAt.Before(key.ValidUntil) {
		return errors.New("signing key is not active at issuance time")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("trusted public key is invalid")
	}
	value, err := base64.RawURLEncoding.DecodeString(signature.Value)
	if err != nil || len(value) != ed25519.SignatureSize {
		return errors.New("detached signature is invalid")
	}
	signingBytes, err := SigningBytes(domain, payload)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), signingBytes, value) {
		return errors.New("detached signature verification failed")
	}
	return nil
}
