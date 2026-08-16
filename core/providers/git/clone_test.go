package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeLocalCheckoutClonesExactDefaultBranchAtomically(t *testing.T) {
	fixture := fastForwardFixture(t)
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	remote := strings.TrimSpace(runFetchGit(t, fixture.client, "remote", "get-url", "origin"))
	source, err := runner.ObserveLocalCloneSource(context.Background(), remote, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := t.TempDir()
	target := filepath.Join(workspaceRoot, "materialized")
	report, err := runner.MaterializeLocalCheckout(
		context.Background(), workspaceRoot, target, source, fixture.firstOID, "full", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.TargetState != "missing" || report.After.TargetState != "present" ||
		report.After.HeadOID != fixture.firstOID {
		t.Fatalf("report=%#v", report)
	}
	if _, err := runner.MaterializeLocalCheckout(
		context.Background(), workspaceRoot, target, source, fixture.firstOID, "full", nil,
	); err == nil {
		t.Fatal("materialized over an existing target")
	}
}

func TestMaterializeLocalCheckoutRejectsSymlinkWorkspace(t *testing.T) {
	fixture := fastForwardFixture(t)
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	remote := strings.TrimSpace(runFetchGit(t, fixture.client, "remote", "get-url", "origin"))
	source, err := runner.ObserveLocalCloneSource(context.Background(), remote, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.MaterializeLocalCheckout(
		context.Background(), link, filepath.Join(link, "target"), source, fixture.firstOID, "full", nil,
	); err == nil {
		t.Fatal("accepted symlink workspace root")
	}
}
