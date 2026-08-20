package projections

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestGenerateRepositoryProjectionDeterministically(t *testing.T) {
	generator, anchor, policy, bundle := controlPlaneInputs(t)
	first, findings := generator.Generate(anchor, policy, bundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	second, findings := generator.Generate(anchor, policy, bundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if first.InputDigest != second.InputDigest || first.OutputDigest != second.OutputDigest {
		t.Fatalf("digest mismatch: first = %#v, second = %#v", first, second)
	}
	if len(first.Files) != len(second.Files) || len(first.Files) != 5 {
		t.Fatalf("files = %#v", first.Files)
	}
	for index := range first.Files {
		if first.Files[index].Path != second.Files[index].Path ||
			first.Files[index].Digest != second.Files[index].Digest ||
			!bytes.Equal(first.Files[index].Content, second.Files[index].Content) {
			t.Fatalf("file %d is not deterministic", index)
		}
	}

	agents := candidateFile(t, first, "AGENTS.md")
	assertBodyDigest(t, agents.Content)
	if bytes.Contains(agents.Content, []byte("2026-07-11T")) {
		t.Fatal("tracked projection contains a wall-clock timestamp")
	}
	// The Claude projection must import the brief, not restate it. This
	// assertion used to require the opposite -- no import, and a "first-class
	// Claude Code projection" banner -- which is why the two files duplicated
	// scope, boundaries, safety, commands and routing almost verbatim. The
	// duplication was test-enforced, not accidental.
	claude := candidateFile(t, first, claudeOutputPath)
	if !bytes.Contains(claude.Content, []byte("@AGENTS.md")) {
		t.Fatalf("Claude projection does not import the repository brief:\n%s", claude.Content)
	}
	for _, duplicated := range []string{
		"## Where to change what", "## How to verify", "Repository `",
	} {
		if bytes.Contains(claude.Content, []byte(duplicated)) {
			t.Fatalf("Claude projection restates %q instead of importing it", duplicated)
		}
	}
	if len(claude.Content) >= len(agents.Content) {
		t.Fatalf("Claude delta (%d bytes) is not smaller than the brief it imports (%d bytes)",
			len(claude.Content), len(agents.Content))
	}
	lock := candidateFile(t, first, bundleLockPath)
	if bytes.Contains(lock.Content, []byte("path: \".gds/bundle.lock.yaml\"")) {
		t.Fatal("bundle lock recursively lists itself")
	}
	workflow := candidateFile(t, first, goCIOutputPath)
	// Derived from the anchor rather than restated. A pinned literal here turns
	// every legitimate `ci.workflow_ref` advance into a test edit, which is how
	// a fixture stops describing the contract and starts describing one commit.
	//
	// The runner was a pinned literal until the fleet migration proved the point:
	// moving the declared runner class failed
	// here for naming a runner the anchor no longer names, which says nothing
	// about the caller contract. What this asserts is that both jobs carry the
	// declared runner and that neither can be generated without one.
	if !bytes.Contains(workflow.Content, []byte("uses: "+anchor.CI.WorkflowRef)) ||
		anchor.CI.Runner == "" ||
		bytes.Count(workflow.Content, []byte("fetch_depth: 0")) != 2 ||
		bytes.Count(workflow.Content, []byte("cache: true")) != 2 ||
		bytes.Count(workflow.Content, []byte("runner: \""+anchor.CI.Runner+"\"")) != 2 ||
		bytes.Contains(workflow.Content, []byte("pull_request_target")) ||
		bytes.Contains(workflow.Content, []byte("secrets: inherit")) {
		t.Fatalf("generated workflow violates the closed caller contract:\n%s", workflow.Content)
	}
}

func TestControlPlaneProjectionMatchesGolden(t *testing.T) {
	generator, anchor, policy, bundle := controlPlaneInputs(t)
	candidate, findings := generator.Generate(anchor, policy, bundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	goldenRoot := filepath.Join(
		projectionRepositoryRoot(t), "tests", "golden", "projections", "control-plane",
	)
	for _, file := range candidate.Files {
		goldenPath := filepath.Join(goldenRoot, filepath.FromSlash(file.Path))
		if os.Getenv("GDS_UPDATE_GOLDEN") == "1" {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatalf("create golden directory %s: %v", filepath.Dir(goldenPath), err)
			}
			if err := os.WriteFile(goldenPath, file.Content, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", file.Path, err)
			}
		}
		expected, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", file.Path, err)
		}
		if !bytes.Equal(expected, file.Content) {
			t.Fatalf("golden projection %s differs from generated output", file.Path)
		}
	}
}

func TestVerifyDetectsMissingManualAndSymlinkDrift(t *testing.T) {
	generator, anchor, policy, bundle := controlPlaneInputs(t)
	candidate, findings := generator.Generate(anchor, policy, bundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	root := t.TempDir()
	if findings := Verify(root, candidate); !hasFinding(findings, "GDS_PROJECTION_MISSING") {
		t.Fatalf("missing findings = %#v", findings)
	}
	writeCandidate(t, root, candidate)
	if findings := Verify(root, candidate); len(findings) != 0 {
		t.Fatalf("clean findings = %#v", findings)
	}

	agentsPath := filepath.Join(root, "AGENTS.md")
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsPath, append(content, []byte("manual edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if findings := Verify(root, candidate); !hasFinding(
		findings, "GDS_PROJECTION_MANUALLY_MODIFIED",
	) {
		t.Fatalf("manual findings = %#v", findings)
	}

	if err := os.Remove(agentsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "CLAUDE.md"), agentsPath); err != nil {
		t.Fatal(err)
	}
	if findings := Verify(root, candidate); !hasFinding(findings, "GDS_PROJECTION_TYPE_INVALID") {
		t.Fatalf("symlink findings = %#v", findings)
	}
}

func TestVerifySeparatesCanonicalStalenessFromManualDrift(t *testing.T) {
	generator, anchor, policy, bundle := controlPlaneInputs(t)
	applied, findings := generator.Generate(anchor, policy, bundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	root := t.TempDir()
	writeCandidate(t, root, applied)

	updatedBundle := bundle
	updatedBundle.SourceCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	updatedBundle.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	updated, findings := generator.Generate(anchor, policy, updatedBundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	findings = Verify(root, updated)
	if !hasFinding(findings, "GDS_PROJECTION_STALE") ||
		hasFinding(findings, "GDS_PROJECTION_MANUALLY_MODIFIED") {
		t.Fatalf("canonical drift was misclassified: %#v", findings)
	}
}

func TestPublicProjectionUsesTypedWhitelist(t *testing.T) {
	generator, anchor, policy, bundle := controlPlaneInputs(t)
	marker := "private-consumer-do-not-persist"
	anchor.Classification.VisibilityContract = "public"
	anchor.Classification.DataClassification = "public"
	// A public repository may not name a self-hosted runner, so the fixture has
	// to be coherent about it: flipping visibility without flipping the runner
	// describes a repository the generator is required to refuse.
	anchor.CI.Runner = "ubuntu-latest"
	anchor.Relationships = []domain.Relationship{{
		Type: "embedded-context-source", Target: marker, Materialization: "ephemeral",
	}}
	anchor.Verification.Commands.Test = []string{"printf '`safe` <!-- still code -->'"}
	candidate, findings := generator.Generate(anchor, policy, bundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	for _, file := range candidate.Files {
		if bytes.Contains(file.Content, []byte(marker)) {
			t.Fatalf("private relationship marker leaked into %s", file.Path)
		}
	}
	agents := string(candidateFile(t, candidate, "AGENTS.md").Content)
	if !strings.Contains(agents, "``printf '`safe` <!-- still code -->'``") {
		t.Fatalf("command was not rendered as a safe code span:\n%s", agents)
	}
}

func TestPublicModuleInstructionsAreMinimalCloneContainedAndLowEntropy(t *testing.T) {
	generator, anchor, policy, bundle := controlPlaneInputs(t)
	anchor.Repository.ID = "repo_01JEXAMPZ0000000000000000N"
	anchor.Repository.Roles = []string{"module"}
	anchor.Classification.VisibilityContract = "public"
	anchor.Classification.DataClassification = "public"
	// A public repository may not name a self-hosted runner, so the fixture has
	// to be coherent about it: flipping visibility without flipping the runner
	// describes a repository the generator is required to refuse.
	anchor.CI.Runner = "ubuntu-latest"
	anchor.Agent.ContextProfile = "public-module"
	anchor.Module = &domain.ModulePolicy{}
	policy.CompiledPolicy.RepositoryID = anchor.Repository.ID

	first, findings := generator.Generate(anchor, policy, bundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	agents := candidateFile(t, first, "AGENTS.md").Content
	claude := candidateFile(t, first, claudeOutputPath).Content
	if string(claude) != "@../AGENTS.md\n" {
		t.Fatalf("Claude bridge = %q", claude)
	}
	for _, forbidden := range [][]byte{
		[]byte("source-commit:"), []byte("input-digest:"), []byte("output-digest:"),
		[]byte("bundle:"), []byte(anchor.Repository.ID), []byte("gds context"),
		[]byte(".gds/repository.yaml"), []byte(".gds/bundle.lock.yaml"),
	} {
		if bytes.Contains(agents, forbidden) || bytes.Contains(claude, forbidden) {
			t.Fatalf("public module instructions contain mutable or non-local data %q:\n%s\n%s",
				forbidden, agents, claude)
		}
	}

	changed := bundle
	changed.Version = "99.99.99"
	changed.SourceCommit = strings.Repeat("b", 40)
	changed.Digest = "sha256:" + strings.Repeat("b", 64)
	policy.CompiledPolicy.BundleVersion = changed.Version
	second, findings := generator.Generate(anchor, policy, changed)
	if len(findings) != 0 {
		t.Fatalf("changed findings = %#v", findings)
	}
	if !bytes.Equal(agents, candidateFile(t, second, "AGENTS.md").Content) ||
		!bytes.Equal(claude, candidateFile(t, second, claudeOutputPath).Content) {
		t.Fatal("public module instructions changed with bundle provenance")
	}
	if bytes.Equal(
		candidateFile(t, first, bundleLockPath).Content,
		candidateFile(t, second, bundleLockPath).Content,
	) {
		t.Fatal("machine-owned bundle metadata did not retain changed provenance")
	}
}

func TestGeneratorRejectsStaleEmbeddedTemplateAgainstNewerSourceCheckout(t *testing.T) {
	generator, _, _, _ := controlPlaneInputs(t)
	root := projectionRepositoryRoot(t)
	if err := generator.VerifyEmbeddedSources(root); err != nil {
		t.Fatalf("current embedded sources: %v", err)
	}
	if len(generator.templates) != 4 {
		t.Fatalf("embedded projection input count = %d", len(generator.templates))
	}
	for path, original := range generator.templates {
		generator.templates[path] = []byte("stale generator\n")
		if err := generator.VerifyEmbeddedSources(root); err == nil ||
			!strings.Contains(err.Error(), path) {
			t.Fatalf("stale embedded source %s error = %v", path, err)
		}
		generator.templates[path] = original
	}
}

func TestDevelopmentSourceLayoutSeparatesPinnedEngineFromPrivateEstate(t *testing.T) {
	estateRoot := t.TempDir()
	engineRoot := filepath.Join(estateRoot, "modules", "github-device-sync")
	if err := os.MkdirAll(engineRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	layout := ResolveDevelopmentSourceLayout(estateRoot)
	if !layout.External || layout.EngineRoot != engineRoot {
		t.Fatalf("layout = %#v", layout)
	}
	want := []string{
		".gds/repository.yaml", ".gitmodules", "estate/exceptions",
		"estate/owners", "modules/github-device-sync", "policies",
	}
	if !reflect.DeepEqual(layout.Paths, want) {
		t.Fatalf("paths = %#v, want %#v", layout.Paths, want)
	}
}

func TestDevelopmentSourceLayoutKeepsStandaloneEngineBoundary(t *testing.T) {
	root := t.TempDir()
	layout := ResolveDevelopmentSourceLayout(root)
	if layout.External || layout.EngineRoot != root {
		t.Fatalf("layout = %#v", layout)
	}
	if !reflect.DeepEqual(layout.Paths, DevelopmentBundleSourcePaths()) {
		t.Fatalf("paths = %#v", layout.Paths)
	}
}

func TestGeneratedAgentsFalsePreservesRepositoryOwnedInstructions(t *testing.T) {
	generator, anchor, policy, bundle := controlPlaneInputs(t)
	anchor.Agent.GeneratedAgents = false
	candidate, findings := generator.Generate(anchor, policy, bundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if len(candidate.Files) != 3 {
		t.Fatalf("files = %#v", candidate.Files)
	}
	for _, file := range candidate.Files {
		if file.Path == "AGENTS.md" || file.Path == claudeOutputPath {
			t.Fatalf("repository-owned instruction became a managed projection: %s", file.Path)
		}
	}
	root := t.TempDir()
	agents := []byte("repository-owned agents\n")
	claude := []byte("repository-owned claude\n")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), agents, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "CLAUDE.md"), claude, 0o644); err != nil {
		t.Fatal(err)
	}
	writeCandidate(t, root, candidate)
	if findings := Verify(root, candidate); len(findings) != 0 {
		t.Fatalf("verify findings = %#v", findings)
	}
	if observed, err := os.ReadFile(filepath.Join(root, "AGENTS.md")); err != nil || !bytes.Equal(observed, agents) {
		t.Fatalf("AGENTS.md changed: %q err=%v", observed, err)
	}
	if observed, err := os.ReadFile(filepath.Join(root, ".claude", "CLAUDE.md")); err != nil || !bytes.Equal(observed, claude) {
		t.Fatalf("CLAUDE.md changed: %q err=%v", observed, err)
	}
}

func TestGoWorkflowValidatorRejectsUnsafeContractChanges(t *testing.T) {
	generator, anchor, policy, bundle := controlPlaneInputs(t)
	candidate, findings := generator.Generate(anchor, policy, bundle)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	workflow := candidateFile(t, candidate, goCIOutputPath)
	for _, mutation := range []struct {
		old string
		new string
	}{
		{"@" + strings.SplitN(anchor.CI.WorkflowRef, "@", 2)[1], "@main"},
		{"permissions: {}", "permissions: write-all"},
		{"pull_request:", "pull_request_target:"},
		{"fetch_depth: 0", "fetch_depth: 1"},
		{"cache: true", "cache: false"},
	} {
		content := bytes.Replace(workflow.Content, []byte(mutation.old), []byte(mutation.new), 1)
		if err := validateGoWorkflowCaller(content, anchor); err == nil {
			t.Fatalf("unsafe workflow mutation %q -> %q was accepted", mutation.old, mutation.new)
		}
	}
}

func TestGeneratorRejectsPrivatePolicyForPublicProjection(t *testing.T) {
	generator, anchor, policy, bundle := controlPlaneInputs(t)
	anchor.Classification.VisibilityContract = "public"
	anchor.Classification.DataClassification = "public"
	// A public repository may not name a self-hosted runner, so the fixture has
	// to be coherent about it: flipping visibility without flipping the runner
	// describes a repository the generator is required to refuse.
	anchor.CI.Runner = "ubuntu-latest"
	policy.Sources[0].Distribution = "private"
	_, findings := generator.Generate(anchor, policy, bundle)
	if !hasFinding(findings, "GDS_PROJECTION_VISIBILITY_VIOLATION") {
		t.Fatalf("findings = %#v", findings)
	}
}

func controlPlaneInputs(
	t *testing.T,
) (*Generator, domain.RepositoryAnchor, compiler.CompiledPolicyDocument, Bundle) {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	root := projectionRepositoryRoot(t)
	anchor, findings := manifest.NewLoader(schemas).LoadRepository(root)
	if len(findings) != 0 {
		t.Fatalf("anchor findings = %#v", findings)
	}
	anchor.Repository.Roles = []string{"control-plane"}
	anchor.Policy.Profiles = []string{"repository-default", "control-plane", "github-device-sync"}
	anchor.Agent.ContextProfile = "control-plane"
	anchor.Agent.GeneratedAgents = true
	anchor.Module = nil
	compiled := compiler.New(schemas).CompileDirectory(
		root, anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		t.Fatalf("policy findings = %#v", compiled.Findings)
	}
	generator, err := New(schemas)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := generator.DevelopmentBundle(
		compiled.Document, "433c46b6923f7dc1efb96713b9ffc9330ca8ba58",
		"sha256:0000000000000000000000000000000000000000000000000000000000000001",
	)
	if err != nil {
		t.Fatal(err)
	}
	return generator, anchor, compiled.Document, bundle
}

func candidateFile(t *testing.T, candidate Candidate, path string) File {
	t.Helper()
	for _, file := range candidate.Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("candidate has no %s", path)
	return File{}
}

func assertBodyDigest(t *testing.T, content []byte) {
	t.Helper()
	parts := bytes.SplitN(content, []byte("-->\n"), 2)
	if len(parts) != 2 {
		t.Fatalf("generated header terminator missing:\n%s", content)
	}
	header := string(parts[0])
	marker := "output-digest: "
	position := strings.Index(header, marker)
	if position < 0 {
		t.Fatalf("output digest missing:\n%s", content)
	}
	value := header[position+len(marker):]
	value = strings.SplitN(value, "\n", 2)[0]
	if value != digestBytes(parts[1]) {
		t.Fatalf("body digest = %s, header = %s", digestBytes(parts[1]), value)
	}
}

func writeCandidate(t *testing.T, root string, candidate Candidate) {
	t.Helper()
	for _, file := range candidate.Files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func hasFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func projectionRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}
