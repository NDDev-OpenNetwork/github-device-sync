package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harnessevidence"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

// evidenceFixture writes a signed harness evidence record plus the trust policy
// that verifies it, mirroring what example-harnesses publishes for a module.
type evidenceFixture struct {
	directory   string
	trustPolicy string
	moduleSHA   string
}

func writeEvidenceFixture(
	t *testing.T,
	estateRoot string,
	harnessID string,
	moduleSHA string,
	now time.Time,
	mutate func(*harnessevidence.Payload),
) evidenceFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bridgeRaw, err := os.ReadFile(filepath.Join(estateRoot, "harnesses", "module-bridge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	profileRaw, err := os.ReadFile(filepath.Join(estateRoot, "harnesses", harnessID, "profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	digestOf := func(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
	payload := harnessevidence.Payload{
		SchemaVersion: 1, EvidenceID: "evidence-" + harnessID, HarnessID: harnessID,
		HarnessRootSHA: strings.Repeat("d", 40), ModuleSHA: moduleSHA,
		ProfileDigest: digestOf(profileRaw), BridgeDigest: digestOf(bridgeRaw),
		ExecutableVersion: "1.2.3",
		Platform:          harnessevidence.Platform{OS: "linux", Architecture: "amd64", DeviceClass: "build"},
		SuiteVersion:      "suite-v1", SuiteCasesDigest: "sha256:" + strings.Repeat("c", 64),
		Result:      "pass",
		GeneratedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
		ActorID: "nddev-harness-release",
	}
	if mutate != nil {
		mutate(&payload)
	}
	evidenceDigest, err := canonicaljson.Digest(payload)
	if err != nil {
		t.Fatal(err)
	}
	signingBytes, err := trust.SigningBytes("gds-harness-runtime-evidence/v1", payload)
	if err != nil {
		t.Fatal(err)
	}
	record := harnessevidence.Record{
		Payload: payload, EvidenceDigest: evidenceDigest,
		Signature: trust.Signature{
			Algorithm: trust.Ed25519, KeyID: "key-1",
			Value: base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, signingBytes)),
		},
	}
	directory := t.TempDir()
	recordRaw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, harnessID+".json"), recordRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	policy := trust.Policy{SchemaVersion: 1, PolicyID: "harness-evidence-test",
		HarnessEvidence: &trust.HarnessEvidencePolicy{
			Producer: trust.ProducerIdentity{
				Repository: "example-org/example-harnesses", Ref: "refs/tags/v1.0.0", Commit: strings.Repeat("d", 40),
			},
			Modules: map[string]string{
				"antigravity": strings.Repeat("5", 40),
				"claude-code": strings.Repeat("1", 40), "codex": strings.Repeat("2", 40),
				"cursor":     strings.Repeat("6", 40),
				"grok-build": moduleSHA, "opencode": strings.Repeat("3", 40), "pi": strings.Repeat("4", 40),
			},
		},
		Identities: []trust.Identity{{
			ActorID: "nddev-harness-release", Roles: []string{"harness-evidence"},
			Keys: []trust.Key{{
				Algorithm: trust.Ed25519, KeyID: "key-1",
				PublicKey: base64.RawURLEncoding.EncodeToString(public),
				ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(7 * 24 * time.Hour),
				Status: "active",
			}},
		}}}
	policyRaw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "trust-policy.json")
	if err := os.WriteFile(policyPath, policyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	return evidenceFixture{directory: directory, trustPolicy: policyPath, moduleSHA: moduleSHA}
}

