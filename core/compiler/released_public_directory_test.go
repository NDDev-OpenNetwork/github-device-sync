package compiler

import (
	"os"
	"path/filepath"
	"testing"
)

func releasedPolicyDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "policies", "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(testRepositoryRoot(t), "policies", "base", "repository-default.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "policies", "base", "repository-default.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReleasedPublicProjectCompilesWithoutEstateOwners(t *testing.T) {
	anchor := testAnchor("repository-default")
	anchor.Classification.VisibilityContract = "public"
	anchor.Repository.Roles = []string{"project"}
	result := New(testSchemas(t)).CompileReleasedPublicDirectory(releasedPolicyDirectory(t), anchor, "1.0.0")
	if len(result.Findings) != 0 {
		t.Fatalf("released public project: %#v", result.Findings)
	}
}

func TestDevelopmentPublicProjectStillRequiresEstateOwners(t *testing.T) {
	anchor := testAnchor("repository-default")
	anchor.Classification.VisibilityContract = "public"
	anchor.Repository.Roles = []string{"project"}
	result := New(testSchemas(t)).CompileDirectory(releasedPolicyDirectory(t), anchor, DevelopmentBundleVersion)
	assertFinding(t, result.Findings, "GDS_POLICY_OWNER_REGISTER_UNAVAILABLE")
}

func TestReleasedDirectoryRejectsPrivateTarget(t *testing.T) {
	anchor := testAnchor("repository-default")
	anchor.Classification.VisibilityContract = "private"
	result := New(testSchemas(t)).CompileReleasedPublicDirectory(releasedPolicyDirectory(t), anchor, "1.0.0")
	assertFinding(t, result.Findings, "GDS_POLICY_RELEASE_TARGET_INVALID")
}

func TestReleasedDirectoryRejectsCorruptExistingOwnerRegister(t *testing.T) {
	root := releasedPolicyDirectory(t)
	directory := filepath.Join(root, "estate", "owners")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "invalid.yaml"), []byte("owner: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	anchor := testAnchor("repository-default")
	anchor.Classification.VisibilityContract = "public"
	result := New(testSchemas(t)).CompileReleasedPublicDirectory(root, anchor, "1.0.0")
	assertFinding(t, result.Findings, "GDS_POLICY_OWNER_REGISTER_UNAVAILABLE")
}

func TestReleasedDirectoryDoesNotInventAnOwnerIdentity(t *testing.T) {
	root := releasedPolicyDirectory(t)
	policy := `schema_version: 1
policy:
  id: owner-selected
  tier: owner
  priority: 100
  distribution: public
match:
  owner: owner:example-declared
apply:
  rollout:
    mode: pull-request
`
	if err := os.WriteFile(filepath.Join(root, "policies", "owner-selected.yaml"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	anchor := testAnchor("repository-default", "owner-selected")
	anchor.Classification.VisibilityContract = "public"
	result := New(testSchemas(t)).CompileReleasedPublicDirectory(root, anchor, "1.0.0")
	assertFinding(t, result.Findings, "GDS_POLICY_PROFILE_NOT_APPLICABLE")
}
