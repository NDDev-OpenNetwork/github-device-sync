package deviceevidence

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/freshness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

func TestSignedDeviceEvidenceBindsCentralFreshnessAndInstalledPaused(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	policyDigest, _ := freshness.DefaultPolicy().Digest()
	payload := Payload{SchemaVersion: 1, EvidenceID: "device-evidence:test", DeviceID: "device:test",
		DeviceProfile: "desktop-builds", ObservedAt: now, ExpiresAt: now.Add(6 * time.Hour),
		GDSVersion: "0.4.0", GDSSourceSHA: strings.Repeat("a", 40),
		WorkspaceInventoryDigest: "sha256:" + strings.Repeat("b", 64),
		RepositoryStateDigest:    "sha256:" + strings.Repeat("c", 64),
		ProviderRefreshDigest:    "sha256:" + strings.Repeat("d", 64),
		FindingsDigest:           "sha256:" + strings.Repeat("e", 64), FreshnessPolicyDigest: policyDigest,
		ActorID: "device:observer", Harnesses: []HarnessState{{ID: "zcode", ExecutableVersion: "0.15.2",
			SetupContractDigest: "sha256:" + strings.Repeat("f", 64), State: "installed-paused"}}}
	artifact, err := Sign(payload, "device-key", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier := Verifier{Now: func() time.Time { return now.Add(time.Minute) }, Trust: trust.Verifier{Policy: trust.Policy{
		SchemaVersion: 1, PolicyID: "device-policy", Identities: []trust.Identity{{ActorID: payload.ActorID,
			Roles: []string{"device-evidence"}, Keys: []trust.Key{{Algorithm: trust.Ed25519, KeyID: "device-key",
				PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), ValidFrom: now.Add(-time.Hour),
				ValidUntil: now.Add(24 * time.Hour), Status: "active"}}}}}}}
	assessment, err := verifier.Verify(artifact)
	if err != nil || assessment.State != "fresh" {
		t.Fatalf("assessment=%#v err=%v", assessment, err)
	}
	artifact.Payload.FreshnessPolicyDigest = "sha256:" + strings.Repeat("0", 64)
	artifact, _ = Sign(artifact.Payload, "device-key", privateKey)
	if _, err := verifier.Verify(artifact); err == nil {
		t.Fatal("device evidence with a different freshness policy was accepted")
	}
}
