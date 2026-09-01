package compiler

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestCompileControlPlanePolicyDeterministically(t *testing.T) {
	schemas := testSchemas(t)
	root := testRepositoryRoot(t)
	anchor, findings := manifest.NewLoader(schemas).LoadRepository(root)
	if len(findings) != 0 {
		t.Fatalf("anchor findings = %#v", findings)
	}
	anchor.Repository.Roles = []string{"control-plane"}
	anchor.Policy.Profiles = []string{"repository-default", "control-plane", "github-device-sync"}
	anchor.Agent.ContextProfile = "control-plane"
	anchor.Module = nil
	policyCompiler := New(schemas)
	first := policyCompiler.CompileDirectory(root, anchor, DevelopmentBundleVersion)
	second := policyCompiler.CompileDirectory(root, anchor, DevelopmentBundleVersion)
	if len(first.Findings) != 0 || len(second.Findings) != 0 {
		t.Fatalf("first findings = %#v, second findings = %#v", first.Findings, second.Findings)
	}
	firstJSON, err := first.Document.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := second.Document.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("identical policy inputs produced different compiled bytes")
	}
	if first.Document.CompiledPolicy.RepositoryID != anchor.Repository.ID ||
		first.Document.CompiledPolicy.Digest == "" {
		t.Fatalf("compiled metadata = %#v", first.Document.CompiledPolicy)
	}
	if len(first.Document.Sources) != 3 ||
		first.Document.Sources[0].ID != "repository-default" ||
		first.Document.Sources[1].ID != "control-plane" ||
		first.Document.Sources[2].ID != "github-device-sync" {
		t.Fatalf("sources = %#v", first.Document.Sources)
	}
	profiles := effectiveProfiles(t, first.Document)
	if len(profiles) != 2 || profiles[0] != "core" || profiles[1] != "estate-admin" {
		t.Fatalf("profiles = %#v", profiles)
	}
	if _, found := first.Document.Provenance["/effective/agent/profiles/1"]; !found {
		t.Fatalf("profile provenance = %#v", first.Document.Provenance)
	}
	github, ok := first.Document.Effective["github"].(map[string]any)
	if !ok {
		t.Fatalf("github governance = %#v", first.Document.Effective["github"])
	}
	actions, ok := github["actions"].(map[string]any)
	if !ok {
		t.Fatalf("github actions = %#v", github["actions"])
	}
	enabled, ok := actions["enabled"].(map[string]any)
	if !ok || enabled["management"] != "managed" || enabled["value"] != true {
		t.Fatalf("github actions enabled = %#v", actions["enabled"])
	}
	for _, field := range []string{"allowed_actions", "sha_pinning_required", "selected_actions"} {
		setting, ok := actions[field].(map[string]any)
		if !ok || setting["management"] != "observed" {
			t.Fatalf("github actions %s = %#v", field, actions[field])
		}
		if _, found := setting["value"]; found {
			t.Fatalf("observed github actions %s carries desired value: %#v", field, setting)
		}
	}
}

func TestCompileRejectsMissingProfile(t *testing.T) {
	result := New(testSchemas(t)).Compile(
		testAnchor("missing"), map[string]PolicySource{}, DevelopmentBundleVersion,
	)
	assertFinding(t, result.Findings, "GDS_POLICY_PROFILE_MISSING")
}

func TestCompilePublicModulePolicy(t *testing.T) {
	anchor := testAnchor("repository-default", "public-module")
	anchor.Repository.Roles = []string{"module"}
	anchor.Classification.VisibilityContract = "public"
	result := New(testSchemas(t)).CompileDirectory(
		testRepositoryRoot(t), anchor, DevelopmentBundleVersion,
	)
	if len(result.Findings) != 0 {
		t.Fatalf("findings = %#v", result.Findings)
	}
	profiles := effectiveProfiles(t, result.Document)
	if len(profiles) != 2 || profiles[0] != "core" || profiles[1] != "module" {
		t.Fatalf("profiles = %#v", profiles)
	}
	release, ok := result.Document.Effective["release"].(map[string]any)
	if !ok || release["compatibility_contract_required"] != true {
		t.Fatalf("release = %#v", result.Document.Effective["release"])
	}
}

