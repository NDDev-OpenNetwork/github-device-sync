package cli

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/app"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

func TestModuleAddMaterializesExactTypedRelationship(t *testing.T) {
	consumer := sessionFixtureWithPolicies(t, "never", "direct", false)
	moduleID := "repo_01JEXAMPZ0000000000000000D"
	moduleSource := filepath.Join(t.TempDir(), "module-source")
	if err := os.Mkdir(moduleSource, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, moduleSource, "init", "-q", "-b", "main")
	runSessionGit(t, moduleSource, "config", "user.name", "Module Fixture")
	runSessionGit(t, moduleSource, "config", "user.email", "module@example.invalid")
	if err := os.WriteFile(filepath.Join(moduleSource, "module.txt"), []byte("module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, moduleSource, "add", "module.txt")
	runSessionGit(t, moduleSource, "commit", "-qm", "module")
	runSessionGit(t, consumer.client, "-c", "protocol.file.allow=always", "submodule", "add", "-q", "--name", "module", moduleSource, "modules/module")
	runSessionGit(t, consumer.client, "config", "-f", ".gitmodules", "submodule.module.url", "https://github.com/example-org/public-module-fork.git")
	runSessionGit(t, consumer.client, "add", ".gitmodules", "modules/module")
	runSessionGit(t, consumer.client, "commit", "-qm", "add module topology")
	runSessionGit(t, consumer.client, "push", "-q", "origin", "main")
	candidatePath := filepath.Join(t.TempDir(), "module.yaml")
	raw, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-module-fork-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(
		string(raw), "repo_01JEXAMPZ0000000000000000C", moduleID, 1,
	))
	if err := os.WriteFile(candidatePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := sessionStatePath(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", consumer.client, "module", "add", "--plan",
		"--module-anchor", candidatePath, "--name", "module",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-add",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	exitCode, applied, stderr := executeJSON(
		t, "--json", "module", "add", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-add", "--approval-ref", "owner-approved:module-add",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	// Assert the relationship, not its serialization. The previous substring
	// match required unquoted scalars, which pinned the anchor writer to
	// whatever style `yaml.Marshal` happened to emit and would have declared a
	// correct anchor broken for writing `target: "repo_..."` the way every
	// authored anchor in the estate does.
	anchorRaw, err := os.ReadFile(filepath.Join(consumer.client, ".gds", "repository.yaml"))
	if err != nil {
		t.Fatalf("read materialized anchor: %v", err)
	}
	var materialized domain.RepositoryAnchor
	if err := serialization.DecodeInto(
		anchor.Path, anchorRaw, &materialized,
	); err != nil {
		t.Fatalf("decode materialized anchor: %v\n%s", err, anchorRaw)
	}
	expected := domain.Relationship{
		Type: "git-submodule-consumer", Target: moduleID, GitmodulesName: "module",
	}
	if !slices.Contains(materialized.Relationships, expected) {
		t.Fatalf("materialized relationships = %#v\n%s", materialized.Relationships, anchorRaw)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "module", "add", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-add",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func TestModuleUpdatePinRequiresPublishedModuleAndStagesOnlyGitlink(t *testing.T) {
	moduleID := "repo_01JEXAMPZ0000000000000000D"
	module := sessionFixtureWithPolicies(t, "never", "direct", false)
	moduleAnchorPath := filepath.Join(module.client, ".gds", "repository.yaml")
	moduleAnchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-module-fork-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	moduleAnchor = []byte(strings.Replace(string(moduleAnchor),
		"repo_01JEXAMPZ0000000000000000C", moduleID, 1))
	moduleAnchor = []byte(strings.Replace(string(moduleAnchor),
		`pin_policy: "version-tag"`, `pin_policy: "default-branch-commit"`, 1))
	moduleAnchor = []byte(strings.Replace(string(moduleAnchor),
		`integration: "pull-request"`, `integration: "direct"`, 1))
	// The fixture declared no verification at all, which is why this test passed
	// while the command refused every real module: the blanket required-checks
	// refusal only fired when a module declared one, and every module in the
	// estate does. A module the consumer pins must state how it is proven, and
	// the pin must now prove it at the target commit, so the fixture declares a
	// lane that succeeds in a clean checkout.
	moduleAnchor = append(moduleAnchor, []byte(
		"\nverification:\n  commands:\n    test:\n      - \"true\"\n  required:\n    - \"test\"\n",
	)...)
	if err := os.WriteFile(moduleAnchorPath, moduleAnchor, 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, module.client, "add", ".gds/repository.yaml")
	runSessionGit(t, module.client, "commit", "-qm", "configure module")
	runSessionGit(t, module.client, "push", "-q", "origin", "main")
	moduleTargetOID := runSessionGit(t, module.client, "rev-parse", "HEAD")

	consumer := sessionFixtureWithPolicies(t, "never", "direct", false)
	consumerAnchorPath := filepath.Join(consumer.client, ".gds", "repository.yaml")
	consumerAnchor, err := os.ReadFile(consumerAnchorPath)
	if err != nil {
		t.Fatal(err)
	}
	consumerAnchor = []byte(strings.Replace(
		string(consumerAnchor), "\nrelease:\n",
		"\nrelationships:\n  - type: \"git-submodule-consumer\"\n    target: \""+moduleID+"\"\n    gitmodules_name: \"module\"\n\nrelease:\n", 1,
	))
	if err := os.WriteFile(consumerAnchorPath, consumerAnchor, 0o644); err != nil {
		t.Fatal(err)
	}
	modules := "[submodule \"module\"]\n\tpath = modules/module\n\turl = https://github.com/example-org/public-module-fork.git\n"
	if err := os.WriteFile(filepath.Join(consumer.client, ".gitmodules"), []byte(modules), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, consumer.client, "add", ".gds/repository.yaml", ".gitmodules")
	oldOID := module.firstOID
	runSessionGit(t, consumer.client, "update-index", "--add", "--cacheinfo", "160000,"+oldOID+",modules/module")
	runSessionGit(t, consumer.client, "commit", "-qm", "configure module consumer")
	runSessionGit(t, consumer.client, "push", "-q", "origin", "main")
	runSessionGit(t, consumer.client, "switch", "-qc", "task/update-module-pin")
	if err := os.MkdirAll(filepath.Join(consumer.client, "modules", "module"), 0o755); err != nil {
		t.Fatal(err)
	}

	statePath := sessionStatePath(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", consumer.client, "module", "update-pin", "--plan",
		"--module", module.client, "--name", "module",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-update-pin",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	exitCode, applied, stderr := executeJSON(
		t, "--json", "module", "update-pin", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-update-pin",
		"--approval-ref", "owner-approved:module-update-pin",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	fields := strings.Fields(runSessionGit(t, consumer.client, "ls-files", "--stage", "modules/module"))
	if len(fields) < 2 || fields[1] != moduleTargetOID {
		t.Fatalf("staged gitlink=%#v want=%s", fields, moduleTargetOID)
	}
	if changed := runSessionGit(t, consumer.client, "diff", "--cached", "--name-only"); changed != "modules/module" {
		t.Fatalf("unexpected staged paths=%q", changed)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "module", "update-pin", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-update-pin",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func TestModuleReleasePublishesOneImmutableVersionTag(t *testing.T) {
	module := sessionFixtureWithPolicies(t, "never", "direct", false)
	anchorPath := filepath.Join(module.client, ".gds", "repository.yaml")
	moduleAnchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-module-fork-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	moduleAnchor = []byte(strings.Replace(string(moduleAnchor),
		`integration: "pull-request"`, `integration: "direct"`, 1))
	moduleAnchor = []byte(strings.Replace(string(moduleAnchor),
		`github_release: "required"`, `github_release: "optional"`, 1))
	if err := os.WriteFile(anchorPath, moduleAnchor, 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, module.client, "add", ".gds/repository.yaml")
	runSessionGit(t, module.client, "commit", "-qm", "configure versioned module")
	runSessionGit(t, module.client, "push", "-q", "origin", "main")
	targetOID := runSessionGit(t, module.client, "rev-parse", "HEAD")
	statePath := sessionStatePath(t)
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", module.client, "module", "release", "--plan",
		"--version", "1.2.3", "--state-path", statePath,
		"--device-id", syncTestDeviceID, "--session-id", "module-release",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	exitCode, applied, stderr := executeJSON(
		t, "--json", "module", "release", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-release", "--approval-ref", "owner-approved:module-release",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	if localTag := runSessionGit(t, module.client, "rev-parse", "refs/tags/v1.2.3"); localTag != targetOID {
		t.Fatalf("local tag=%s want=%s", localTag, targetOID)
	}
	if remoteTag := runSessionGit(t, module.remote, "rev-parse", "refs/tags/v1.2.3"); remoteTag != targetOID {
		t.Fatalf("remote tag=%s want=%s", remoteTag, targetOID)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "module", "release", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-release",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}

func TestModuleReleaseGitHubReleaseApplyRequiresPrivateRuntimes(t *testing.T) {
	module := sessionFixtureWithPolicies(t, "never", "direct", false)
	anchorPath := filepath.Join(module.client, ".gds", "repository.yaml")
	moduleAnchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-module-fork-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(moduleAnchor),
		`integration: "pull-request"`, `integration: "direct"`, 1)
	text = strings.Replace(text, `mode: "version-tag"`, `mode: "github-release"`, 1)
	if err := os.WriteFile(anchorPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, module.client, "add", ".gds/repository.yaml")
	runSessionGit(t, module.client, "commit", "-qm", "configure github-release module")
	runSessionGit(t, module.client, "push", "-q", "origin", "main")
	statePath := sessionStatePath(t)
	assetPath := filepath.Join(t.TempDir(), "provider.tar.gz")
	if err := os.WriteFile(assetPath, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	// Isolate device-local operator configuration so this test proves the same
	// fail-closed boundary on developer machines and clean CI runners.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	services, runtimePath := moduleReleaseReadServices(t)
	exitCode, planned, stderr := executeJSONWithServices(
		t, services, "--json", "--cwd", module.client, "module", "release", "--plan",
		"--version", "1.2.3", "--state-path", statePath,
		"--asset", assetPath, "--runtime-config", runtimePath,
		"--device-id", syncTestDeviceID, "--session-id", "module-release-blocked",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	// The controlled estate permits a managed module release plan, but apply
	// still fails closed before provider mutation when its private GitHub
	// runtimes are unavailable.
	exitCode, applied, stderr := executeJSONWithServices(
		t, services, "--json", "module", "release", "--apply", planID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-release-blocked", "--approval-ref", "owner-approved:module-release",
		"--runtime-config", runtimePath,
	)
	if exitCode == 0 || applied.Mutation.Attempted ||
		!containsFinding(applied.Findings, "GDS_GITHUB_MUTATION_RUNTIME_NOT_PROVEN") {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
}

func TestModuleReleasePlansGitHubReleaseWithPinPolicyIndependent(t *testing.T) {
	module := sessionFixtureWithPolicies(t, "never", "direct", false)
	anchorPath := filepath.Join(module.client, ".gds", "repository.yaml")
	moduleAnchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-module-fork-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	// Pin policy and release mode are independent: a default-branch-commit pin
	// module can still publish an immutable GitHub release. github_release stays
	// "required", which is the expected state for github-release mode.
	text := strings.Replace(string(moduleAnchor),
		`pin_policy: "version-tag"`, `pin_policy: "default-branch-commit"`, 1)
	text = strings.Replace(text, `integration: "pull-request"`, `integration: "direct"`, 1)
	text = strings.Replace(text, `mode: "version-tag"`, `mode: "github-release"`, 1)
	if err := os.WriteFile(anchorPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, module.client, "add", ".gds/repository.yaml")
	runSessionGit(t, module.client, "commit", "-qm", "configure github-release module")
	runSessionGit(t, module.client, "push", "-q", "origin", "main")
	targetOID := runSessionGit(t, module.client, "rev-parse", "HEAD")
	assetPath := filepath.Join(t.TempDir(), "provider.tar.gz")
	if err := os.WriteFile(assetPath, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	services, runtimePath := moduleReleaseReadServices(t)
	exitCode, envelope, stderr := executeJSONWithServices(
		t, services, "--json", "--cwd", module.client, "module", "release", "--plan",
		"--version", "1.2.3", "--state-path", sessionStatePath(t),
		"--asset", assetPath, "--runtime-config", runtimePath,
		"--device-id", syncTestDeviceID, "--session-id", "module-release-provider",
	)
	if exitCode != 0 || envelope.Mutation.Attempted {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("plan data=%#v", envelope.Data)
	}
	assessment, ok := data["assessment"].(map[string]any)
	if !ok || assessment["release_mode"] != "github-release" ||
		assessment["commit_oid"] != targetOID || assessment["owner"] != "example-org" ||
		assessment["name"] != "public-module-fork" ||
		assessment["mutation_capability_id"] != "mutation:github-organization" {
		t.Fatalf("assessment=%#v", data["assessment"])
	}
	plan, ok := data["plan"].(map[string]any)
	if !ok {
		t.Fatalf("plan=%#v", data["plan"])
	}
	steps, ok := plan["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("steps=%#v", plan["steps"])
	}
	step, ok := steps[0].(map[string]any)
	if !ok || step["action"] != "publish-github-release" {
		t.Fatalf("step=%#v", steps[0])
	}
}

func moduleReleaseReadServices(t *testing.T) (*app.Services, string) {
	t.Helper()
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-runtime.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(t.TempDir(), "github-runtime.yaml")
	if err := os.WriteFile(runtimePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}))
	for _, name := range []string{
		"GDS_GITHUB_APP_ORGANIZATION_KEY", "GDS_GITHUB_APP_PERSONAL_KEY",
		"GDS_GITHUB_APP_EXAMPLE_MEDIA_KEY", "GDS_GITHUB_APP_GUILD_KEY",
	} {
		t.Setenv(name, privatePEM)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/installations/900001/access_tokens":
			_, _ = writer.Write([]byte(`{"token":"ghs_read","expires_at":"2099-01-01T00:00:00Z",` +
				`"permissions":{"actions":"read","administration":"read","checks":"read",` +
				`"contents":"read","metadata":"read","pull_requests":"read"},` +
				`"repository_selection":"all"}`))
		case "/repos/example-org/public-module-fork":
			_, _ = fmt.Fprint(writer, `{"id":123456789,"node_id":"R_release_fixture",`+
				`"name":"public-module-fork","full_name":"example-org/public-module-fork",`+
				`"private":false,"visibility":"public","fork":false,"archived":false,`+
				`"disabled":false,"default_branch":"main",`+
				`"html_url":"https://github.com/example-org/public-module-fork",`+
				`"owner":{"login":"example-org"}}`)
		case "/repos/example-org/public-module-fork/git/ref/tags/v1.2.3",
			"/repos/example-org/public-module-fork/releases/tags/v1.2.3":
			http.NotFound(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	services, err := app.NewServices(app.DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 5 * time.Second
	services.GitHubRuntimeBuildOptions = githubruntime.BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	}
	return services, runtimePath
}
