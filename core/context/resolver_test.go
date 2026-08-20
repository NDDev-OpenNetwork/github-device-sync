package contextresolver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estateregistry"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestResolveControlPlaneContext(t *testing.T) {
	t.Parallel()
	resolver := newTestResolver(t)
	outcome := resolver.Resolve(context.Background(), repositoryRoot(t))
	if outcome.Context.Repository.ID != "repo_01M0EZ7TB3KNXNSP78Z8M64WXG" {
		t.Fatalf("repository id = %q", outcome.Context.Repository.ID)
	}
	if !outcome.Context.Estate.Registered {
		t.Fatal("control-plane repository should self-register as estate root")
	}
	if outcome.Class != domain.ExitSuccess || !outcome.Context.Policy.BundleLockPresent {
		t.Fatalf("class = %q, policy = %#v", outcome.Class, outcome.Context.Policy)
	}
	if outcome.Context.Policy.Provenance != "verified" {
		t.Fatalf("policy provenance = %q", outcome.Context.Policy.Provenance)
	}
	if len(outcome.Findings) != 0 {
		t.Fatalf("findings = %#v", outcome.Findings)
	}
}

func TestResolveOutsideGitRepository(t *testing.T) {
	t.Parallel()
	resolver := newTestResolver(t)
	outcome := resolver.Resolve(context.Background(), t.TempDir())
	if outcome.Class != domain.ExitNotProven {
		t.Fatalf("class = %q", outcome.Class)
	}
	if !hasFinding(outcome.Findings, "GDS_CONTEXT_NO_REPOSITORY") {
		t.Fatalf("findings = %#v", outcome.Findings)
	}
}

func TestResolveAppliedPolicyRejectsTamperedLockedProjection(t *testing.T) {
	sourceRoot := repositoryRoot(t)
	root := t.TempDir()
	for _, relative := range []string{
		".gds/bundle.lock.yaml",
		".gds/compiled-policy.json",
		".claude/CLAUDE.md",
		".github/workflows/gds-ci.yml",
		"AGENTS.md",
	} {
		source := filepath.Join(sourceRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	resolver := &Resolver{schemas: schemas}
	resolved := Context{Repository: RepositoryContext{ID: "repo_01M0EZ7TB3KNXNSP78Z8M64WXG"}}
	findings := []domain.Finding{}
	resolveAppliedPolicy(resolver, &resolved, &findings, root)
	if len(findings) != 0 || resolved.Policy.Digest == "" {
		t.Fatalf("clean findings = %#v, policy = %#v", findings, resolved.Policy)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved = Context{Repository: RepositoryContext{ID: "repo_01M0EZ7TB3KNXNSP78Z8M64WXG"}}
	findings = nil
	resolveAppliedPolicy(resolver, &resolved, &findings, root)
	if !hasFinding(findings, "GDS_CONTEXT_PROJECTION_DIGEST_MISMATCH") {
		t.Fatalf("tamper findings = %#v", findings)
	}
}

func TestCanonicalPolicyProverRejectsCommittedSelfConsistentReplacement(t *testing.T) {
	sourceRoot := repositoryRoot(t)
	targetRoot := t.TempDir()
	copyContextFixture(t, sourceRoot, targetRoot)
	runContextGit(t, targetRoot, "init", "--quiet")
	runContextGit(t, targetRoot, "config", "user.name", "GDS context test")
	runContextGit(t, targetRoot, "config", "user.email", "context@example.invalid")
	runContextGit(t, targetRoot, "add", "--all")
	runContextGit(t, targetRoot, "commit", "--quiet", "-m", "test: applied policy fixture")

	resolver := newTestResolver(t)
	anchorValue, anchorFindings := resolver.manifests.LoadRepository(targetRoot)
	if len(anchorFindings) != 0 {
		t.Fatal(anchorFindings)
	}
	resolved := Context{Repository: RepositoryContext{ID: anchorValue.Repository.ID}}
	findings := []domain.Finding{}
	document := resolveAppliedPolicy(resolver, &resolved, &findings, targetRoot)
	if document == nil || len(findings) != 0 {
		t.Fatalf("clean applied policy findings = %#v", findings)
	}
	prover := NewCanonicalPolicyProver(
		fixedCommittedSource{
			oid:        document.Bundle.SourceCommit,
			treeDigest: document.Bundle.SourceTreeDigest,
		},
		resolver.prover.compiler,
		resolver.prover.projector,
	)
	if provenanceFindings := prover.Verify(
		context.Background(), targetRoot, sourceRoot, anchorValue, *document,
	); len(provenanceFindings) != 0 {
		t.Fatalf("clean provenance findings = %#v", provenanceFindings)
	}

	agentsPath := filepath.Join(targetRoot, "AGENTS.md")
	agents, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(agents))
	agents = append(agents, '\n')
	newDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(agents))
	if err := os.WriteFile(agentsPath, agents, 0o644); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(targetRoot, ".gds", "bundle.lock.yaml")
	lock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(lock), oldDigest) != 1 {
		t.Fatalf("AGENTS digest occurrence count differs from one: %s", oldDigest)
	}
	lock = []byte(strings.Replace(string(lock), oldDigest, newDigest, 1))
	outputValues := make([]map[string]any, 0, len(document.Projection.Files))
	for _, file := range document.Projection.Files {
		digest := file.Digest
		if file.Path == "AGENTS.md" {
			digest = newDigest
		}
		outputValues = append(outputValues, map[string]any{"path": file.Path, "digest": digest})
	}
	outputRaw, err := json.Marshal(outputValues)
	if err != nil {
		t.Fatal(err)
	}
	newOutputDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(outputRaw))
	if strings.Count(string(lock), document.Projection.OutputDigest) != 1 {
		t.Fatalf("output digest occurrence count differs from one: %s", document.Projection.OutputDigest)
	}
	lock = []byte(strings.Replace(
		string(lock), document.Projection.OutputDigest, newOutputDigest, 1,
	))
	if err := os.WriteFile(lockPath, lock, 0o644); err != nil {
		t.Fatal(err)
	}
	runContextGit(t, targetRoot, "add", "--all")
	runContextGit(t, targetRoot, "commit", "--quiet", "-m", "test: self-consistent replacement")

	resolved = Context{Repository: RepositoryContext{ID: anchorValue.Repository.ID}}
	findings = nil
	document = resolveAppliedPolicy(resolver, &resolved, &findings, targetRoot)
	if document == nil || len(findings) != 0 {
		t.Fatalf("self-consistent lock should pass internal digest checks: %#v", findings)
	}
	provenanceFindings := prover.Verify(
		context.Background(), targetRoot, sourceRoot, anchorValue, *document,
	)
	if !hasFinding(provenanceFindings, "GDS_PROJECTION_STALE") {
		t.Fatalf(
			"canonical reconstruction accepted a committed self-consistent replacement: %#v",
			provenanceFindings,
		)
	}
}