func TestCompileRejectsSelectorMismatch(t *testing.T) {
	source := testSource("module-only", "role", 100, map[string]any{
		"agent": map[string]any{"generated_agents": true},
	})
	source.Match.Roles = []string{"module"}
	result := New(testSchemas(t)).Compile(
		testAnchor("module-only"), map[string]PolicySource{"module-only": source},
		DevelopmentBundleVersion,
	)
	assertFinding(t, result.Findings, "GDS_POLICY_PROFILE_NOT_APPLICABLE")
}

func TestCompileRejectsPrivatePolicyForPublicRepository(t *testing.T) {
	source := testSource("private-policy", "repository", 100, map[string]any{
		"rollout": map[string]any{"mode": "disabled"},
	})
	source.Policy.Distribution = "private"
	anchor := testAnchor("private-policy")
	anchor.Classification.VisibilityContract = "public"
	result := New(testSchemas(t)).Compile(
		anchor, map[string]PolicySource{"private-policy": source},
		DevelopmentBundleVersion,
	)
	assertFinding(t, result.Findings, "GDS_POLICY_VISIBILITY_VIOLATION")
}

func TestCompileRejectsEqualPrioritySameTierConflict(t *testing.T) {
	left := testSource("left", "role", 100, map[string]any{
		"rollout": map[string]any{"mode": "pull-request"},
	})
	right := testSource("right", "role", 100, map[string]any{
		"rollout": map[string]any{"mode": "disabled"},
	})
	result := New(testSchemas(t)).Compile(
		testAnchor("left", "right"), map[string]PolicySource{"left": left, "right": right},
		DevelopmentBundleVersion,
	)
	assertFinding(t, result.Findings, "GDS_POLICY_SAME_TIER_CONFLICT")
}

func TestCompileAllowsHigherPriorityOverride(t *testing.T) {
	left := testSource("left", "role", 100, map[string]any{
		"rollout": map[string]any{"mode": "disabled"},
	})
	right := testSource("right", "role", 200, map[string]any{
		"rollout": map[string]any{"mode": "pull-request"},
	})
	result := New(testSchemas(t)).Compile(
		testAnchor("right", "left"), map[string]PolicySource{"left": left, "right": right},
		DevelopmentBundleVersion,
	)
	if len(result.Findings) != 0 {
		t.Fatalf("findings = %#v", result.Findings)
	}
	rollout := result.Document.Effective["rollout"].(map[string]any)
	if rollout["mode"] != "pull-request" {
		t.Fatalf("rollout = %#v", rollout)
	}
}

func TestCompileRejectsIncompleteSelectedActionsPolicy(t *testing.T) {
	source := testSource("github", "base", 100, map[string]any{
		"github": map[string]any{"actions": map[string]any{
			"allowed_actions": map[string]any{"management": "managed", "value": "selected"},
		}},
	})
	result := New(testSchemas(t)).Compile(
		testAnchor("github"), map[string]PolicySource{"github": source},
		DevelopmentBundleVersion,
	)
	assertFinding(t, result.Findings, "GDS_POLICY_GITHUB_SELECTED_ACTIONS_INCONSISTENT")
}

func TestCompileRejectsSelectedActionsOutsideSelectedMode(t *testing.T) {
	source := testSource("github", "base", 100, map[string]any{
		"github": map[string]any{"actions": map[string]any{
			"allowed_actions": map[string]any{"management": "managed", "value": "all"},
			"selected_actions": map[string]any{
				"management": "managed",
				"value": map[string]any{
					"github_owned_allowed": true,
					"verified_allowed":     false,
					"patterns_allowed":     []any{},
				},
			},
		}},
	})
	result := New(testSchemas(t)).Compile(
		testAnchor("github"), map[string]PolicySource{"github": source},
		DevelopmentBundleVersion,
	)
	assertFinding(t, result.Findings, "GDS_POLICY_GITHUB_SELECTED_ACTIONS_INCONSISTENT")
}

