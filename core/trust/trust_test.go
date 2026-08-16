package trust

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func TestVerifierBindsActorRoleTimeDomainAndPayload(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	verifier := Verifier{Policy: Policy{
		SchemaVersion: 1, PolicyID: "estate-trust",
		Identities: []Identity{{
			ActorID: "owner:example-user", Roles: []string{"estate-approver"},
			Keys: []Key{{
				Algorithm: Ed25519, KeyID: "owner-primary-2026",
				PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
				ValidFrom: issued.Add(-time.Hour), ValidUntil: issued.Add(time.Hour), Status: "active",
			}},
		}},
	}}
	payload := map[string]any{"plan_digest": "sha256:exact", "scope": "estate"}
	signingBytes, err := SigningBytes("gds-approval/v1", payload)
	if err != nil {
		t.Fatal(err)
	}
	signature := Signature{
		Algorithm: Ed25519, KeyID: "owner-primary-2026",
		Value: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, signingBytes)),
	}
	if err := verifier.Verify(
		"gds-approval/v1", "owner:example-user", "estate-approver", issued, payload, signature,
	); err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		name, domain, actor, role string
		issued                    time.Time
		payload                   any
	}{
		{"domain", "gds-device-evidence/v1", "owner:example-user", "estate-approver", issued, payload},
		{"actor", "gds-approval/v1", "owner:other", "estate-approver", issued, payload},
		{"role", "gds-approval/v1", "owner:example-user", "release-approver", issued, payload},
		{"time", "gds-approval/v1", "owner:example-user", "estate-approver", issued.Add(2 * time.Hour), payload},
		{"payload", "gds-approval/v1", "owner:example-user", "estate-approver", issued, map[string]any{"plan_digest": "sha256:changed", "scope": "estate"}},
	}
	for _, item := range invalid {
		item := item
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			if err := verifier.Verify(item.domain, item.actor, item.role, item.issued, item.payload, signature); err == nil {
				t.Fatal("tampered signature binding was accepted")
			}
		})
	}
}
