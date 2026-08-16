package fork

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func TestInspectMaintainedForkUsesCachedRefsWithoutClaimingFreshness(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	repository, base, current := createForkFixture(t, true)
	runner, err := gitprovider.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	anchor := forkAnchor("maintained-patch")
	report, findings := (Inspector{Git: runner}).Inspect(context.Background(), repository, anchor)
	if !report.Comparison.Available || report.Comparison.LeftOID != base ||
		report.Comparison.RightOID != current || report.Comparison.RightOnly != 1 {
		t.Fatalf("report = %#v", report)
	}
	if !forkHasFinding(findings, "GDS_FORK_REMOTE_FRESHNESS_NOT_PROVEN") ||
		forkHasFinding(findings, "GDS_FORK_UNEXPECTED_COMMITS") {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestInspectUpstreamTrackingForkPreservesUnexpectedCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	repository, _, _ := createForkFixture(t, true)
	runner, err := gitprovider.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	_, findings := (Inspector{Git: runner}).Inspect(
		context.Background(), repository, forkAnchor("upstream-tracking"),
	)
	if !forkHasFinding(findings, "GDS_FORK_UNEXPECTED_COMMITS") {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestDetachedForkDoesNotRequireUpstreamRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	repository, _, _ := createForkFixture(t, false)
	runner, err := gitprovider.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	report, findings := (Inspector{Git: runner}).Inspect(
		context.Background(), repository, forkAnchor("detached"),
	)
	if len(findings) != 0 || report.Comparison.Available {
		t.Fatalf("report = %#v, findings = %#v", report, findings)
	}
}

func forkAnchor(policy string) domain.RepositoryAnchor {
	return domain.RepositoryAnchor{
		Repository: domain.RepositoryIdentity{ID: "repo_01JEXAMPZ0000000000000000C"},
		Provider:   domain.GitHubLocator{Owner: "example", Name: "fork"},
		Fork: &domain.ForkPolicy{
			Upstream: domain.ForkUpstream{
				Provider: "github", RepositoryID: 2, Owner: "upstream", Name: "source",
			},
			Policy: policy, SyncBranch: "main", PreserveForkCommits: true,
		},
	}
}

func createForkFixture(t *testing.T, upstreamRemote bool) (string, string, string) {
	t.Helper()
	repository := t.TempDir()
	forkGit(t, repository, "init", "-qb", "main")
	forkGit(t, repository, "config", "user.name", "GDS Test")
	forkGit(t, repository, "config", "user.email", "gds@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	forkGit(t, repository, "add", "base.txt")
	forkGit(t, repository, "commit", "-qm", "base")
	base := strings.TrimSpace(forkGitOutput(t, repository, "rev-parse", "HEAD"))
	forkGit(t, repository, "remote", "add", "origin", "https://github.com/example/fork.git")
	if upstreamRemote {
		forkGit(t, repository, "remote", "add", "upstream", "https://github.com/upstream/source.git")
	}
	forkGit(t, repository, "update-ref", "refs/remotes/upstream/main", base)
	if err := os.WriteFile(filepath.Join(repository, "fork.txt"), []byte("fork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	forkGit(t, repository, "add", "fork.txt")
	forkGit(t, repository, "commit", "-qm", "fork change")
	current := strings.TrimSpace(forkGitOutput(t, repository, "rev-parse", "HEAD"))
	forkGit(t, repository, "update-ref", "refs/remotes/origin/main", current)
	return repository, base, current
}

func forkGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func forkGitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

func forkHasFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
