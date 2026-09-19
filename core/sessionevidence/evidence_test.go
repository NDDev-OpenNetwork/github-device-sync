package sessionevidence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

func testPayload(now time.Time) Payload {
	return Payload{
		SchemaVersion:  SchemaVersion,
		EvidenceID:     "sev_test",
		RepositoryID:   "repository:example",
		RepositoryName: "example",
		DeviceID:       "device:test",
		SessionID:      "session:test",
		HarnessID:      "codex",
		HarnessVersion: "0.1.0",
		ActorID:        "owner:test",
		ObservedAt:     now,
		GDSVersion:     "0.9.9-dev",
		Baseline: Baseline{
			HeadOID:        strings.Repeat("a", 40),
			HeadMode:       "branch",
			Branch:         "main",
			Upstream:       "origin/main",
			UpstreamState:  "present",
			Classification: "clean",
			ChangedPaths:   []string{"README.md", "core/app/x.go"},
			StatusDigest:   "sha256:" + strings.Repeat("b", 64),
			Submodules: []SubmoduleEvidence{{
				Path: "modules/one", GitlinkOID: strings.Repeat("c", 40),
				CurrentOID: strings.Repeat("c", 40), WorktreeState: "clean",
			}},
		},
	}
}

func testVerifier(t *testing.T, publicKey ed25519.PublicKey, now time.Time) *Verifier {
	t.Helper()
	verifier, err := NewVerifier(trust.Policy{
		SchemaVersion: 1, PolicyID: "test-policy",
		Identities: []trust.Identity{{
			ActorID: "owner:test", Roles: []string{SignatureRole},
			Keys: []trust.Key{{
				Algorithm: trust.Ed25519, KeyID: "session-key",
				PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
				ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(24 * time.Hour), Status: "active",
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func TestSignedSessionEvidenceVerifies(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	artifact, err := Sign(testPayload(now), "session-key", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := testVerifier(t, publicKey, now).Verify(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.EvidenceID != "sev_test" || assessment.RepositoryID != "repository:example" ||
		assessment.SessionID != "session:test" || assessment.KeyID != "session-key" {
		t.Fatalf("assessment=%#v", assessment)
	}
}

func TestTamperedPayloadFailsDigest(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	artifact, err := Sign(testPayload(now), "session-key", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Payload.Baseline.Staged = 7
	if _, err := testVerifier(t, publicKey, now).Verify(context.Background(), artifact); err == nil {
		t.Fatal("tampered payload was accepted")
	}
}

func TestForgedDigestStillFailsSignature(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	artifact, err := Sign(testPayload(now), "session-key", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Payload.Baseline.Staged = 7
	forged, err := DigestPayload(artifact.Payload)
	if err != nil {
		t.Fatal(err)
	}
	artifact.EvidenceDigest = forged
	if _, err := testVerifier(t, publicKey, now).Verify(context.Background(), artifact); err == nil {
		t.Fatal("payload with a recomputed digest but stale signature was accepted")
	}
}

func TestWrongActorOrKeyRejected(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	artifact, err := Sign(testPayload(now), "session-key", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier := testVerifier(t, publicKey, now)
	artifact.Payload.ActorID = "owner:other"
	if _, err := verifier.Verify(context.Background(), artifact); err == nil {
		t.Fatal("artifact naming an untrusted actor was accepted")
	}
	otherPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	verifier = testVerifier(t, otherPublic, now)
	artifact.Payload.ActorID = "owner:test"
	if _, err := verifier.Verify(context.Background(), artifact); err == nil {
		t.Fatal("artifact signed by a different key was accepted")
	}
}

func TestMalformedArtifactsRejected(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	verifier := testVerifier(t, publicKey, now)

	cases := map[string]func(Payload) Payload{
		"empty repository": func(p Payload) Payload { p.RepositoryID = ""; return p },
		"empty head":       func(p Payload) Payload { p.Baseline.HeadOID = ""; return p },
		"unsorted paths":   func(p Payload) Payload { p.Baseline.ChangedPaths = []string{"b", "a"}; return p },
		"duplicate submodule": func(p Payload) Payload {
			p.Baseline.Submodules = append(p.Baseline.Submodules, p.Baseline.Submodules[0])
			return p
		},
		"future schema": func(p Payload) Payload { p.SchemaVersion = 99; return p },
	}
	for name, mutate := range cases {
		payload := mutate(testPayload(now))
		artifact, err := Sign(payload, "session-key", privateKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(context.Background(), artifact); err == nil {
			t.Fatalf("%s: malformed artifact was accepted", name)
		}
	}
}

func TestExpiredKeyRejected(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	artifact, err := Sign(testPayload(now), "session-key", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(trust.Policy{
		SchemaVersion: 1, PolicyID: "test-policy",
		Identities: []trust.Identity{{
			ActorID: "owner:test", Roles: []string{SignatureRole},
			Keys: []trust.Key{{
				Algorithm: trust.Ed25519, KeyID: "session-key",
				PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
				ValidFrom: now.Add(-2 * time.Hour), ValidUntil: now.Add(-time.Hour), Status: "active",
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), artifact); err == nil {
		t.Fatal("artifact signed outside the key validity window was accepted")
	}
}
