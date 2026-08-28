package harnessevidence

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

func TestIsolatedEvidenceAndAggregateRequireExactActiveSeven(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	trustVerifier := trust.Verifier{Policy: trust.Policy{SchemaVersion: 1, PolicyID: "harness-test", Identities: []trust.Identity{{
		ActorID: "nddev-harness-release", Roles: []string{"harness-evidence", "harness-evidence-aggregate"}, Keys: []trust.Key{{
			Algorithm: trust.Ed25519, KeyID: "key-1", PublicKey: base64.RawURLEncoding.EncodeToString(public),
			ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(7 * 24 * time.Hour), Status: "active",
		}},
	}}}}
	expected := Expectation{Channel: "stable", HarnessRootSHA: "root-sha", Now: now,
		ModuleSHAs: map[string]string{}, ExecutableVersions: map[string]string{},
		ProfileDigests: map[string]string{}, BridgeDigests: map[string]string{}}
	records := make([]Record, 0, len(ActiveHarnesses))
	entries := make([]ManifestEntry, 0, len(ActiveHarnesses))
	for _, id := range ActiveHarnesses {
		expected.ModuleSHAs[id] = "module-" + id
		expected.ExecutableVersions[id] = "1.2.3"
		expected.ProfileDigests[id] = "sha256:profile-" + id
		expected.BridgeDigests[id] = "sha256:bridge-" + id
		payload := Payload{SchemaVersion: 1, EvidenceID: "evidence-" + id, HarnessID: id,
			HarnessRootSHA: "root-sha", ModuleSHA: "module-" + id,
			ProfileDigest: expected.ProfileDigests[id], BridgeDigest: expected.BridgeDigests[id],
			ExecutableVersion: "1.2.3", Platform: Platform{OS: "linux", Architecture: "amd64", DeviceClass: "build"},
			SuiteVersion: "suite-v1", SuiteCasesDigest: "sha256:cases", Result: "pass",
			GeneratedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour), ActorID: "nddev-harness-release"}
		digest, _ := canonicaljson.Digest(payload)
		record := Record{Payload: payload, EvidenceDigest: digest, Signature: sign(t, private, "gds-harness-runtime-evidence/v1", payload)}
		records = append(records, record)
		entries = append(entries, ManifestEntry{HarnessID: id, EvidenceDigest: digest})
	}
	payload := ManifestPayload{SchemaVersion: 1, ManifestID: "manifest-1", HarnessRootSHA: "root-sha", Channel: "stable",
		GeneratedAt: now.Add(-30 * time.Minute), ExpiresAt: now.Add(24 * time.Hour), ActorID: "nddev-harness-release", Evidence: entries}
	digest, _ := canonicaljson.Digest(payload)
	manifest := Manifest{Payload: payload, ManifestDigest: digest, Signature: sign(t, private, "gds-harness-runtime-manifest/v1", payload)}
	verifier := Verifier{Trust: trustVerifier}
	if err := verifier.VerifyManifest(manifest, records, expected); err != nil {
		t.Fatal(err)
	}
	tampered := records[:len(records)-1]
	if err := verifier.VerifyManifest(manifest, tampered, expected); err == nil {
		t.Fatal("stable manifest accepted a missing isolated harness record")
	}
	wrong := records[0]
	wrong.Payload.ExecutableVersion = "1.2.4"
	if err := verifier.Verify(wrong, expected); err == nil {
		t.Fatal("wrong exact executable version was accepted")
	}
	wrongModule := records[0]
	expected.ModuleSHAs[wrongModule.Payload.HarnessID] = "different-immutable-module"
	if err := verifier.Verify(wrongModule, expected); err == nil {
		t.Fatal("self-asserted module identity was accepted")
	}
	expected.ModuleSHAs[wrongModule.Payload.HarnessID] = wrongModule.Payload.ModuleSHA
	invalidVersion := records[0]
	invalidVersion.Payload.ExecutableVersion = "bad version"
	expected.ExecutableVersions[invalidVersion.Payload.HarnessID] = "bad version"
	invalidVersion.EvidenceDigest, _ = canonicaljson.Digest(invalidVersion.Payload)
	invalidVersion.Signature = sign(t, private, "gds-harness-runtime-evidence/v1", invalidVersion.Payload)
	if err := verifier.Verify(invalidVersion, expected); err == nil {
		t.Fatal("signed unsafe executable version was accepted")
	}
}

func TestAnchoredIdentityRequiresExactImmutableActiveSeven(t *testing.T) {
	commit := strings.Repeat("a", 40)
	policy := trust.Policy{HarnessEvidence: &trust.HarnessEvidencePolicy{
		Producer: trust.ProducerIdentity{
			Repository: "example-org/example-harnesses", Ref: "refs/tags/evidence-v1", Commit: commit,
		},
		Modules: map[string]string{},
	}}
	for _, id := range ActiveHarnesses {
		policy.HarnessEvidence.Modules[id] = commit
	}
	root, modules, err := AnchoredIdentity(policy)
	if err != nil || root != commit || modules["codex"] != commit {
		t.Fatalf("root=%q modules=%v err=%v", root, modules, err)
	}
	delete(policy.HarnessEvidence.Modules, "pi")
	if _, _, err := AnchoredIdentity(policy); err == nil {
		t.Fatal("identity without the exact active-seven mapping was accepted")
	}
	policy.HarnessEvidence.Modules["pi"] = strings.Repeat("z", 40)
	if _, _, err := AnchoredIdentity(policy); err == nil {
		t.Fatal("non-commit module identity was accepted")
	}
}

func sign(t *testing.T, private ed25519.PrivateKey, domain string, payload any) trust.Signature {
	t.Helper()
	raw, err := trust.SigningBytes(domain, payload)
	if err != nil {
		t.Fatal(err)
	}
	return trust.Signature{Algorithm: trust.Ed25519, KeyID: "key-1", Value: base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, raw))}
}
