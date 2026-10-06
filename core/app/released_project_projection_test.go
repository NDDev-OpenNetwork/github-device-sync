package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasedPublicProjectRequiresArtifactWithoutFallingBackToEstate(t *testing.T) {
	root := releasedProjectFixture(t, "public")
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "missing-release.tar.gz")
	_, findings := services.projectionOperationContext(context.Background(), root, ProjectionSourceOptions{
		BundleArchive: archive, ReleaseEnvelope: archive + ".json",
	})
	if len(findings) != 1 || findings[0].Code != "GDS_LOCAL_OPERATION_NOT_PROVEN" ||
		findings[0].Evidence["path"] != archive {
		t.Fatalf("public project did not require its exact released artifact: %#v", findings)
	}
	if _, err := os.Stat(filepath.Join(root, ".gds", "compiled-policy.json")); !os.IsNotExist(err) {
		t.Fatalf("artifact failure wrote a projection: %v", err)
	}
}

func TestReleasedPrivateProjectCannotDetachFromEstatePolicy(t *testing.T) {
	root := releasedProjectFixture(t, "private")
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	_, findings := services.projectionOperationContext(context.Background(), root, ProjectionSourceOptions{
		BundleArchive: "untrusted.tar.gz", ReleaseEnvelope: "untrusted.json",
	})
	if len(findings) != 1 || findings[0].Code != "GDS_PROJECTION_RELEASE_TARGET_INVALID" {
		t.Fatalf("private project could bypass canonical estate policy: %#v", findings)
	}
}

func releasedProjectFixture(t *testing.T, visibility string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor := `schema_version: 1
repository:
  id: repo_01JEXAMPZ00000000000000001
  display_name: example-project
  roles: [project]
  lifecycle: active
provider:
  type: github
  installation: installation:example
  repository_id: 1234
  owner: example-owner
  name: example-project
classification:
  portfolios: [portfolio:example]
  visibility_contract: VISIBILITY
  data_classification: VISIBILITY
policy:
  profiles: [repository-default]
  rollout_ring: standard
git:
  default_branch: main
  integration: pull-request
  branch_model: task-branches
  handoff_pr: preferred
  cleanup: merged-only
verification:
  commands:
    test: [git diff --check]
  required: [test]
agent:
  context_profile: project-default
  generated_agents: false
  serena:
    enabled: false
    provenance_required: false
release:
  mode: none
`
	if err := os.WriteFile(filepath.Join(root, ".gds", "repository.yaml"),
		[]byte(strings.ReplaceAll(anchor, "VISIBILITY", visibility)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=main"},
		{"add", ".gds/repository.yaml"},
		{"-c", "user.name=Example", "-c", "user.email=example@example.test", "-c", "commit.gpgsign=false",
			"commit", "--quiet", "-m", "fixture"},
	} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture Git %v: %v: %s", args, err, output)
		}
	}
	return root
}