func TestCompileRejectsMonotonicWeakening(t *testing.T) {
	base := testSource("base", "base", 100, map[string]any{
		"security": map[string]any{"external_write_requires_approval": true},
	})
	base.Constraints.Monotonic = []string{"security.external_write_requires_approval"}
	repository := testSource("repository", "repository", 100, map[string]any{
		"security": map[string]any{"external_write_requires_approval": false},
	})
	result := New(testSchemas(t)).Compile(
		testAnchor("repository", "base"),
		map[string]PolicySource{"base": base, "repository": repository},
		DevelopmentBundleVersion,
	)
	assertFinding(t, result.Findings, "GDS_POLICY_MONOTONIC_WEAKENING")
}

func TestCompileAllowsExactScopedUnexpiredException(t *testing.T) {
	base := testSource("base", "base", 100, map[string]any{
		"security": map[string]any{"external_write_requires_approval": true},
	})
	base.Constraints.Monotonic = []string{"security.external_write_requires_approval"}
	repository := testSource("repository", "repository", 100, map[string]any{
		"security": map[string]any{"external_write_requires_approval": false},
	})
	exception := testException(
		"security.external_write_requires_approval", false, "2099-01-01T00:00:00Z",
	)
	result := New(testSchemas(t)).CompileWithExceptions(
		testAnchor("repository", "base"),
		map[string]PolicySource{"base": base, "repository": repository},
		[]PolicyException{exception}, DevelopmentBundleVersion,
		time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
	)
	if len(result.Findings) != 0 {
		t.Fatalf("findings = %#v", result.Findings)
	}
	provenance := result.Document.Provenance["/effective/security/external_write_requires_approval"]
	if provenance.Operation != "exception" || provenance.ExceptionID != exception.Exception.ID ||
		provenance.ApprovalRef != exception.Exception.OwnerApprovalRef ||
		provenance.ExceptionDigest != exception.Digest {
		t.Fatalf("provenance = %#v", provenance)
	}
}

func TestCompileRejectsExceptionForDifferentValue(t *testing.T) {
	base := testSource("base", "base", 100, map[string]any{
		"security": map[string]any{"external_write_requires_approval": true},
	})
	base.Constraints.Monotonic = []string{"security.external_write_requires_approval"}
	repository := testSource("repository", "repository", 100, map[string]any{
		"security": map[string]any{"external_write_requires_approval": false},
	})
	result := New(testSchemas(t)).CompileWithExceptions(
		testAnchor("repository", "base"),
		map[string]PolicySource{"base": base, "repository": repository},
		[]PolicyException{testException(
			"security.external_write_requires_approval", "false", "2099-01-01T00:00:00Z",
		)}, DevelopmentBundleVersion, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
	)
	assertFinding(t, result.Findings, "GDS_POLICY_MONOTONIC_WEAKENING")
}

func TestCompileRejectsExpiredAndUnusedExceptions(t *testing.T) {
	base := testSource("base", "base", 100, map[string]any{
		"security": map[string]any{"external_write_requires_approval": true},
	})
	base.Constraints.Monotonic = []string{"security.external_write_requires_approval"}
	anchor := testAnchor("base")
	compiler := New(testSchemas(t))
	expired := compiler.CompileWithExceptions(
		anchor, map[string]PolicySource{"base": base},
		[]PolicyException{testException(
			"security.external_write_requires_approval", false, "2026-07-10T00:00:00Z",
		)}, DevelopmentBundleVersion, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
	)
	assertFinding(t, expired.Findings, "GDS_POLICY_EXCEPTION_EXPIRED")
	unused := compiler.CompileWithExceptions(
		anchor, map[string]PolicySource{"base": base},
		[]PolicyException{testException(
			"security.external_write_requires_approval", false, "2099-01-01T00:00:00Z",
		)}, DevelopmentBundleVersion, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
	)
	assertFinding(t, unused.Findings, "GDS_POLICY_EXCEPTION_UNUSED")
}

func TestCompileAppliesExplicitProfileAppendAndRemove(t *testing.T) {
	base := testSource("base", "base", 100, map[string]any{
		"agent": map[string]any{
			"profiles": map[string]any{"append": []any{"core", "module"}},
		},
	})
	repository := testSource("repository", "repository", 100, map[string]any{
		"agent": map[string]any{
			"profiles": map[string]any{
				"remove": []any{"module"}, "append": []any{"repository-local"},
			},
		},
	})
	result := New(testSchemas(t)).Compile(
		testAnchor("repository", "base"),
		map[string]PolicySource{"base": base, "repository": repository},
		DevelopmentBundleVersion,
	)
	if len(result.Findings) != 0 {
		t.Fatalf("findings = %#v", result.Findings)
	}
	profiles := effectiveProfiles(t, result.Document)
	if len(profiles) != 2 || profiles[0] != "core" || profiles[1] != "repository-local" {
		t.Fatalf("profiles = %#v", profiles)
	}
	if provenance := result.Document.Provenance["/effective/agent/profiles/1"]; provenance.Source != "repository" || provenance.Operation != "append" {
		t.Fatalf("provenance = %#v", provenance)
	}
}

