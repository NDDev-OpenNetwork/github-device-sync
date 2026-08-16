package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectTopologyPreservesGitlinkAndRedactsRemoteCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	module := createCommittedRepository(t)
	superproject := createCommittedRepository(t)
	runGit(
		t, superproject, "-c", "protocol.file.allow=always", "submodule", "add", "-q",
		module, "modules/example",
	)
	runGit(t, superproject, "commit", "-qam", "add module")
	runGit(
		t, superproject, "remote", "add", "origin",
		"https://token:secret@github.com/example/superproject.git?token=leak",
	)

	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	topology, err := runner.InspectTopology(context.Background(), superproject)
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Submodules) != 1 {
		t.Fatalf("submodules = %#v", topology.Submodules)
	}
	submodule := topology.Submodules[0]
	if submodule.Name != "modules/example" || submodule.Path != "modules/example" ||
		submodule.GitlinkOID == "" || submodule.CurrentOID != submodule.GitlinkOID ||
		submodule.WorktreeState != "at-gitlink" {
		t.Fatalf("submodule = %#v", submodule)
	}
	if len(topology.Remotes) != 1 || len(topology.Remotes[0].FetchURLs) != 1 {
		t.Fatalf("remotes = %#v", topology.Remotes)
	}
	remote := topology.Remotes[0].FetchURLs[0]
	if !remote.CredentialsRedacted || strings.Contains(remote.Value, "token") ||
		strings.Contains(remote.Value, "secret") || strings.Contains(remote.Value, "leak") {
		t.Fatalf("credential-bearing remote was not redacted: %#v", remote)
	}

	moduleCheckout := filepath.Join(superproject, "modules", "example")
	configureIdentity(t, moduleCheckout)
	writeFile(t, moduleCheckout, "new.txt", "module change\n")
	runGit(t, moduleCheckout, "add", "new.txt")
	runGit(t, moduleCheckout, "commit", "-qm", "module change")
	offPin, err := runner.InspectTopology(context.Background(), superproject)
	if err != nil {
		t.Fatal(err)
	}
	if offPin.Submodules[0].WorktreeState != "off-gitlink" {
		t.Fatalf("off-pin submodule = %#v", offPin.Submodules[0])
	}
}

func TestCompareCachedRemoteRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	repository := createCommittedRepository(t)
	base := strings.TrimSpace(runGitOutput(t, repository, "rev-parse", "HEAD"))
	runGit(t, repository, "update-ref", "refs/remotes/upstream/main", base)
	writeFile(t, repository, "next.txt", "next\n")
	runGit(t, repository, "add", "next.txt")
	runGit(t, repository, "commit", "-qm", "next")
	next := strings.TrimSpace(runGitOutput(t, repository, "rev-parse", "HEAD"))
	runGit(t, repository, "update-ref", "refs/remotes/origin/main", next)
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := runner.CompareCachedRemoteRefs(
		context.Background(), repository, "upstream", "origin", "main",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !comparison.Available || comparison.LeftOnly != 0 || comparison.RightOnly != 1 ||
		comparison.LeftOID != base || comparison.RightOID != next ||
		comparison.Freshness != "cached-unknown" {
		t.Fatalf("comparison = %#v", comparison)
	}
}

