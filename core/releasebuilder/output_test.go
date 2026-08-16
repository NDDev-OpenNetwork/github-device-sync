package releasebuilder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestReleaseOutputRoundTripAndTamperRejection(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	request, source, candidate, sbom, trustRaw := releaseFixture(t, schemas)
	_, files, err := releaseOutputFiles(
		request, source, ExpectedGoVersion, testGitIdentity, candidate, sbom, trustRaw,
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("round-trip", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "release")
		if err := writeOutput(destination, files); err != nil {
			t.Fatal(err)
		}
		verified, err := VerifyDirectory(destination, schemas)
		if err != nil {
			t.Fatal(err)
		}
		if verified.Status != "verified" || verified.ArtifactDigest != candidate.Envelope.ArtifactDigest || len(verified.Files) != 6 {
			t.Fatalf("unexpected verification result: %+v", verified)
		}
	})

	t.Run("tampered-artifact", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "release")
		if err := writeOutput(destination, files); err != nil {
			t.Fatal(err)
		}
		artifact := filepath.Join(destination, "gds-bundle-v1.2.3.tar.gz")
		if err := os.WriteFile(artifact, []byte("tampered"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyDirectory(destination, schemas); err == nil {
			t.Fatal("tampered artifact was accepted")
		}
	})

	t.Run("extra-file", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "release")
		if err := writeOutput(destination, files); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, "extra"), []byte("unexpected"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyDirectory(destination, schemas); err == nil {
			t.Fatal("extra release file was accepted")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "release")
		if err := writeOutput(destination, files); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(destination, releaseSBOMName)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(releaseTrustName, path); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyDirectory(destination, schemas); err == nil {
			t.Fatal("symlinked release file was accepted")
		}
	})
}

func TestReleaseOutputRollsBackAfterFailedPublication(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	request, source, candidate, sbom, trustRaw := releaseFixture(t, schemas)
	_, files, err := releaseOutputFiles(
		request, source, ExpectedGoVersion, testGitIdentity, candidate, sbom, trustRaw,
	)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "release")
	injected := errors.New("post-rename publication failure")
	err = writeOutputWithPostRenameHook(destination, files, func() error { return injected })
	if !errors.Is(err, injected) {
		t.Fatalf("expected injected post-rename failure, got %v", err)
	}
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("release destination survived a failed publication: %v", statErr)
	}
	// A rolled-back destination must not block a subsequent successful build.
	if err := writeOutput(destination, files); err != nil {
		t.Fatalf("retry after rolled-back publication failed: %v", err)
	}
	verified, err := VerifyDirectory(destination, schemas)
	if err != nil {
		t.Fatalf("verify after retry failed: %v", err)
	}
	if verified.Status != "verified" || len(verified.Files) != 6 {
		t.Fatalf("unexpected verification after retry: %+v", verified)
	}
}

