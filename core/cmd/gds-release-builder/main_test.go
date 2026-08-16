package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunReturnsStructuredArgumentFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := run(context.Background(), []string{"--unknown"}, &stdout, &stderr); exit != 4 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["code"] != "GDS_RELEASE_ARGUMENTS_INVALID" || payload["result"] != "failed" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestRunVerifiesTrustedRootAgainstLocalPolicy(t *testing.T) {
	root := t.TempDir()
	trustedRoot := filepath.Join(root, "trusted-root.jsonl")
	trustPolicy := filepath.Join(root, "bundle-trust.yaml")
	if err := os.WriteFile(trustedRoot, []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPolicy, []byte(`schema_version: 1
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
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := run(context.Background(), []string{
		"--verify-trusted-root", trustedRoot, "--trust-policy", trustPolicy,
	}, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "verified" || payload["trust_domain"] != "gds-release" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestRunReturnsStructuredVerificationFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := run(
		context.Background(), []string{"--verify-directory", t.TempDir()}, &stdout, &stderr,
	); exit != 2 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["code"] != "GDS_RELEASE_VERIFICATION_FAILED" || payload["detail"] == "" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}
