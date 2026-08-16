package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func TestMaterializeHandlerPublishesOnlyCheckoutWithExactAnchor(t *testing.T) {
	remote, headOID, anchorDigest := workspaceSourceFixture(t)
	runner, err := gitprovider.NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	source, err := runner.ObserveLocalCloneSource(context.Background(), remote, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	placement := Placement{
		DeviceID:     "device_01JEXAMPZ00000000000000000",
		RepositoryID: "repo_01JEXAMPZ0000000000000000C", Mode: "active",
		WorkspaceRoot: root, TargetPath: filepath.Join(root, "example"),
	}
	step := operations.Step{
		RepositoryID: placement.RepositoryID, Action: MaterializeCheckoutAction,
		Parameters: MaterializeStepParameters(
			placement, source, "full", "schema_version: 1\n", anchorDigest,
			DeviceCandidate{Path: "/tmp/device.yaml", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		),
	}
	handler, err := NewMaterializeHandler(runner)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := handler.Apply(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, _ := json.Marshal(evidence.After)
	if err := handler.Verify(context.Background(), step, afterRaw); err != nil {
		t.Fatal(err)
	}
	if head := workspaceGit(t, placement.TargetPath, "rev-parse", "HEAD"); head != headOID {
		t.Fatalf("head=%s want=%s", head, headOID)
	}
}

func workspaceSourceFixture(t *testing.T) (string, string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	workspaceGit(t, filepath.Dir(remote), "init", "--bare", "-q", remote)
	client := filepath.Join(t.TempDir(), "client")
	if err := os.Mkdir(client, 0o755); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, client, "init", "-q", "-b", "main")
	workspaceGit(t, client, "config", "user.name", "GDS Fixture")
	workspaceGit(t, client, "config", "user.email", "fixture@example.invalid")
	if err := os.Mkdir(filepath.Join(client, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor := []byte("schema_version: 1\n")
	if err := os.WriteFile(filepath.Join(client, ".gds", "repository.yaml"), anchor, 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, client, "add", ".gds/repository.yaml")
	workspaceGit(t, client, "commit", "-qm", "anchor")
	workspaceGit(t, client, "remote", "add", "origin", remote)
	workspaceGit(t, client, "push", "-qu", "origin", "main")
	workspaceGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return remote, workspaceGit(t, client, "rev-parse", "HEAD"),
		fmt.Sprintf("sha256:%x", sha256.Sum256(anchor))
}

func workspaceGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}
