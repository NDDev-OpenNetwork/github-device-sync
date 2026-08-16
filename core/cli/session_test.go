package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type sessionFixtureState struct {
	client   string
	writer   string
	remote   string
	firstOID string
}

func runSessionGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func sessionFixture(t *testing.T) sessionFixtureState {
	return sessionFixtureWithPolicies(t, "preferred", "pull-request", true)
}

func sessionFixtureWithHandoffPolicy(t *testing.T, policy string) sessionFixtureState {
	return sessionFixtureWithPolicies(t, policy, "pull-request", true)
}

func sessionFixtureWithPolicies(
	t *testing.T,
	handoffPolicy string,
	integrationPolicy string,
	requiredChecks bool,
) sessionFixtureState {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runSessionGit(t, filepath.Dir(remote), "init", "--bare", "-q", remote)
	client := filepath.Join(t.TempDir(), "client")
	if err := os.Mkdir(client, 0o755); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, client, "init", "-q", "-b", "main")
	runSessionGit(t, client, "config", "user.name", "GDS Client")
	runSessionGit(t, client, "config", "user.email", "client@example.invalid")
	if err := os.Mkdir(filepath.Join(client, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.ReadFile(filepath.Join(
		repositoryRoot(t), "tests", "fixtures", "schemas", "v1", "valid-repository.yaml",
	))
	if err != nil {
		t.Fatal(err)
	}
	anchor = []byte(strings.Replace(
		string(anchor), `handoff_pr: "preferred"`, `handoff_pr: "`+handoffPolicy+`"`, 1,
	))
	anchor = []byte(strings.Replace(
		string(anchor), `integration: "pull-request"`, `integration: "`+integrationPolicy+`"`, 1,
	))
	if !requiredChecks {
		anchor = []byte(strings.Replace(
			string(anchor), "required:\n    - \"test\"", "required: []", 1,
		))
	}
	if err := os.WriteFile(filepath.Join(client, ".gds", "repository.yaml"), anchor, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client, "fixture.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, client, "add", ".gds/repository.yaml", "fixture.txt")
	runSessionGit(t, client, "commit", "-qm", "first")
	firstOID := runSessionGit(t, client, "rev-parse", "HEAD")
	runSessionGit(t, client, "remote", "add", "origin", remote)
	runSessionGit(t, client, "push", "-qu", "origin", "main")
	runSessionGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	writer := filepath.Join(t.TempDir(), "writer")
	runSessionGit(t, filepath.Dir(writer), "clone", "-q", remote, writer)
	runSessionGit(t, writer, "config", "user.name", "GDS Writer")
	runSessionGit(t, writer, "config", "user.email", "writer@example.invalid")
	return sessionFixtureState{client: client, writer: writer, remote: remote, firstOID: firstOID}
}

func (fixture sessionFixtureState) pushSecond(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.writer, "fixture.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, fixture.writer, "commit", "-qam", "second")
	runSessionGit(t, fixture.writer, "push", "-q", "origin", "main")
}

func sessionStatePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "state.db")
	store, err := state.Initialize(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSessionStartSeparatesCachedObservationFromExplicitRefresh(t *testing.T) {
	fixture := sessionFixture(t)
	fixture.pushSecond(t)
	t.Setenv("GDS_ESTATE_ROOT", repositoryRoot(t))
	statePath := sessionStatePath(t)

	exitCode, cached, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "session", "start",
		"--scope", "current", "--refresh", "none", "--state-path", statePath,
	)
	if exitCode != 3 || cached.Mutation.Attempted {
		t.Fatalf("cached exit=%d stderr=%q envelope=%#v", exitCode, stderr, cached)
	}
	cachedData, ok := cached.Data.(map[string]any)
	if !ok {
		t.Fatalf("cached data=%#v", cached.Data)
	}
	cachedBoundaries, _ := cachedData["boundaries"].([]any)
	if len(cachedBoundaries) != 1 {
		t.Fatalf("cached boundaries=%#v", cachedBoundaries)
	}
	cachedBoundary, _ := cachedBoundaries[0].(map[string]any)
	cachedAfter, _ := cachedBoundary["after"].(map[string]any)
	if cachedAfter["classification"] != "clean-cached" || cachedAfter["remote_freshness"] != "unknown" {
		t.Fatalf("cached boundary=%#v", cachedBoundary)
	}

	exitCode, refreshed, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "session", "start",
		"--scope", "current", "--refresh", "origin", "--state-path", statePath,
	)
	if exitCode != 3 || !refreshed.Mutation.Attempted || !refreshed.Mutation.Completed {
		t.Fatalf("refresh exit=%d stderr=%q envelope=%#v", exitCode, stderr, refreshed)
	}
	refreshedData, _ := refreshed.Data.(map[string]any)
	refreshedBoundaries, _ := refreshedData["boundaries"].([]any)
	boundary, _ := refreshedBoundaries[0].(map[string]any)
	after, _ := boundary["after"].(map[string]any)
	if after["classification"] != "behind-current" || after["remote_freshness"] != "current" {
		t.Fatalf("refreshed boundary=%#v", boundary)
	}
	if runSessionGit(t, fixture.client, "rev-parse", "HEAD") != fixture.firstOID {
		t.Fatal("session refresh integrated the current branch")
	}
	store, err := state.OpenReadOnly(context.Background(), statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.GetRemoteRefresh(
		context.Background(), "repo_01JEXAMPZ0000000000000000C",
		runSessionGit(t, fixture.client, "rev-parse", "--show-toplevel"), "origin",
	)
	if err != nil || record.HeadOID != fixture.firstOID || record.RefsDigest == "" || record.ForcedUpdate {
		t.Fatalf("refresh evidence=%#v err=%v", record, err)
	}
}

func TestSessionStartReportsForcedUpdateAndBlocksSync(t *testing.T) {
	fixture := sessionFixture(t)
	fixture.pushSecond(t)
	t.Setenv("GDS_ESTATE_ROOT", repositoryRoot(t))
	statePath := sessionStatePath(t)
	_, _, _ = executeJSON(
		t, "--json", "--cwd", fixture.client, "session", "start", "--refresh", "origin", "--state-path", statePath,
	)
	runSessionGit(t, fixture.writer, "reset", "--hard", fixture.firstOID)
	if err := os.WriteFile(filepath.Join(fixture.writer, "fixture.txt"), []byte("rewritten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, fixture.writer, "commit", "-qam", "rewritten")
	runSessionGit(t, fixture.writer, "push", "-q", "--force", "origin", "main")
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "session", "start", "--refresh", "origin", "--state-path", statePath,
	)
	if exitCode != 8 || !containsFinding(envelope.Findings, "GDS_SESSION_FORCED_UPDATE") {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	data, _ := envelope.Data.(map[string]any)
	boundaries, _ := data["boundaries"].([]any)
	boundary, _ := boundaries[0].(map[string]any)
	blocked, _ := boundary["blocked_actions"].([]any)
	if boundary["forced_update"] != true || !containsAny(blocked, "sync") {
		t.Fatalf("forced boundary=%#v", boundary)
	}
	if runSessionGit(t, fixture.client, "rev-parse", "HEAD") != fixture.firstOID {
		t.Fatal("forced refresh integrated the current branch")
	}
	store, err := state.OpenReadOnly(context.Background(), statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.GetRemoteRefresh(
		context.Background(), "repo_01JEXAMPZ0000000000000000C",
		runSessionGit(t, fixture.client, "rev-parse", "--show-toplevel"), "origin",
	)
	if err != nil || !record.ForcedUpdate {
		t.Fatalf("forced refresh evidence=%#v err=%v", record, err)
	}
	exitCode, syncPlan, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "sync", "checkouts", "--plan",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "forced-update-session",
	)
	if exitCode != 3 || !containsFinding(syncPlan.Findings, "GDS_SYNC_NO_ELIGIBLE_CHECKOUT") {
		t.Fatalf("forced sync exit=%d stderr=%q envelope=%#v", exitCode, stderr, syncPlan)
	}
}

func TestSessionStartDoesNotFetchWithoutDurableState(t *testing.T) {
	fixture := sessionFixture(t)
	fixture.pushSecond(t)
	t.Setenv("GDS_ESTATE_ROOT", repositoryRoot(t))
	missingState := filepath.Join(t.TempDir(), "missing", "state.db")
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", fixture.client, "session", "start", "--refresh", "origin",
		"--state-path", missingState,
	)
	if exitCode != 3 || envelope.Mutation.Attempted ||
		!containsFinding(envelope.Findings, "GDS_SESSION_STATE_NOT_PROVEN") {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	if oid := runSessionGit(t, fixture.client, "rev-parse", "refs/remotes/origin/main"); oid != fixture.firstOID {
		t.Fatalf("origin/main changed without durable state: %s", oid)
	}
}

func containsAny(values []any, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