func TestRemoveReleaseOutputIfSameRefusesForeignDirectory(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "release")
	if err := os.Mkdir(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(foreign, "keep")
	if err := os.WriteFile(sentinel, []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "unrelated")
	if err := os.Mkdir(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	unrelatedInfo, err := os.Lstat(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	// A destination whose identity no longer matches the staging directory must
	// be reported, never deleted.
	if err := removeReleaseOutputIfSame(foreign, unrelatedInfo); err == nil ||
		!strings.Contains(err.Error(), "release output identity changed") {
		t.Fatalf("expected identity-change guard, got %v", err)
	}
	if _, statErr := os.Lstat(sentinel); statErr != nil {
		t.Fatalf("foreign directory was disturbed despite identity mismatch: %v", statErr)
	}
}

func TestValidateReleaseRef(t *testing.T) {
	accepted := []struct{ ref, version, channel string }{
		{"refs/heads/main", "1.2.3-canary.1", "canary"},
		{"refs/tags/gds-v1.2.3", "1.2.3", "stable"},
		{"refs/tags/gds-v1.2.3", "1.2.3", "frozen"},
	}
	for _, value := range accepted {
		if err := validateReleaseRef(value.ref, value.version, value.channel); err != nil {
			t.Fatalf("accepted ref rejected: %+v: %v", value, err)
		}
	}
	rejected := []struct{ ref, version, channel string }{
		{"refs/heads/main", "1.2.3", "stable"},
		{"refs/heads/feature", "1.2.3-canary.1", "canary"},
		{"refs/tags/gds-v1.2.2", "1.2.3", "stable"},
	}
	for _, value := range rejected {
		if err := validateReleaseRef(value.ref, value.version, value.channel); err == nil {
			t.Fatalf("unsafe ref accepted: %+v", value)
		}
	}
}

func TestReleaseEnvironmentRemovesAmbientGoConfiguration(t *testing.T) {
	t.Setenv("GOFLAGS", "-race")
	t.Setenv("GOEXPERIMENT", "fieldtrack")
	t.Setenv("GOWORK", "/tmp/untrusted.work")
	t.Setenv("GH_TOKEN", "must-not-reach-go")
	home := t.TempDir()
	environment := releaseEnvironment("linux", "arm64", "/tmp/gds-cache", home)
	values := map[string]string{}
	for _, entry := range environment {
		parts := strings.SplitN(entry, "=", 2)
		values[parts[0]] = parts[1]
	}
	if _, found := values["GOFLAGS"]; found {
		t.Fatal("ambient GOFLAGS survived release environment normalization")
	}
	if _, found := values["GH_TOKEN"]; found {
		t.Fatal("ambient credential survived release environment normalization")
	}
	if values["GOOS"] != "linux" || values["GOARCH"] != "arm64" ||
		values["GOWORK"] != "off" || values["CGO_ENABLED"] != "0" ||
		values["GOTOOLCHAIN"] != ExpectedGoVersion || values["HOME"] != home ||
		values["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Fatalf("release environment is incomplete: %#v", values)
	}
}

func releaseFixture(
	t *testing.T,
	schemas *validation.Set,
) (Request, Source, bundle.Candidate, []byte, []byte) {
	t.Helper()
	root := t.TempDir()
	tracked := []string{
		"harnesses/test/profile.yaml",
		"policies/base/test.yaml",
		"skills/registry.yaml",
	}
	for _, relative := range tracked {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture: true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	source := Source{
		Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Ref: "refs/tags/gds-v1.2.3",
		Timestamp: time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC),
	}
	binary := []byte("binary")
	sbom, err := buildSBOM(
		"1.2.3", source,
		[]moduleRecord{{Path: "github.com/NDDev-OpenNetwork/github-device-sync", Version: "1.2.3"}},
		[]binaryRecord{{Path: "bin/linux/amd64/gds", Content: binary, Digest: digestBytes(binary)}},
		testGitIdentity,
	)
	if err != nil {
		t.Fatal(err)
	}
	trustRaw := []byte(`schema_version: 1
trust_domain: "gds-release"
source:
  owner: "example-user"
  repository: "github-device-sync"
  allowed_workflows: [".github/workflows/release-bundle.yml"]
  allowed_refs: ["refs/heads/main", "refs/tags/gds-v*"]
release:
  minimum_release_sequence: 1
  allowed_channels: ["canary", "stable", "frozen"]
verification:
  attestation: "required"
  sbom_for_executables: "required"
  offline_material: "required"
  trusted_root_digest: "sha256:65ca537f6ed8a47fd0e560c421baa1f6c1efb8b25fc200d8c5c02c0e92eb2b9c"
  verifier:
    name: "github-cli"
    version: "2.96.0"
    executables:
      - os: "linux"
        arch: "amd64"
        digest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
`)
	trust := bundle.TrustPolicy{
		SchemaVersion: 1, TrustDomain: "gds-release",
		Source: bundle.TrustSource{
			Owner: "example-user", Repository: "github-device-sync",
			AllowedWorkflows: []string{".github/workflows/release-bundle.yml"},
			AllowedRefs:      []string{"refs/heads/main", "refs/tags/gds-v*"},
		},
		Release: bundle.TrustRelease{MinimumReleaseSequence: 1, AllowedChannels: []string{"canary", "stable", "frozen"}},
		Verification: bundle.TrustVerification{
			Attestation: "required", SBOMForExecutables: "required", OfflineMaterial: "required",
			TrustedRootDigest: "sha256:65ca537f6ed8a47fd0e560c421baa1f6c1efb8b25fc200d8c5c02c0e92eb2b9c",
			Verifier: bundle.TrustVerifier{
				Name: "github-cli", Version: "2.96.0",
				Executables: []bundle.TrustVerifierExecutable{{
					OS: "linux", Arch: "amd64", Digest: "sha256:" + strings.Repeat("f", 64),
				}},
			},
		},
	}
	request := Request{Version: "1.2.3", ReleaseSequence: 7, Channel: "canary", MinimumCLIVersion: "1.2.3"}
	candidate, findings := bundle.Build(root, bundle.BuildOptions{
		BundleVersion: "1.2.3", ReleaseSequence: 7, Channel: "canary",
		SourceCommit: source.Commit, MinimumCLIVersion: "1.2.3",
		Workflow: trust.Source.AllowedWorkflows[0], SourceRef: source.Ref,
		TrackedSources:             tracked,
		HarnessEvidenceProvisional: true,
		AdditionalFiles: []bundle.AdditionalFile{
			{Path: "bin/linux/amd64/gds", Content: binary, Mode: "0755"},
			{Path: "sbom/gds.spdx.json", Content: sbom, Mode: "0644"},
			{Path: "trust/bundle-trust.yaml", Content: trustRaw, Mode: "0644"},
		},
	}, trust, schemas)
	if len(findings) != 0 {
		t.Fatalf("bundle build findings: %+v", findings)
	}
	return request, source, candidate, sbom, trustRaw
}
