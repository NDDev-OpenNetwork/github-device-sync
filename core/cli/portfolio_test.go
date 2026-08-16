package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPortfolioPlanIsolatesDirtyRepository(t *testing.T) {
	root := t.TempDir()
	readyID := "repo_01JEXAMPZ0000000000000000D"
	blockedID := "repo_01JEXAMPZ0000000000000000E"
	portfolioRepositoryFixture(t, root, "ready", readyID, 223456789, false)
	portfolioRepositoryFixture(t, root, "blocked", blockedID, 323456789, true)
	t.Setenv("GDS_ESTATE_ROOT", repositoryRoot(t))

	exitCode, envelope, stderr := executeJSON(
		t, "--json", "portfolio", "plan",
		"--portfolio", "portfolio:personal-projects",
		"--operation", "repository-change",
		"--intent", "Prepare one independently reviewable repository change.",
		"--inventory-root", root,
	)
	if exitCode != 10 || envelope.Mutation.Attempted ||
		!containsFinding(envelope.Findings, "GDS_PORTFOLIO_REPOSITORY_NOT_READY") {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok || data["ready_count"] != float64(1) || data["blocked_count"] != float64(1) ||
		data["target_set_digest"] == "" || data["plan_digest"] == "" {
		t.Fatalf("data=%#v", envelope.Data)
	}
	subplans, _ := data["subplans"].([]any)
	if len(subplans) != 2 {
		t.Fatalf("subplans=%#v", subplans)
	}
	statuses := map[string]string{}
	for _, raw := range subplans {
		subplan, _ := raw.(map[string]any)
		statuses[subplan["repository_id"].(string)] = subplan["status"].(string)
		if subplan["subplan_digest"] == "" {
			t.Fatalf("subplan lacks digest: %#v", subplan)
		}
	}
	if statuses[readyID] != "ready" || statuses[blockedID] != "blocked" {
		t.Fatalf("statuses=%#v", statuses)
	}
}

func portfolioRepositoryFixture(
	t *testing.T,
	root string,
	name string,
	repositoryID string,
	providerID int,
	dirty bool,
) string {
	t.Helper()
	repository := filepath.Join(root, name)
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, repository, "init", "-q", "-b", "main")
	runSessionGit(t, repository, "config", "user.name", "Portfolio Fixture")
	runSessionGit(t, repository, "config", "user.email", "portfolio@example.invalid")
	if err := os.Mkdir(filepath.Join(repository, ".gds"), 0o755); err != nil {
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
	text = strings.Replace(text, `display_name: "example-project"`, `display_name: "`+name+`"`, 1)
	text = strings.Replace(text, `  name: "example-project"`, `  name: "`+name+`"`, 1)
	if err := os.WriteFile(filepath.Join(repository, ".gds", "repository.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "fixture.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, repository, "add", ".gds/repository.yaml", "fixture.txt")
	runSessionGit(t, repository, "commit", "-qm", "initialize portfolio fixture")
	remote := filepath.Join(t.TempDir(), name+".git")
	runSessionGit(t, filepath.Dir(remote), "init", "--bare", "-q", remote)
	runSessionGit(t, repository, "remote", "add", "origin", remote)
	runSessionGit(t, repository, "push", "-qu", "origin", "main")
	runSessionGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	if dirty {
		if err := os.WriteFile(filepath.Join(repository, "fixture.txt"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repository
}
