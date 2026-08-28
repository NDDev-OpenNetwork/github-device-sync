package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/app"
	approvalcontract "github.com/NDDev-OpenNetwork/github-device-sync/core/approval"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestValidateSchemasJSON(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(t,
		"--json", "--cwd", root, "validate", "schemas",
	)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	if envelope.Command != "gds validate schemas" || envelope.Result != "succeeded" {
		t.Fatalf("envelope = %#v", envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestSourceStatusReportsControlPlaneSourcesCurrent(t *testing.T) {
	// Every control-plane source claim is docs-verified with a pinned content
	// digest; runtime/behavioral proof is delegated to the isolated per-harness
	// setup systems, so the register is release-current with no missing-evidence
	// and no not-proven finding.
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "source", "status", "--as-of", "2026-07-11",
	)
	if exitCode != 0 || envelope.ExitClass != domain.ExitSuccess {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok || data["count"] != float64(59) {
		t.Fatalf("data = %#v", envelope.Data)
	}
	immutableSource := false
	items, itemsOK := data["items"].([]any)
	if !itemsOK {
		t.Fatalf("source items are not typed: %#v", envelope.Data)
	}
	for _, raw := range items {
		item, itemOK := raw.(map[string]any)
		if itemOK && item["id"] == "github-repository-immutable-releases" {
			immutableSource = true
			break
		}
	}
	if !immutableSource {
		t.Fatalf("immutable-release source is absent: %#v", envelope.Data)
	}
	if containsFinding(envelope.Findings, "GDS_SOURCE_CONTENT_DIGEST_NOT_PROVEN") {
		t.Fatalf("no claim should be not-proven once runtime is delegated: %#v", envelope.Findings)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestValidateAllHarnessProfilesDelegatesRuntimeProof(t *testing.T) {
	// Static harness validation stays clean, and --runtime is now also clean: the
	// control plane delegates harness runtime proof to the isolated per-harness
	// setup systems (every profile declares runtime_tests.required=false), so no
	// GDS_HARNESS_RUNTIME_NOT_PROVEN is raised even in all-mode with --runtime.
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "validate", "harnesses", "--harness", "all",
	)
	if exitCode != 0 || len(envelope.Findings) != 0 {
		t.Fatalf("static exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	exitCode, envelope, stderr = executeJSON(
		t, "--json", "--cwd", root, "validate", "harnesses", "--harness", "all", "--runtime",
	)
	if exitCode != 0 || envelope.ExitClass != domain.ExitSuccess || len(envelope.Findings) != 0 {
		t.Fatalf("runtime proof is delegated, expected clean: exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestContextReportsAppliedBundle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(t,
		"--json", "--cwd", root, "context",
	)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	if envelope.ExitClass != domain.ExitSuccess || len(envelope.Findings) != 0 {
		t.Fatalf("envelope = %#v", envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("context data = %#v", envelope.Data)
	}
	capabilities, ok := data["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("context capabilities = %#v", data["capabilities"])
	}
	provider, ok := capabilities["provider_observation"].(map[string]any)
	if !ok || provider["support"] != "implemented" || provider["policy"] != "read-only" {
		t.Fatalf("provider capability = %#v", capabilities["provider_observation"])
	}
	mutations, ok := capabilities["mutations"].(map[string]any)
	if !ok || mutations["support"] != "implemented" || mutations["policy"] != "explicit-approval" {
		t.Fatalf("mutation capability = %#v", capabilities["mutations"])
	}
	assertEnvelopeSchema(t, envelope)
}

func TestCompilePolicyJSON(t *testing.T) {
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "compile", "policy",
	)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("data = %#v", envelope.Data)
	}
	metadata, ok := data["compiled_policy"].(map[string]any)
	if !ok || metadata["repository_id"] != "repo_01M0EZ7TB3KNXNSP78Z8M64WXG" {
		t.Fatalf("compiled policy = %#v", data)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestGenerateRepositoryReturnsCandidateWithoutMutation(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "generate", "repository",
	)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("data = %#v", envelope.Data)
	}
	candidate, ok := data["candidate"].(map[string]any)
	if !ok {
		t.Fatalf("candidate = %#v", data)
	}
	files, ok := candidate["files"].([]any)
	if !ok || len(files) != 3 {
		t.Fatalf("candidate files = %#v", candidate["files"])
	}
	if envelope.Mutation.Attempted || envelope.Mutation.Completed {
		t.Fatalf("mutation = %#v", envelope.Mutation)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestGenerateRepositoryUsesTrustedEstateBundleProvenance(t *testing.T) {
	target := t.TempDir()
	runSessionGit(t, target, "init", "-q", "-b", "main")
	runSessionGit(t, target, "config", "user.name", "Projection Target")
	runSessionGit(t, target, "config", "user.email", "projection@example.invalid")
	if err := os.Mkdir(filepath.Join(target, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	anchor = []byte(strings.Replace(string(anchor), "generated_agents: true", "generated_agents: false", 1))
	if err := os.WriteFile(filepath.Join(target, ".gds", "repository.yaml"), anchor, 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, target, "add", ".gds/repository.yaml")
	runSessionGit(t, target, "commit", "-qm", "add repository anchor")
	targetHead := runSessionGit(t, target, "rev-parse", "HEAD")
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))

	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", target, "generate", "repository",
	)
	if exitCode != 0 || envelope.Mutation.Attempted {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	data, _ := envelope.Data.(map[string]any)
	bundle, _ := data["bundle"].(map[string]any)
	candidate, _ := data["candidate"].(map[string]any)
	files, _ := candidate["files"].([]any)
	if bundle["source_commit"] == "" || bundle["source_commit"] == targetHead || len(files) != 2 {
		t.Fatalf("bundle=%#v candidate=%#v target_head=%s", bundle, candidate, targetHead)
	}
}

func TestGenerateRepositoryConsumesVerifiedReleasedBundle(t *testing.T) {
	source := repositoryRoot(t)
	tracked := strings.Fields(runSessionGit(t, source, "ls-files"))
	sourceCommit := runSessionGit(t, source, "rev-parse", "HEAD")
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	candidate, findings := bundle.Build(source, bundle.BuildOptions{
		BundleVersion: "9.0.0", ReleaseSequence: 900, Channel: "canary",
		SourceCommit: sourceCommit, MinimumCLIVersion: "9.0.0",
		Workflow: ".github/workflows/release-bundle.yml", SourceRef: "refs/heads/main",
		TrackedSources: tracked, HarnessEvidenceProvisional: true,
	}, bundle.TrustPolicy{
		SchemaVersion: 1, TrustDomain: "gds-release",
		Source: bundle.TrustSource{Owner: "NDDev-OpenNetwork", Repository: "github-device-sync",
			AllowedWorkflows: []string{".github/workflows/release-bundle.yml"}, AllowedRefs: []string{"refs/heads/main"}},
		Release: bundle.TrustRelease{MinimumReleaseSequence: 1, AllowedChannels: []string{"canary"}},
	}, schemas)
	if len(findings) != 0 {
		t.Fatalf("build findings: %#v", findings)
	}
	target := t.TempDir()
	runSessionGit(t, target, "init", "-q", "-b", "main")
	runSessionGit(t, target, "config", "user.name", "Released Projection")
	runSessionGit(t, target, "config", "user.email", "projection@example.invalid")
	if err := os.Mkdir(filepath.Join(target, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.ReadFile(filepath.Join(source, ".gds", "repository.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	anchor = []byte(strings.Replace(
		string(anchor), "    - \"github-device-sync\"\n", "    - \"github-actions\"\n", 1,
	))
	if err := os.WriteFile(filepath.Join(target, ".gds", "repository.yaml"), anchor, 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, target, "add", ".gds/repository.yaml")
	runSessionGit(t, target, "commit", "-qm", "add anchor")
	archive := filepath.Join(t.TempDir(), "bundle.tar.gz")
	envelope := filepath.Join(t.TempDir(), "release-envelope.json")
	if err := os.WriteFile(archive, candidate.Artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	envelopeRaw, _ := json.Marshal(candidate.Envelope)
	if err := os.WriteFile(envelope, envelopeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	exitCode, result, stderr := executeJSON(t, "--json", "--cwd", target,
		"generate", "repository", "--bundle-archive", archive, "--release-envelope", envelope)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%q result=%#v", exitCode, stderr, result)
	}
	data := result.Data.(map[string]any)
	released := data["bundle"].(map[string]any)
	if released["version"] != "9.0.0" || released["release_sequence"] != float64(900) ||
		released["channel"] != "canary" || released["digest"] != candidate.Envelope.ArtifactDigest {
		t.Fatalf("released bundle identity = %#v", released)
	}
}

func TestGenerateRepositoryRejectsPartialReleasedBundleIdentity(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, result, _ := executeJSON(t, "--json", "--cwd", root,
		"generate", "repository", "--bundle-archive", filepath.Join(root, "go.mod"))
	if exitCode != 2 || !containsFinding(result.Findings, "GDS_PROJECTION_RELEASE_INPUT_INCOMPLETE") {
		t.Fatalf("exit=%d result=%#v", exitCode, result)
	}
}

func TestStatusDoesNotChangeGitIndex(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := repositoryRoot(t)
	indexPath := repositoryGitIndexPath(t, root)
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	exitCode, envelope, stderr := executeJSON(t,
		"--json", "--cwd", root, "status",
	)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("git index content changed during read-only status")
	}
	assertEnvelopeSchema(t, envelope)
}

func TestInvalidCommandReturnsStructuredInputError(t *testing.T) {
	exitCode, envelope, stderr := executeJSON(t, "--json", "unknown-command")
	if exitCode != 4 {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr)
	}
	if envelope.ExitClass != domain.ExitInput ||
		!containsFinding(envelope.Findings, "GDS_CLI_INPUT_INVALID") {
		t.Fatalf("envelope = %#v", envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestInvalidTimeoutReturnsInputEnvelope(t *testing.T) {
	exitCode, envelope, _ := executeJSON(t, "--json", "--timeout", "0s", "context")
	if exitCode != 4 || !containsFinding(envelope.Findings, "GDS_TIMEOUT_INVALID") {
		t.Fatalf("exit = %d, envelope = %#v", exitCode, envelope)
	}
}

func TestInvalidDiscoveryConcurrencyReturnsInputEnvelope(t *testing.T) {
	exitCode, envelope, _ := executeJSON(
		t, "--json", "discover", "--concurrency", "0",
	)
	if exitCode != 4 ||
		!containsFinding(envelope.Findings, "GDS_DISCOVERY_OPTIONS_INVALID") {
		t.Fatalf("exit = %d, envelope = %#v", exitCode, envelope)
	}
}

func TestValidatePlanIsReadOnly(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "tests", "fixtures", "schemas", "v1", "valid-plan.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "validate", "plan", "--file", path,
	)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) || envelope.Mutation.Attempted {
		t.Fatalf("plan validation mutated input: %#v", envelope.Mutation)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestRolloutPlanIsDeterministicAndReadOnly(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-rollout-request.yaml",
	)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	exitCode, first, stderr := executeJSON(
		t, "--json", "rollout", "plan", "--file", path,
	)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, first)
	}
	_, second, _ := executeJSON(t, "--json", "rollout", "plan", "--file", path)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	firstData, ok := first.Data.(map[string]any)
	if !ok || firstData["target_count"] != float64(2) {
		t.Fatalf("rollout data = %#v", first.Data)
	}
	secondData, ok := second.Data.(map[string]any)
	if !ok || firstData["plan_digest"] != secondData["plan_digest"] {
		t.Fatalf("rollout plan is not deterministic: first=%#v second=%#v", first.Data, second.Data)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) || first.Mutation.Attempted {
		t.Fatalf("rollout planning mutated input: %#v", first.Mutation)
	}
	assertEnvelopeSchema(t, first)
}

func TestStateInspectDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.db")
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "state", "inspect", "--path", path,
	)
	if exitCode != 3 || !containsFinding(envelope.Findings, "GDS_STATE_NOT_INITIALIZED") {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state inspect created missing database: %v", err)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestStateInspectUsesQueryOnlyMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state.db")
	store, err := state.Initialize(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "state", "inspect", "--path", path,
	)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("state data = %#v", envelope.Data)
	}
	info, ok := data["info"].(map[string]any)
	if !ok || info["query_only"] != true {
		t.Fatalf("state inspection was not query-only: %#v", data)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestStateInitializeRequiresExactPlanApprovalAndVerifiesEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-state", "state.db")
	exitCode, planned, stderr := executeJSON(
		t, "--json", "state", "initialize", "--path", path, "--plan",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("planning created state: %v", err)
	}
	data, ok := planned.Data.(map[string]any)
	if !ok {
		t.Fatalf("plan data=%#v", planned.Data)
	}
	plan, ok := data["plan"].(map[string]any)
	if !ok {
		t.Fatalf("plan=%#v", data["plan"])
	}
	digest, _ := plan["plan_digest"].(string)
	planID, _ := plan["plan_id"].(string)
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("plan digest=%q", digest)
	}
	exitCode, rejected, _ := executeJSON(
		t, "--json", "state", "initialize", "--path", path, "--apply", digest,
	)
	if exitCode != 6 || !containsFinding(rejected.Findings, "GDS_SIGNED_APPROVAL_REQUIRED") {
		t.Fatalf("unapproved apply exit=%d envelope=%#v", exitCode, rejected)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unapproved apply created state: %v", err)
	}
	approvalPath := signStateLifecycleTestApproval(t, plan)
	exitCode, applied, stderr := executeJSON(
		t, "--json", "state", "initialize", "--path", path,
		"--apply", digest, "--approval-ref", approvalPath, "--enable", planID,
	)
	if exitCode != 0 || !applied.Mutation.Attempted || !applied.Mutation.Completed {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "state", "initialize", "--path", path, "--verify", digest,
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
	assertEnvelopeSchema(t, planned)
	assertEnvelopeSchema(t, applied)
	assertEnvelopeSchema(t, verified)
}

func signStateLifecycleTestApproval(t *testing.T, plan map[string]any) string {
	t.Helper()
	now := time.Now().UTC()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	actorID, keyID := "owner:state-test", "state-test-key"
	scopeDigest, err := canonicaljson.Digest(struct {
		Action          string `json:"action"`
		StatePath       string `json:"state_path"`
		ExpectedState   string `json:"expected_state"`
		ExpectedVersion int    `json:"expected_version"`
		ExpectedDigest  string `json:"expected_digest,omitempty"`
		TargetVersion   int    `json:"target_version"`
	}{plan["action"].(string), plan["state_path"].(string), plan["expected_state"].(string),
		int(plan["expected_version"].(float64)), stringValue(plan["expected_digest"]), int(plan["target_version"].(float64))})
	if err != nil {
		t.Fatal(err)
	}
	approvalID, _ := identity.New("approval", now, nil)
	record := approvalcontract.Record{SchemaVersion: 1, ApprovalID: approvalID, PlanID: plan["plan_id"].(string),
		PlanDigest: plan["plan_digest"].(string), ActorID: actorID, ActorType: "owner", IssuedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Hour), ApprovalClass: "state-lifecycle", ScopeDigest: scopeDigest}
	raw, _ := trust.SigningBytes(approvalcontract.SignatureDomain, record.Payload())
	record.Signature = trust.Signature{Algorithm: trust.Ed25519, KeyID: keyID,
		Value: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, raw))}
	policy := trust.Policy{SchemaVersion: 1, PolicyID: "state-test-policy", Identities: []trust.Identity{{ActorID: actorID,
		Roles: []string{"mutation-approver"}, Keys: []trust.Key{{Algorithm: trust.Ed25519, KeyID: keyID,
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(2 * time.Hour), Status: "active"}}}}}
	directory := t.TempDir()
	approvalPath, policyPath := filepath.Join(directory, "approval.json"), filepath.Join(directory, "trust.json")
	approvalRaw, _ := json.Marshal(record)
	policyRaw, _ := json.Marshal(policy)
	if err := os.WriteFile(approvalPath, approvalRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDS_TRUST_POLICY_FILE", policyPath)
	return approvalPath
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func TestOperationInspectVerifiesDurableJournalReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state.db")
	store, err := state.Initialize(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	plan, err := operations.NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGV", now, now.Add(15*time.Minute),
		operations.PlanInput{
			Operation: "fixture-operation",
			Actor:     operations.Actor{Type: "agent-session", SessionID: "test-session"},
			Preconditions: []operations.Precondition{{
				RepositoryID:   "repo_01JEXAMPZ0000000000000000C",
				HeadOID:        "0123456789abcdef0123456789abcdef01234567",
				ManifestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				PolicyDigest:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			}},
			Steps: []operations.Step{{
				StepID: "fixture-step", RepositoryID: "repo_01JEXAMPZ0000000000000000C",
				Action: "fixture-action", RequiresApproval: true,
				Compensation: operations.Compensation{Mode: "manual"},
			}},
			ApprovalClass: "fixture-approval",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := plan.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutPlan(ctx, state.PlanRecord{
		PlanID: plan.PlanID, Operation: plan.Operation, PlanDigest: plan.PlanDigest,
		Body: body, Status: "planned", CreatedAt: plan.CreatedAt,
		ExpiresAt: plan.ExpiresAt, InsertedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	operationID := "op_01KX7BV07RHD6KRA4Z4J0KCHGV"
	idempotencyKey, err := operations.StepIdempotencyKey(plan, plan.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartOperation(ctx, state.OperationRecord{
		OperationID: operationID, PlanID: plan.PlanID, Operation: plan.Operation,
		Status: "applying", Actor: json.RawMessage(`{"type":"agent-session"}`),
		StartedAt: now,
	}, []state.StepRecord{{
		OperationID: operationID, StepID: "fixture-step",
		RepositoryID:   "repo_01JEXAMPZ0000000000000000C",
		Action:         "fixture-action",
		IdempotencyKey: idempotencyKey,
		Sequence:       0, Status: "pending",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionStep(
		ctx, operationID, "fixture-step", "pending", "blocked", now, nil, nil,
		"fixture failure",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOperation(
		ctx, operationID, "applying", "blocked", now,
		map[string]any{"code": "GDS_FIXTURE_BLOCKED"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPlan(ctx, plan.PlanID, "planned", "failed"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	exitCode, envelope, stderr := executeJSON(
		t, "--json", "operation", "inspect", operationID, "--state-path", path,
	)
	if exitCode != 0 || envelope.Mutation.Attempted || envelope.OperationID != operationID {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok || data["journal_integrity"] != "pass" {
		t.Fatalf("operation report=%#v", envelope.Data)
	}
	recovery, ok := data["recovery"].(map[string]any)
	if !ok || recovery["classification"] != "replan-required" {
		t.Fatalf("recovery=%#v", data["recovery"])
	}
	assertEnvelopeSchema(t, envelope)
}

func TestRecoverOperationRequiresDeadOwnerExactPlanApprovalAndVerify(t *testing.T) {
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	repository := repositoryRoot(t)
	stateRoot := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateRoot, "state.db")
	store, err := state.Initialize(context.Background(), statePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	originalPlan, err := operations.NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGY", now.Add(-10*time.Minute), now.Add(time.Hour),
		operations.PlanInput{
			Operation: "fixture-operation",
			Actor:     operations.Actor{Type: "agent-session", SessionID: "interrupted-session"},
			Preconditions: []operations.Precondition{{
				RepositoryID:   "repo_01M0EZ7TB3KNXNSP78Z8M64WXG",
				HeadOID:        "0123456789abcdef0123456789abcdef01234567",
				ManifestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				PolicyDigest:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			}},
			Steps: []operations.Step{{
				StepID: "fixture-step", RepositoryID: "repo_01M0EZ7TB3KNXNSP78Z8M64WXG",
				Action: "fixture-action", RequiresApproval: true,
				Compensation: operations.Compensation{Mode: "manual"},
			}},
			ApprovalClass: "fixture-approval",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := originalPlan.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutPlan(ctx, state.PlanRecord{
		PlanID: originalPlan.PlanID, Operation: originalPlan.Operation,
		PlanDigest: originalPlan.PlanDigest, Body: body, Status: "planned",
		CreatedAt: originalPlan.CreatedAt, ExpiresAt: originalPlan.ExpiresAt,
		InsertedAt: now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionPlan(ctx, originalPlan.PlanID, "planned", "approved"); err != nil {
		t.Fatal(err)
	}
	originalOperationID := "op_01KX7BV07RHD6KRA4Z4J0KCHGY"
	idempotencyKey, err := operations.StepIdempotencyKey(originalPlan, originalPlan.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartOperation(ctx, state.OperationRecord{
		OperationID: originalOperationID, PlanID: originalPlan.PlanID,
		Operation: originalPlan.Operation, Status: "applying",
		Actor:     json.RawMessage(`{"type":"agent-session","session_id":"interrupted-session"}`),
		StartedAt: now.Add(-10 * time.Minute),
	}, []state.StepRecord{{
		OperationID: originalOperationID, StepID: "fixture-step",
		RepositoryID: "repo_01M0EZ7TB3KNXNSP78Z8M64WXG", Action: "fixture-action",
		IdempotencyKey: idempotencyKey, Sequence: 0, Status: "pending",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLock(ctx, state.Lock{
		Scope: "repository", ScopeID: "repo_01M0EZ7TB3KNXNSP78Z8M64WXG",
		LockID: "lock_01KX7BV07RHD6KRA4Z4J0KCHGY", OperationID: originalOperationID,
		DeviceID: "device_01JEXAMPZ00000000000000000", SessionID: "interrupted-session", PID: 2147483647,
		AcquiredAt: now.Add(-10 * time.Minute), LeaseExpiresAt: now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	common := []string{
		"--json", "--cwd", repository, "recover", "operation", originalOperationID,
		"--state-path", statePath, "--device-id", "device_01JEXAMPZ00000000000000000",
		"--session-id", "recovery-session",
	}
	exitCode, planned, stderr := executeJSON(t, append(common, "--plan")...)
	if exitCode != 0 {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	data, ok := planned.Data.(map[string]any)
	if !ok {
		t.Fatalf("plan data=%#v", planned.Data)
	}
	decision, _ := data["decision"].(map[string]any)
	plan, _ := data["plan"].(map[string]any)
	planID, _ := plan["plan_id"].(string)
	if decision["classification"] != "safe-abort" || planID == "" {
		t.Fatalf("decision=%#v plan=%#v", decision, plan)
	}
	exitCode, rejected, _ := executeJSON(t, append(common, "--apply", planID)...)
	if exitCode != 6 || !containsFinding(rejected.Findings, "GDS_SIGNED_APPROVAL_REQUIRED") {
		t.Fatalf("unapproved recovery exit=%d envelope=%#v", exitCode, rejected)
	}
	exitCode, applied, stderr := executeJSON(
		t, append(common, "--apply", planID, "--approval-ref", "approval:owner:recovery")...,
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	recoveryOperationID := applied.OperationID
	exitCode, verified, stderr := executeJSON(
		t, append(common, "--verify", recoveryOperationID)...,
	)
	if exitCode != 0 || verified.OperationID != recoveryOperationID {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
	readOnly, err := state.OpenReadOnly(context.Background(), statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	recovered, err := readOnly.RecoverySnapshot(context.Background(), originalOperationID)
	if err != nil || recovered.OperationStatus != "failed" || len(recovered.Locks) != 0 {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
}

func TestGitTopologyIsReadOnlyAndStructured(t *testing.T) {
	root := repositoryRoot(t)
	indexPath := repositoryGitIndexPath(t, root)
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "git", "topology",
	)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, envelope = %#v", exitCode, stderr, envelope)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) || envelope.Mutation.Attempted {
		t.Fatalf("topology inspection mutated repository: %#v", envelope.Mutation)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("topology data = %#v", envelope.Data)
	}
	if _, ok := data["remotes"].([]any); !ok {
		t.Fatalf("topology remotes = %#v", data)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestRootHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(context.Background(), nil, bytes.NewReader(nil), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Agent-first repository estate control plane")) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestGitIndependentCommandsDoNotRequireGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := Execute(
		context.Background(), []string{"--version"}, bytes.NewReader(nil), &stdout, &stderr,
	); exitCode != 0 || !strings.Contains(stdout.String(), Version) {
		t.Fatalf("version exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}

	exitCode, envelope, stderrText := executeJSON(t, "--json", "identity", "new", "repo")
	if exitCode != 0 || envelope.ExitClass != domain.ExitSuccess {
		t.Fatalf("identity exit=%d stderr=%q envelope=%#v", exitCode, stderrText, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok || !strings.HasPrefix(data["id"].(string), "repo_") {
		t.Fatalf("identity data=%#v", envelope.Data)
	}
}

func executeJSON(t *testing.T, args ...string) (int, domain.Envelope, string) {
	t.Helper()
	args = signLegacyTestApproval(t, args)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(
		context.Background(), args, bytes.NewReader(nil), &stdout, &stderr,
	)
	var envelope domain.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode stdout as envelope: %v\nstdout: %s\nstderr: %s", err, stdout.Bytes(), stderr.Bytes())
	}
	return exitCode, envelope, stderr.String()
}

func executeJSONWithServices(
	t *testing.T, services *app.Services, args ...string,
) (int, domain.Envelope, string) {
	t.Helper()
	args = signLegacyTestApproval(t, args)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := executeWithServices(
		context.Background(), args, bytes.NewReader(nil), &stdout, &stderr, services,
	)
	var envelope domain.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode stdout as envelope: %v\nstdout: %s\nstderr: %s", err, stdout.Bytes(), stderr.Bytes())
	}
	return exitCode, envelope, stderr.String()
}

// signLegacyTestApproval upgrades pre-signature CLI fixtures to the production
// approval contract. It deliberately creates no engine bypass: Apply reads and
// verifies the resulting exact-plan record through the normal trust path.
func signLegacyTestApproval(t *testing.T, args []string) []string {
	t.Helper()
	approvalIndex, statePath, planID, deviceID, sessionID := -1, "", "", "", ""
	for index, value := range args {
		switch value {
		case "--approval-ref":
			if index+1 < len(args) {
				approvalIndex = index + 1
			}
		case "--state-path":
			if index+1 < len(args) {
				statePath = args[index+1]
			}
		case "--apply":
			if index+1 < len(args) {
				planID = args[index+1]
			}
		case "--device-id":
			if index+1 < len(args) {
				deviceID = args[index+1]
			}
		case "--session-id":
			if index+1 < len(args) {
				sessionID = args[index+1]
			}
		}
	}
	if approvalIndex < 0 || statePath == "" || !identity.Valid("plan", planID) {
		return args
	}
	if info, err := os.Stat(args[approvalIndex]); err == nil && info.Mode().IsRegular() {
		return args
	}
	store, err := state.Open(context.Background(), statePath)
	if err != nil {
		t.Fatalf("open plan state for signed approval: %v", err)
	}
	record, err := store.GetPlan(context.Background(), planID)
	_ = store.Close()
	if err != nil {
		t.Fatalf("load plan for signed approval: %v", err)
	}
	var plan operations.Plan
	if err := json.Unmarshal(record.Body, &plan); err != nil {
		t.Fatalf("decode plan for signed approval: %v", err)
	}
	scopeDigest, err := operations.ApprovalScopeDigest(plan)
	if err != nil {
		t.Fatalf("digest approval scope: %v", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate approval key: %v", err)
	}
	now := time.Now().UTC()
	actorID, keyID := "owner:cli-test", "cli-test-key"
	approvalID, err := identity.New("approval", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	approval := approvalcontract.Record{
		SchemaVersion: 1, ApprovalID: approvalID, PlanID: plan.PlanID,
		PlanDigest: plan.PlanDigest, ActorID: actorID, ActorType: "owner",
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		ApprovalClass: plan.ApprovalClass, ScopeDigest: scopeDigest,
		ExternalReference: args[approvalIndex],
	}
	signingBytes, err := trust.SigningBytes(approvalcontract.SignatureDomain, approval.Payload())
	if err != nil {
		t.Fatalf("encode signed approval: %v", err)
	}
	approval.Signature = trust.Signature{Algorithm: trust.Ed25519, KeyID: keyID,
		Value: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, signingBytes))}
	policy := trust.Policy{SchemaVersion: 1, PolicyID: "cli-test-policy", Identities: []trust.Identity{{
		ActorID: actorID, Roles: []string{"mutation-approver"}, Keys: []trust.Key{{
			Algorithm: trust.Ed25519, KeyID: keyID,
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
			ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(2 * time.Hour), Status: "active",
		}},
	}}}
	directory := t.TempDir()
	approvalPath, policyPath := filepath.Join(directory, "approval.json"), filepath.Join(directory, "trust.json")
	approvalRaw, _ := json.Marshal(approval)
	policyRaw, _ := json.Marshal(policy)
	if err := os.WriteFile(approvalPath, approvalRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDS_TRUST_POLICY_FILE", policyPath)
	enablementStore, err := state.Open(context.Background(), statePath)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	engine := operations.NewDefaultEngine(enablementStore, schemas, nil, nil, deviceID, sessionID)
	if _, err := engine.EnableSigned(context.Background(), planID, approval); err != nil {
		_ = enablementStore.Close()
		t.Fatalf("enable signed CLI test plan: %v", err)
	}
	if err := enablementStore.Close(); err != nil {
		t.Fatal(err)
	}
	result := append([]string(nil), args...)
	result[approvalIndex] = approvalPath
	return result
}

func assertEnvelopeSchema(t *testing.T, envelope domain.Envelope) {
	t.Helper()
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	value, err := serialization.Decode("envelope.json", raw)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	if findings := schemas.Validate("operation-result", value, "envelope"); len(findings) != 0 {
		t.Fatalf("envelope findings = %#v", findings)
	}
}

func containsFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func testEstateRoot(t *testing.T) string {
	t.Helper()
	sourceRoot := repositoryRoot(t)
	estateRoot := t.TempDir()
	for _, directory := range []string{"estate", "policies"} {
		if err := copyCLITestTree(sourceRoot, estateRoot, directory); err != nil {
			t.Fatal(err)
		}
	}
	engineRoot := filepath.Join(estateRoot, "modules", "github-device-sync")
	for _, directory := range []string{
		"harnesses", "plugins", "schemas", "skills", "templates", "core/harness", "core/skills",
	} {
		if err := copyCLITestTree(sourceRoot, engineRoot, directory); err != nil {
			t.Fatal(err)
		}
	}
	engineAnchor := filepath.Join(engineRoot, ".gds", "repository.yaml")
	if err := os.MkdirAll(filepath.Dir(engineAnchor), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyCLITestFile(filepath.Join(sourceRoot, ".gds", "repository.yaml"), engineAnchor); err != nil {
		t.Fatal(err)
	}
	commitCLITestRepository(t, engineRoot, "test: public engine fixture")
	raw, err := os.ReadFile(filepath.Join(sourceRoot, ".gds", "repository.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	text = strings.Replace(text,
		"  roles:\n    - \"project\"\n    - \"module\"\n",
		"  roles:\n    - \"control-plane\"\n", 1)
	text = strings.Replace(text, "    - \"public-module\"\n", "    - \"control-plane\"\n", 1)
	text = strings.Replace(text, "  context_profile: \"project-default\"\n", "  context_profile: \"control-plane\"\n", 1)
	start := strings.Index(text, "\nmodule:\n")
	end := strings.Index(text, "\nrelease:\n")
	if start < 0 || end <= start {
		t.Fatal("public engine anchor has no removable module section")
	}
	text = text[:start] + text[end:]
	anchorPath := filepath.Join(estateRoot, ".gds", "repository.yaml")
	if err := os.MkdirAll(filepath.Dir(anchorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(anchorPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	commitCLITestRepository(t, estateRoot, "test: external estate fixture")
	return estateRoot
}

func commitCLITestRepository(t *testing.T, root string, message string) {
	t.Helper()
	for _, arguments := range [][]string{
		{"init", "--quiet"},
		{"config", "user.name", "GDS CLI test"},
		{"config", "user.email", "cli-test@example.invalid"},
		{"config", "gc.auto", "0"},
		{"config", "maintenance.auto", "false"},
		{"add", "--all"},
		{"commit", "--quiet", "-m", message},
	} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
}

func copyCLITestFile(source string, target string) error {
	content, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, content, 0o644)
}

func copyCLITestTree(sourceRoot string, targetRoot string, relative string) error {
	source := filepath.Join(sourceRoot, relative)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("test estate source is not a regular file: %s", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
}

func repositoryGitIndexPath(t *testing.T, root string) string {
	t.Helper()
	return runSessionGit(t, root, "rev-parse", "--path-format=absolute", "--git-path", "index")
}