func TestInspectTopologyUsesStoredRemoteWithoutGlobalInsteadOfRewrite(t *testing.T) {
	repository := createCommittedRepository(t)
	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/repository.git")
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, []byte(
		"[url \"git@github.com:\"]\n\tinsteadOf = https://github.com/\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	topology, err := runner.InspectTopology(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Remotes) != 1 || len(topology.Remotes[0].FetchURLs) != 1 ||
		topology.Remotes[0].FetchURLs[0].Value != "https://github.com/example/repository.git" {
		t.Fatalf("topology remotes=%+v", topology.Remotes)
	}
}

func TestSanitizeRepositoryURLPreservesCredentialFreeSCPTransport(t *testing.T) {
	value := "git@github.com:Example/repository.git"
	sanitized, redacted := sanitizeRepositoryURL(value)
	if redacted || sanitized != value {
		t.Fatalf("sanitized=%q redacted=%t", sanitized, redacted)
	}
}

func TestParseConfiguredSubmodulesRejectsTraversalAndDuplicates(t *testing.T) {
	t.Parallel()
	_, err := parseConfiguredSubmodules([]byte(
		"submodule.example.path\n../outside\x00" +
			"submodule.example.url\nhttps://github.com/example/module.git\x00",
	))
	if err == nil {
		t.Fatal("expected traversal rejection")
	}
	_, err = parseConfiguredSubmodules([]byte(
		"submodule.one.path\nmodule\x00" +
			"submodule.one.url\nhttps://github.com/example/one.git\x00" +
			"submodule.two.path\nmodule\x00" +
			"submodule.two.url\nhttps://github.com/example/two.git\x00",
	))
	if err == nil {
		t.Fatal("expected duplicate path rejection")
	}
}

func TestParseGitHubRepository(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		owner string
		name  string
	}{
		{"https://github.com/Example/repository.git", "Example", "repository"},
		{"git@github.com:Example/repository.git", "Example", "repository"},
		{"ssh://git@github.com/Example/repository.git", "Example", "repository"},
		{"git://github.com/Example/repository.git", "Example", "repository"},
	}
	for _, test := range tests {
		repository, err := ParseGitHubRepository(test.value)
		if err != nil {
			t.Fatalf("ParseGitHubRepository(%q): %v", test.value, err)
		}
		if repository.Owner != test.owner || repository.Name != test.name {
			t.Fatalf("ParseGitHubRepository(%q) = %#v", test.value, repository)
		}
	}
	invalid := []string{
		"file:///tmp/repository", "https://example.com/owner/repository",
		"https://token@github.com/owner/repository.git",
		"ssh://git:secret@github.com/owner/repository.git",
		"https://github.com/owner/repository.git?token=secret",
	}
	for _, value := range invalid {
		if _, err := ParseGitHubRepository(value); err == nil {
			t.Fatalf("ParseGitHubRepository(%q) unexpectedly succeeded", value)
		}
	}
}

func TestRewriteGitHubRepositoryURLPreservesTransportAndGitSuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/old-owner/old-name.git", "https://github.com/new-owner/new-name.git"},
		{"git@github.com:old-owner/old-name.git", "git@github.com:new-owner/new-name.git"},
		{"ssh://git@github.com/old-owner/old-name", "ssh://git@github.com/new-owner/new-name"},
		{"git://github.com/old-owner/old-name.git", "git://github.com/new-owner/new-name.git"},
	}
	for _, test := range tests {
		got, err := RewriteGitHubRepositoryURL(test.input, "new-owner", "new-name")
		if err != nil {
			t.Fatalf("RewriteGitHubRepositoryURL(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("RewriteGitHubRepositoryURL(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	for _, input := range []string{
		"https://example.com/old-owner/old-name.git",
		"https://token@github.com/old-owner/old-name.git",
		"file:///tmp/old-name.git",
	} {
		if _, err := RewriteGitHubRepositoryURL(input, "new-owner", "new-name"); err == nil {
			t.Fatalf("RewriteGitHubRepositoryURL(%q) unexpectedly succeeded", input)
		}
	}
}

func TestReadOnlyRunnerRejectsUnboundedTopologyCommands(t *testing.T) {
	t.Parallel()
	runner := NewRunnerForPath("git", 1024)
	directory := t.TempDir()
	commands := [][]string{
		{"config", "--list"},
		{"ls-files", "--others"},
		{"remote", "set-url", "origin", "https://example.invalid"},
		{"for-each-ref", "--format=%(contents)", "refs/heads"},
		{"rev-list", "--all"},
	}
	for _, command := range commands {
		if _, err := runner.Run(context.Background(), directory, command...); err == nil {
			t.Fatalf("git %v was not rejected", command)
		}
	}
}

func runGitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

func TestTopologyDoesNotWriteRepositoryFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	repository := createCommittedRepository(t)
	index := filepath.Join(repository, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.InspectTopology(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("topology inspection changed the Git index")
	}
}
