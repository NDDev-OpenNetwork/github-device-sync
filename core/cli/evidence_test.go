package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/sessionevidence"
)

func writeSessionKeyPair(t *testing.T) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session-key.pem")
	raw := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, base64.RawURLEncoding.EncodeToString(publicKey)
}

func writeSessionTrustPolicy(t *testing.T, publicKey string) string {
	t.Helper()
	policy := map[string]any{
		"schema_version": 1,
		"policy_id":      "test-session-policy",
		"identities": []map[string]any{{
			"actor_id": "owner:test",
			"roles":    []string{"session-evidence"},
			"keys": []map[string]any{{
				"algorithm":   "ed25519",
				"key_id":      "session-key-2026",
				"public_key":  publicKey,
				"valid_from":  time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
				"valid_until": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
				"status":      "active",
			}},
		}},
	}
	path := filepath.Join(t.TempDir(), "trust-policy.json")
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func recordArgs(client, keyPath, evidenceRoot string) []string {
	return []string{
		"--json", "--cwd", client, "evidence", "record",
		"--device-id", "device:test", "--session-id", "session:test",
		"--harness", "codex", "--harness-version", "0.1.0",
		"--actor-id", "owner:test", "--key-id", "session-key-2026",
		"--private-key", keyPath, "--evidence-root", evidenceRoot,
	}
}

func readArtifact(t *testing.T, path string) sessionevidence.Artifact {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var artifact sessionevidence.Artifact
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestEvidenceRecordAndVerifyRoundTrip(t *testing.T) {
	fixture := sessionFixture(t)
	keyPath, publicKey := writeSessionKeyPair(t)
	policyPath := writeSessionTrustPolicy(t, publicKey)
	evidenceRoot := filepath.Join(t.TempDir(), "session-evidence")

	exitCode, envelope, stderr := executeJSON(t, recordArgs(fixture.client, keyPath, evidenceRoot)...)
	if exitCode != 0 {
		t.Fatalf("record failed: %d %s %#v", exitCode, stderr, envelope.Findings)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected record data: %#v", envelope.Data)
	}
	artifactPath, _ := data["path"].(string)
	if artifactPath == "" || !strings.HasPrefix(artifactPath, evidenceRoot) {
		t.Fatalf("artifact written outside evidence root: %q", artifactPath)
	}
	info, err := os.Stat(artifactPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact permissions: %v %v", info, err)
	}
	artifact := readArtifact(t, artifactPath)
	if artifact.Payload.Baseline.HeadOID != fixture.firstOID {
		t.Fatalf("head oid %q != fixture %q", artifact.Payload.Baseline.HeadOID, fixture.firstOID)
	}
	if artifact.Payload.RepositoryName != "example-repository" &&
		!strings.Contains(artifact.Payload.RepositoryName, "example") {
		t.Fatalf("unexpected repository name %q", artifact.Payload.RepositoryName)
	}

	exitCode, verifyEnvelope, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "evidence", "verify",
		"--file", artifactPath, "--trust-policy", policyPath,
	)
	if exitCode != 0 {
		t.Fatalf("verify failed: %d %s %#v", exitCode, stderr, verifyEnvelope.Findings)
	}

	// A second record chains to the first artifact for the same repository.
	exitCode, second, stderr := executeJSON(t, recordArgs(fixture.client, keyPath, evidenceRoot)...)
	if exitCode != 0 {
		t.Fatalf("second record failed: %d %s", exitCode, stderr)
	}
	secondData, _ := second.Data.(map[string]any)
	if secondData["previous_evidence_digest"] != artifact.EvidenceDigest {
		t.Fatalf("hash chain broken: previous=%v first=%s",
			secondData["previous_evidence_digest"], artifact.EvidenceDigest)
	}
}

func TestEvidenceVerifyRejectsTamperedArtifact(t *testing.T) {
	fixture := sessionFixture(t)
	keyPath, publicKey := writeSessionKeyPair(t)
	policyPath := writeSessionTrustPolicy(t, publicKey)
	evidenceRoot := filepath.Join(t.TempDir(), "session-evidence")

	exitCode, envelope, stderr := executeJSON(t, recordArgs(fixture.client, keyPath, evidenceRoot)...)
	if exitCode != 0 {
		t.Fatalf("record failed: %d %s %#v", exitCode, stderr, envelope.Findings)
	}
	data, _ := envelope.Data.(map[string]any)
	artifactPath, _ := data["path"].(string)
	artifact := readArtifact(t, artifactPath)
	artifact.Payload.Baseline.Untracked = 42
	raw, _ := json.MarshalIndent(artifact, "", "  ")
	if err := os.WriteFile(artifactPath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	exitCode, verifyEnvelope, _ := executeJSON(
		t, "--json", "--cwd", fixture.client, "evidence", "verify",
		"--file", artifactPath, "--trust-policy", policyPath,
	)
	if exitCode == 0 {
		t.Fatalf("tampered artifact verified: %#v", verifyEnvelope.Data)
	}
}
