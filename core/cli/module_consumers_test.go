package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestModuleConsumerPlanningIsolatesPackageProviderFailure(t *testing.T) {
	root := t.TempDir()
	moduleRoot, moduleRemote, oldOID := moduleConsumerModuleFixture(t, root)
	gitConsumerID := "repo_01JEXAMPZ0000000000000000D"
	packageConsumerID := "repo_01JEXAMPZ0000000000000000E"
	moduleConsumerFixture(t, root, "git-consumer", gitConsumerID, 223456789, "git-submodule-consumer", oldOID)
	moduleConsumerFixture(t, root, "package-consumer", packageConsumerID, 323456789, "package-consumer", oldOID)
	if remoteHead := runSessionGit(t, moduleRemote, "rev-parse", "refs/heads/main"); remoteHead == oldOID {
		t.Fatal("module target commit was not advanced")
	}
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	statePath := sessionStatePath(t)
	exitCode, planned, stderr := executeJSON(
		t, "--json", "module", "update-consumers", "--plan",
		"--module", moduleRoot, "--inventory-root", root,
		"--consumer-id", gitConsumerID, "--consumer-id", packageConsumerID,
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "module-consumers",
	)
	if exitCode != 10 || planned.Mutation.Attempted ||
		!containsFinding(planned.Findings, "GDS_MODULE_CONSUMER_PACKAGE_PROVIDER_REQUIRED") {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	data, ok := planned.Data.(map[string]any)
	if !ok || data["planned"] != float64(1) || data["blocked"] != float64(1) {
		t.Fatalf("data=%#v", planned.Data)
	}
	subplans, _ := data["subplans"].([]any)
	if len(subplans) != 2 {
		t.Fatalf("subplans=%#v", subplans)
	}
	plannedCount := 0
	for _, raw := range subplans {
		subplan, _ := raw.(map[string]any)
		if subplan["status"] == "planned" && subplan["plan_id"] != "" {
			plannedCount++
		}
	}
	if plannedCount != 1 {
		t.Fatalf("subplans=%#v", subplans)
	}
}

func moduleConsumerModuleFixture(t *testing.T, root string) (string, string, string) {
	t.Helper()
	moduleRoot := filepath.Join(root, "module")
	if err := os.Mkdir(moduleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, moduleRoot, "init", "-q", "-b", "main")
	runSessionGit(t, moduleRoot, "config", "user.name", "Module")
	runSessionGit(t, moduleRoot, "config", "user.email", "module@example.invalid")
	if err := os.Mkdir(filepath.Join(moduleRoot, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-module-fork-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	text := string(anchor)
	start := strings.Index(text, "\nrelationships:\n")
	end := strings.Index(text, "\nmodule:\n")
	if start < 0 || end <= start {
		t.Fatal("module fixture relationship block not found")
	}
	text = text[:start] + text[end:]
	text = strings.Replace(text, `pin_policy: "version-tag"`, `pin_policy: "default-branch-commit"`, 1)
	text = strings.Replace(text, `mode: "version-tag"`, `mode: "none"`, 1)
	// A module a consumer pins must state how it is proven, and the pin now
	// proves it at the target commit rather than assuming it. `true` is a lane
	// that succeeds in a clean checkout without depending on anything installed.
	text += "\nverification:\n  commands:\n    test:\n      - \"true\"\n  required:\n    - \"test\"\n"
	if err := os.WriteFile(filepath.Join(moduleRoot, ".gds", "repository.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "module.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, moduleRoot, "add", ".gds/repository.yaml", "module.txt")
	runSessionGit(t, moduleRoot, "commit", "-qm", "module v1")
	oldOID := runSessionGit(t, moduleRoot, "rev-parse", "HEAD")
	remote := filepath.Join(t.TempDir(), "module.git")
	runSessionGit(t, filepath.Dir(remote), "init", "--bare", "-q", remote)
	runSessionGit(t, moduleRoot, "remote", "add", "origin", remote)
	runSessionGit(t, moduleRoot, "push", "-qu", "origin", "main")
	runSessionGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	if err := os.WriteFile(filepath.Join(moduleRoot, "module.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, moduleRoot, "commit", "-qam", "module v2")
	runSessionGit(t, moduleRoot, "push", "-q", "origin", "main")
	return moduleRoot, remote, oldOID
}

func moduleConsumerFixture(
	t *testing.T,
	root string,
	name string,
	repositoryID string,
	providerID int,
	relationshipType string,
	oldOID string,
) string {
	t.Helper()
	consumer := filepath.Join(root, name)
	if err := os.Mkdir(consumer, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, consumer, "init", "-q", "-b", "main")
	runSessionGit(t, consumer, "config", "user.name", "Consumer")
	runSessionGit(t, consumer, "config", "user.email", "consumer@example.invalid")
	if err := os.Mkdir(filepath.Join(consumer, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(anchor), "repo_01JEXAMPZ0000000000000000C", repositoryID, 1)
	text = strings.Replace(text, "repository_id: 123456789", "repository_id: "+strconv.Itoa(providerID), 1)
	text = strings.Replace(text, `  name: "example-project"`, `  name: "`+name+`"`, 1)
	relationship := "\nrelationships:\n  - type: \"" + relationshipType + "\"\n" +
		"    target: \"repo_01JEXAMPZ0000000000000000C\"\n"
	if relationshipType == "git-submodule-consumer" {
		relationship += "    gitmodules_name: \"module\"\n"
	}
	text = strings.Replace(text, "\nrelease:\n", relationship+"\nrelease:\n", 1)
	if err := os.WriteFile(filepath.Join(consumer, ".gds", "repository.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, consumer, "add", ".gds/repository.yaml")
	if relationshipType == "git-submodule-consumer" {
		modules := "[submodule \"module\"]\n\tpath = modules/module\n\turl = https://github.com/example-org/public-module-fork.git\n"
		if err := os.WriteFile(filepath.Join(consumer, ".gitmodules"), []byte(modules), 0o644); err != nil {
			t.Fatal(err)
		}
		runSessionGit(t, consumer, "add", ".gitmodules")
		runSessionGit(t, consumer, "update-index", "--add", "--cacheinfo", "160000,"+oldOID+",modules/module")
	}
	runSessionGit(t, consumer, "commit", "-qm", "configure consumer")
	remote := filepath.Join(t.TempDir(), name+".git")
	runSessionGit(t, filepath.Dir(remote), "init", "--bare", "-q", remote)
	runSessionGit(t, consumer, "remote", "add", "origin", remote)
	runSessionGit(t, consumer, "push", "-qu", "origin", "main")
	runSessionGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	if relationshipType == "git-submodule-consumer" {
		runSessionGit(t, consumer, "switch", "-qc", "task/update-module-pin")
		if err := os.MkdirAll(filepath.Join(consumer, "modules", "module"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return consumer
}