func TestCanonicalPolicyProverFailsClosedWithoutReleaseEvidence(t *testing.T) {
	resolver := newTestResolver(t)
	document := bundleLockDocument{}
	document.Bundle.Channel = "stable"
	document.Bundle.Version = "1.0.0"
	findings := resolver.prover.Verify(
		context.Background(), t.TempDir(), t.TempDir(), domain.RepositoryAnchor{}, document,
	)
	if !hasFinding(findings, "GDS_CONTEXT_POLICY_PROVENANCE_NOT_PROVEN") {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestResolveEstateUsesDeviceLocalRegistration(t *testing.T) {
	root := repositoryRoot(t)
	configHome := t.TempDir()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	anchorEvidence, err := anchor.Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	candidate, findings := estateregistry.NewCandidate(
		"device_01JEXAMPZ00000000000000000",
		"repo_01M0EZ7TB3KNXNSP78Z8M64WXG",
		root,
		anchorEvidence.File.ContentDigest,
		schemas,
	)
	if len(findings) != 0 {
		t.Fatal(findings)
	}
	registrationPath := filepath.Join(configHome, "github-device-sync", estateregistry.FileName)
	if err := os.MkdirAll(filepath.Dir(registrationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registrationPath, candidate.Raw, 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &Resolver{
		manifests: manifest.NewLoader(schemas), schemas: schemas,
		getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
		userHome: func() (string, error) { return t.TempDir(), nil },
	}
	result := Context{Repository: RepositoryContext{ID: "repo_01JEXAMPZ0000000000000000B"}}
	resolveFindings := []domain.Finding{}
	resolveEstate(resolver, &result, &resolveFindings)
	expectedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolveFindings) != 0 || !result.Estate.Registered || result.Estate.Root != expectedRoot {
		t.Fatalf("estate = %#v, findings = %#v", result.Estate, resolveFindings)
	}
}

func TestResolveEstateRejectsRegistrationAnchorDrift(t *testing.T) {
	root := repositoryRoot(t)
	configHome := t.TempDir()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	candidate, findings := estateregistry.NewCandidate(
		"device_01JEXAMPZ00000000000000000",
		"repo_01M0EZ7TB3KNXNSP78Z8M64WXG",
		root,
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		schemas,
	)
	if len(findings) != 0 {
		t.Fatal(findings)
	}
	registrationPath := filepath.Join(configHome, "github-device-sync", estateregistry.FileName)
	if err := os.MkdirAll(filepath.Dir(registrationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registrationPath, candidate.Raw, 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &Resolver{
		manifests: manifest.NewLoader(schemas), schemas: schemas,
		getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
		userHome: func() (string, error) { return t.TempDir(), nil },
	}
	result := Context{Repository: RepositoryContext{ID: "repo_01JEXAMPZ0000000000000000B"}}
	resolveFindings := []domain.Finding{}
	resolveEstate(resolver, &result, &resolveFindings)
	if result.Estate.Registered || !hasFinding(resolveFindings, "GDS_CONTEXT_ESTATE_NOT_REGISTERED") {
		t.Fatalf("estate = %#v, findings = %#v", result.Estate, resolveFindings)
	}
}

func newTestResolver(t *testing.T) *Resolver {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := gitprovider.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	projector, err := projections.New(schemas)
	if err != nil {
		t.Fatal(err)
	}
	prover := NewCanonicalPolicyProver(runner, compiler.New(schemas), projector)
	resolver := NewResolver(runner, manifest.NewLoader(schemas), schemas, prover)
	resolver.getenv = func(string) string { return "" }
	resolver.userHome = func() (string, error) { return t.TempDir(), nil }
	return resolver
}

func hasFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func copyContextFixture(t *testing.T, sourceRoot string, targetRoot string) {
	t.Helper()
	for _, relative := range []string{
		".gds/repository.yaml",
		".gds/bundle.lock.yaml",
		".gds/compiled-policy.json",
		".claude/CLAUDE.md",
		".github/workflows/gds-ci.yml",
		"AGENTS.md",
	} {
		content, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(targetRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runContextGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

type fixedCommittedSource struct {
	oid        string
	treeDigest string
}

func (source fixedCommittedSource) CommittedSourceOID(
	context.Context,
	string,
	[]string,
) (string, error) {
	return source.oid, nil
}

func (source fixedCommittedSource) SourceTreeDigest(
	context.Context,
	string,
	[]string,
) (string, error) {
	return source.treeDigest, nil
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}
