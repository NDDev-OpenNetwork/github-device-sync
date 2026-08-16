package releasebuilder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestVerifyTrustedRootRequiresIndependentDigestPin(t *testing.T) {
	t.Parallel()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	trustedRoot := filepath.Join(root, "trusted-root.jsonl")
	trust := filepath.Join(root, "bundle-trust.yaml")
	if err := os.WriteFile(trustedRoot, []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trust, []byte(`schema_version: 1
trust_domain: "gds-release"
source:
  owner: "example-user"
  repository: "github-device-sync"
  allowed_workflows: [".github/workflows/release-bundle.yml"]
  allowed_refs: ["refs/heads/main"]
release:
  minimum_release_sequence: 1
  allowed_channels: ["canary"]
verification:
  attestation: "required"
  sbom_for_executables: "required"
  offline_material: "required"
  trusted_root_digest: "sha256:e80b71cd14d3cbd65f4173abcbfcf01a545dbca32a72d575108b553a648cc96f"
  verifier:
    name: "github-cli"
    version: "2.96.0"
    executables:
      - os: "linux"
        arch: "amd64"
        digest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyTrustedRoot(trustedRoot, trust, schemas)
	if err != nil || verified.Status != "verified" || verified.TrustDomain != "gds-release" {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	if err := os.WriteFile(trustedRoot, []byte("substituted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTrustedRoot(trustedRoot, trust, schemas); err == nil {
		t.Fatal("substituted trusted root was accepted")
	}
}