// A module owned by an active harness may only be released with a signed proof
// about the exact commit being released. This is the whole point of the gate: the
// public repository has no CI of its own, so an unproven release would otherwise
// be indistinguishable from a proven one.
func TestModuleReleaseHarnessEvidenceGate(t *testing.T) {
	root := appTestRepositoryRoot(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	services, err := NewServices(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	// NewServices wires its clock argument only into the inventory compiler and
	// leaves Services.Now as time.Now, so the evidence clock is set explicitly.
	// Freshness is the gate being tested; it must not depend on wall time.
	services.Now = func() time.Time { return now }
	commit := strings.Repeat("a", 40)
	anchor := domain.RepositoryAnchor{
		Module:   &domain.ModulePolicy{PinPolicy: "exact"},
		Provider: domain.GitHubLocator{Type: "github", Owner: "example-org", Name: "grok-setup-system"},
		Release:  domain.ReleasePolicy{Mode: "github-release"},
	}

	t.Run("accepts a signed proof for the exact commit", func(t *testing.T) {
		fixture := writeEvidenceFixture(t, root, "grok-build", commit, now, nil)
		evidence, findings := services.moduleReleaseHarnessEvidence(root, anchor, commit, ModuleReleaseOptions{
			HarnessEvidenceDirectory: fixture.directory, HarnessEvidenceTrustPolicy: fixture.trustPolicy,
		})
		if len(findings) != 0 || evidence == nil {
			t.Fatalf("evidence=%#v findings=%#v", evidence, findings)
		}
		if evidence.HarnessID != "grok-build" || evidence.ModuleSHA != commit || evidence.EvidenceDigest == "" {
			t.Fatalf("evidence did not bind the exact proof: %#v", evidence)
		}
	})

	t.Run("rejects a proof about a different commit", func(t *testing.T) {
		other := strings.Repeat("b", 40)
		fixture := writeEvidenceFixture(t, root, "grok-build", other, now, nil)
		_, findings := services.moduleReleaseHarnessEvidence(root, anchor, commit, ModuleReleaseOptions{
			HarnessEvidenceDirectory: fixture.directory, HarnessEvidenceTrustPolicy: fixture.trustPolicy,
		})
		if len(findings) == 0 {
			t.Fatal("a proof for another commit satisfied the release gate")
		}
	})

	t.Run("rejects a failed lane result", func(t *testing.T) {
		fixture := writeEvidenceFixture(t, root, "grok-build", commit, now,
			func(payload *harnessevidence.Payload) { payload.Result = "fail" })
		_, findings := services.moduleReleaseHarnessEvidence(root, anchor, commit, ModuleReleaseOptions{
			HarnessEvidenceDirectory: fixture.directory, HarnessEvidenceTrustPolicy: fixture.trustPolicy,
		})
		if len(findings) == 0 {
			t.Fatal("a failing harness result satisfied the release gate")
		}
	})

	t.Run("rejects expired evidence", func(t *testing.T) {
		fixture := writeEvidenceFixture(t, root, "grok-build", commit, now,
			func(payload *harnessevidence.Payload) {
				payload.GeneratedAt = now.Add(-48 * time.Hour)
				payload.ExpiresAt = now.Add(-time.Hour)
			})
		_, findings := services.moduleReleaseHarnessEvidence(root, anchor, commit, ModuleReleaseOptions{
			HarnessEvidenceDirectory: fixture.directory, HarnessEvidenceTrustPolicy: fixture.trustPolicy,
		})
		if len(findings) == 0 {
			t.Fatal("expired harness evidence satisfied the release gate")
		}
	})

	t.Run("rejects a tampered payload", func(t *testing.T) {
		fixture := writeEvidenceFixture(t, root, "grok-build", commit, now, nil)
		path := filepath.Join(fixture.directory, "grok-build.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		payload, _ := record["payload"].(map[string]any)
		payload["executable_version"] = "9.9.9"
		tampered, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, tampered, 0o600); err != nil {
			t.Fatal(err)
		}
		_, findings := services.moduleReleaseHarnessEvidence(root, anchor, commit, ModuleReleaseOptions{
			HarnessEvidenceDirectory: fixture.directory, HarnessEvidenceTrustPolicy: fixture.trustPolicy,
		})
		if len(findings) == 0 {
			t.Fatal("tampered harness evidence satisfied the release gate")
		}
	})

	t.Run("fails closed when no evidence is supplied", func(t *testing.T) {
		_, findings := services.moduleReleaseHarnessEvidence(root, anchor, commit, ModuleReleaseOptions{})
		if len(findings) == 0 {
			t.Fatal("a harness-owned module released with no evidence at all")
		}
	})

	t.Run("does not require evidence for a module with no active harness", func(t *testing.T) {
		unmapped := anchor
		unmapped.Provider.Name = "ci-workflows"
		evidence, findings := services.moduleReleaseHarnessEvidence(root, unmapped, commit, ModuleReleaseOptions{})
		if len(findings) != 0 || evidence != nil {
			t.Fatalf("a module with no harness mapping was gated: evidence=%#v findings=%#v", evidence, findings)
		}
	})
}
