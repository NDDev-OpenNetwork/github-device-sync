package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
)

func TestReleasedPublicProjectGeneratesFromVerifiedPortableArtifact(t *testing.T) {
	root := releasedProjectFixture(t, "public")
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	engine := appTestRepositoryRoot(t)
	command := exec.Command("git", "-C", engine, "ls-files", "--",
		"policies", "schemas/v1", "schemas/migrations", "templates/agents",
		"templates/github-actions", "templates/harnesses", "skills/canonical",
		"skills/registry.yaml", "harnesses", "plugins/gds-core",
		"plugins/gds-estate-admin", "plugins/gds-module")
	tracked, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	options := bundle.BuildOptions{
		BundleVersion: "1.0.0", ReleaseSequence: 1,
		SourceCommit: strings.Repeat("a", 40), MinimumCLIVersion: "0.1.0",
		Workflow: ".github/workflows/release-bundle.yml", SourceRef: "refs/heads/main",
		TrackedSources: strings.Fields(string(tracked)),
	}
	trust := bundle.TrustPolicy{
		Source: bundle.TrustSource{Owner: "example-owner", Repository: "example-engine",
			AllowedWorkflows: []string{options.Workflow}, AllowedRefs: []string{options.SourceRef}},
		Release: bundle.TrustRelease{MinimumReleaseSequence: 1},
	}
	candidate, findings := bundle.Build(engine, options, trust, services.Schemas)
	if len(findings) != 0 {
		t.Fatalf("synthetic release build: %#v", findings)
	}
	directory := t.TempDir()
	archive := filepath.Join(directory, "bundle.tar.gz")
	envelope := filepath.Join(directory, "release-envelope.json")
	raw, err := json.Marshal(candidate.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, candidate.Artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envelope, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	source := ProjectionSourceOptions{BundleArchive: archive, ReleaseEnvelope: envelope}
	t.Run("operation-plan-inputs", func(t *testing.T) {
		resolved, findings := services.projectionOperationContext(context.Background(), root, source)
		if len(findings) != 0 || len(resolved.candidate.Files) != 2 {
			t.Fatalf("public project projection: files=%d findings=%#v", len(resolved.candidate.Files), findings)
		}
	})
	t.Run("read-only-generation", func(t *testing.T) {
		envelope := services.GenerateRepository(context.Background(), root, false, source)
		if envelope.ExitCode != 0 {
			t.Fatalf("public project generation: %#v", envelope.Findings)
		}
	})
	if _, err := os.Stat(filepath.Join(root, ".gds", "compiled-policy.json")); !os.IsNotExist(err) {
		t.Fatalf("candidate generation wrote a projection: %v", err)
	}
}

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
