package releaseconsumer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
)

type verificationRunner struct {
	testing *testing.T
	calls   int
}

func (runner *verificationRunner) Run(
	_ context.Context,
	_ string,
	arguments []string,
	environment []string,
) ([]byte, error) {
	runner.testing.Helper()
	runner.calls++
	if len(arguments) < 3 || arguments[0] != "attestation" || arguments[1] != "verify" {
		runner.testing.Fatalf("unexpected arguments: %#v", arguments)
	}
	for _, variable := range environment {
		if strings.HasPrefix(variable, "GH_TOKEN=") || strings.HasPrefix(variable, "GITHUB_TOKEN=") {
			runner.testing.Fatalf("credential leaked to offline verifier: %s", variable)
		}
		if variable == "HOME="+os.Getenv("HOME") {
			runner.testing.Fatalf("real HOME leaked to offline verifier: %s", variable)
		}
	}
	predicate := argumentValue(arguments, "--predicate-type")
	for _, required := range []string{
		"--repo", "--signer-workflow", "--source-digest", "--source-ref",
		"--bundle", "--custom-trusted-root", "--format",
	} {
		if argumentValue(arguments, required) == "" {
			runner.testing.Fatalf("missing argument value %s", required)
		}
	}
	// The estate builds its releases on its own fleet, so constraining the runner
	// environment rejects its own valid bundles while adding nothing to the
	// identity binding the arguments above already enforce. Reintroducing the
	// flag would make every release un-installable again, silently and only at
	// install time, so it is asserted absent rather than merely not required.
	if containsArgument(arguments, "--deny-self-hosted-runners") {
		runner.testing.Fatalf("runner environment must not be constrained; releases are built on the self-hosted fleet")
	}
	digest, err := fileDigest(arguments[2])
	if err != nil {
		return nil, err
	}
	return json.Marshal([]map[string]any{{
		"verificationResult": map[string]any{
			"statement": map[string]any{
				"predicateType": predicate,
				"subject": []map[string]any{{
					"digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")},
				}},
			},
		},
	}})
}

func TestGHAttestationVerifierUsesExactOfflineEvidence(t *testing.T) {
	release := t.TempDir()
	evidence := t.TempDir()
	for _, name := range []string{
		"gds-bundle-v1.2.3.tar.gz", "release-envelope.json", "manifest.json",
		"sbom.spdx.json", "bundle-trust.yaml",
	} {
		if err := os.WriteFile(filepath.Join(release, name), []byte("content:"+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{ProvenanceBundleName, SBOMBundleName, TrustedRootName} {
		if err := os.WriteFile(filepath.Join(evidence, name), []byte("evidence:"+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	artifact := filepath.Join(release, "gds-bundle-v1.2.3.tar.gz")
	digest, err := fileDigest(artifact)
	if err != nil {
		t.Fatal(err)
	}
	runner := &verificationRunner{testing: t}
	verifier := &GHAttestationVerifier{
		Binary: "/test/gh", Timeout: time.Minute, runner: runner, identityVerified: true,
		Identity:         bundle.TrustVerifier{Name: "github-cli", Version: "2.96.0"},
		ExecutableDigest: "sha256:" + strings.Repeat("a", 64),
		executable:       []byte("approved verifier fixture"),
	}
	verifier.ExecutableDigest = bytesDigest(verifier.executable)
	result, err := verifier.Verify(context.Background(), AttestationRequest{
		ReleaseDirectory: release, EvidenceDirectory: evidence,
		ArtifactName: "gds-bundle-v1.2.3.tar.gz", ArtifactDigest: digest,
		SourceCommit: strings.Repeat("a", 40), SourceRef: "refs/tags/gds-v1.2.3",
		SourceOwner: "example-user", SourceRepository: "github-device-sync",
		Workflow: ".github/workflows/release-bundle.yml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 6 || !result.Verified || !result.SBOMVerified || !result.OfflineMaterial {
		t.Fatalf("calls=%d result=%+v", runner.calls, result)
	}
	if result.VerifierName != "github-cli" || result.VerifierVersion != "2.96.0" ||
		result.VerifierPath != "/test/gh" || result.VerifierDigest != verifier.ExecutableDigest {
		t.Fatalf("verifier identity=%+v", result)
	}
}

func TestNewGHAttestationVerifierRejectsPathSubstitution(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "gh")
	content := []byte("#!/bin/sh\nprintf 'gh version 2.96.0 (fake)\\n'\n")
	if err := os.WriteFile(binary, content, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	identity := bundle.TrustVerifier{
		Name: "github-cli", Version: "2.96.0",
		Executables: []bundle.TrustVerifierExecutable{{
			OS: runtime.GOOS, Arch: runtime.GOARCH,
			Digest: "sha256:" + strings.Repeat("a", 64),
		}},
	}
	if _, err := NewGHAttestationVerifier(identity); err == nil {
		t.Fatal("substituted GitHub CLI outside the approved digest was accepted")
	}
}

func TestNewGHAttestationVerifierAcceptsExactApprovedIdentity(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "gh")
	content := []byte("#!/bin/sh\nprintf 'gh version 2.96.0 (approved fixture)\\n'\n")
	if err := os.WriteFile(binary, content, 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := fileDigest(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	identity := bundle.TrustVerifier{
		Name: "github-cli", Version: "2.96.0",
		Executables: []bundle.TrustVerifierExecutable{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, Digest: digest,
		}},
	}
	verifier, err := NewGHAttestationVerifier(identity)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	if verifier.Binary != resolved || verifier.ExecutableDigest != digest || !verifier.identityVerified {
		t.Fatalf("verifier=%+v", verifier)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bytesDigest(verifier.executable) != digest {
		t.Fatal("verified GitHub CLI snapshot changed with the ambient pathname")
	}
}

func TestValidateGHVerificationRejectsWrongSubject(t *testing.T) {
	raw := []byte(`[{"verificationResult":{"statement":{"predicateType":"https://slsa.dev/provenance/v1","subject":[{"digest":{"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]}}}]`)
	if err := validateGHVerification(raw, provenancePredicate, strings.Repeat("b", 64)); err == nil {
		t.Fatal("wrong attestation subject was accepted")
	}
}

func argumentValue(arguments []string, name string) string {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name {
			return arguments[index+1]
		}
	}
	return ""
}

func containsArgument(arguments []string, expected string) bool {
	for _, value := range arguments {
		if value == expected {
			return true
		}
	}
	return false
}