func TestLoaderRejectsDuplicatePolicyIDs(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"policies/base", "policies/roles"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte(`schema_version: 1
policy:
  id: "duplicate"
  tier: "base"
  priority: 100
  distribution: "public"
apply:
  rollout:
    mode: "disabled"
`)
	for _, path := range []string{"policies/base/one.yaml", "policies/roles/two.yaml"} {
		if err := os.WriteFile(filepath.Join(root, path), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, findings := NewLoader(testSchemas(t)).Load(root)
	assertFinding(t, findings, "GDS_POLICY_DUPLICATE_ID")
}

func testSource(id, tier string, priority int, apply map[string]any) PolicySource {
	return PolicySource{
		SchemaVersion: 1,
		Policy: PolicyMetadata{
			ID: id, Tier: tier, Priority: priority, Distribution: "public",
		},
		Apply: apply, Path: "policies/" + tier + "/" + id + ".yaml",
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func testException(path string, value any, expiry string) PolicyException {
	return PolicyException{
		SchemaVersion: 1,
		Exception: PolicyExceptionMetadata{
			ID:           "exc_01KX7BV07RHD6KRA4Z4J0KCHGR",
			RepositoryID: "repo_01JEXAMPZ0000000000000000C",
			PolicyPath:   path, RequestedValue: value,
			Reason: "Scoped compiler fixture.", OwnerApprovalRef: "approval:fixture-001",
			ExpiresAt: expiry,
		},
		Path:   "estate/exceptions/fixture.yaml",
		Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}

func testAnchor(profiles ...string) domain.RepositoryAnchor {
	return domain.RepositoryAnchor{
		SchemaVersion: 1,
		Repository: domain.RepositoryIdentity{
			ID: "repo_01JEXAMPZ0000000000000000C", Roles: []string{"project"},
			Lifecycle: "active",
		},
		Provider: domain.GitHubLocator{Owner: "example-user"},
		Classification: domain.RepositoryClassification{
			Portfolios:         []string{"portfolio:personal-projects"},
			VisibilityContract: "private",
		},
		Policy: domain.RepositoryPolicy{Profiles: profiles},
	}
}

func effectiveProfiles(t *testing.T, document CompiledPolicyDocument) []string {
	t.Helper()
	agent, ok := document.Effective["agent"].(map[string]any)
	if !ok {
		t.Fatalf("agent = %#v", document.Effective["agent"])
	}
	profiles, ok := agent["profiles"].([]string)
	if !ok {
		t.Fatalf("profiles = %#v", agent["profiles"])
	}
	return profiles
}

func assertFinding(t *testing.T, findings []domain.Finding, code string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("missing finding %s in %#v", code, findings)
}

func testSchemas(t *testing.T) *validation.Set {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	return schemas
}

func testRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

// TestNpmFamilyStrengthensButNeverWeakens covers the vocabulary "allowed"
// gained when the estate-wide npm-family ban collided with the owner's fixed
// decision that every toolchain a project actually uses must work. A role may
// strengthen the base's allowed to forbidden; the reverse is a weakening and
// must be refused exactly like every other monotonic field.
func TestNpmFamilyStrengthensButNeverWeakens(t *testing.T) {
	t.Parallel()
	path := "package_management.npm_family_on_managed_path"
	if isWeakening(path, "allowed", "forbidden") {
		t.Fatal("strengthening allowed -> forbidden was refused")
	}
	if !isWeakening(path, "forbidden", "allowed") {
		t.Fatal("weakening forbidden -> allowed was permitted")
	}
	if !isWeakening(path, "allowed", "unheard-of") {
		t.Fatal("an unknown value did not count as weakening")
	}
}
