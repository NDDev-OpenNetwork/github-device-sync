package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func TestOrderCompletionGraphIsDeterministicWithoutDependencies(t *testing.T) {
	contexts := []completeContext{
		{repositoryID: "repo_c"},
		{repositoryID: "repo_a"},
		{repositoryID: "repo_b"},
	}
	byID := map[string]completeContext{}
	for _, current := range contexts {
		byID[current.repositoryID] = current
	}
	ordered, findings := (&Services{}).orderCompletionGraph(
		context.Background(), contexts, byID,
	)
	if len(findings) != 0 || len(ordered) != 3 {
		t.Fatalf("order findings=%+v ordered=%+v", findings, ordered)
	}
	for index, want := range []string{"repo_a", "repo_b", "repo_c"} {
		if ordered[index].repositoryID != want {
			t.Fatalf("order[%d]=%q want=%q", index, ordered[index].repositoryID, want)
		}
	}
}

func TestOrderCompletionGraphBlocksPackageFinalizationBeforeC5(t *testing.T) {
	module := completeContext{repositoryID: "repo_module"}
	consumer := completeContext{
		repositoryID: "repo_consumer",
		anchor: domain.RepositoryAnchor{Relationships: []domain.Relationship{{
			Type: "package-consumer", Target: module.repositoryID,
		}}},
	}
	contexts := []completeContext{consumer, module}
	byID := map[string]completeContext{
		consumer.repositoryID: consumer,
		module.repositoryID:   module,
	}
	ordered, findings := (&Services{}).orderCompletionGraph(
		context.Background(), contexts, byID,
	)
	if ordered != nil || len(findings) != 1 ||
		findings[0].Code != "GDS_COMPLETE_PACKAGE_FINALIZATION_NOT_PROVEN" {
		t.Fatalf("ordered=%+v findings=%+v", ordered, findings)
	}
}

func TestOrderCompletionGraphFinalizesPinnedGitlinkDependencyFirst(t *testing.T) {
	consumerRoot := t.TempDir()
	runCompletionGraphGit(t, consumerRoot, "init", "-q")
	moduleOID := "0123456789abcdef0123456789abcdef01234567"
	modules := "[submodule \"module\"]\n\tpath = modules/module\n\turl = https://github.com/example/module.git\n"
	if err := os.WriteFile(filepath.Join(consumerRoot, ".gitmodules"), []byte(modules), 0o644); err != nil {
		t.Fatal(err)
	}
	runCompletionGraphGit(t, consumerRoot, "add", ".gitmodules")
	runCompletionGraphGit(
		t, consumerRoot, "update-index", "--add", "--cacheinfo",
		"160000,"+moduleOID+",modules/module",
	)
	runner, err := gitprovider.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	module := completeContext{
		repositoryID: "repo_module",
		assessment:   CompleteAssessment{ExpectedTaskOID: moduleOID},
	}
	consumer := completeContext{
		root: consumerRoot, repositoryID: "repo_consumer",
		anchor: domain.RepositoryAnchor{Relationships: []domain.Relationship{{
			Type: "git-submodule-consumer", Target: module.repositoryID,
			GitmodulesName: "module",
		}}},
	}
	contexts := []completeContext{consumer, module}
	byID := map[string]completeContext{
		consumer.repositoryID: consumer,
		module.repositoryID:   module,
	}
	ordered, findings := (&Services{Git: runner}).orderCompletionGraph(
		context.Background(), contexts, byID,
	)
	if len(findings) != 0 || len(ordered) != 2 ||
		ordered[0].repositoryID != module.repositoryID ||
		ordered[1].repositoryID != consumer.repositoryID {
		t.Fatalf("ordered=%+v findings=%+v", ordered, findings)
	}
}

func runCompletionGraphGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
