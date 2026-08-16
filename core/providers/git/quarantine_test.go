package git

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestQuarantineCheckoutMovesCleanPublishedRepositoryWithoutDeletingIt(t *testing.T) {
	fixture := fastForwardFixture(t)
	anchor := []byte("schema_version: 1\n")
	if err := os.Mkdir(filepath.Join(fixture.client, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.client, ".gds", "repository.yaml"), anchor, 0o644); err != nil {
		t.Fatal(err)
	}
	runFetchGit(t, fixture.client, "add", ".gds/repository.yaml")
	runFetchGit(t, fixture.client, "commit", "-qm", "anchor")
	runFetchGit(t, fixture.client, "push", "-q", "origin", "main")
	head := stringsTrim(runFetchGit(t, fixture.client, "rev-parse", "HEAD"))
	workspaceRoot := filepath.Dir(fixture.client)
	stateRoot := t.TempDir()
	quarantine := filepath.Join(stateRoot, "quarantine", "checkouts", "repo_fixture", head)
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(anchor))
	report, err := runner.QuarantineCheckout(
		context.Background(), workspaceRoot, fixture.client, stateRoot, quarantine,
		head, "refs/heads/main", digest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.Location != "workspace" || report.After.Location != "quarantine" {
		t.Fatalf("report=%#v", report)
	}
	if _, err := os.Lstat(fixture.client); !os.IsNotExist(err) {
		t.Fatalf("workspace checkout remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(quarantine, ".gds", "repository.yaml")); err != nil {
		t.Fatalf("quarantined checkout missing: %v", err)
	}
	if _, err := runner.ObserveQuarantinedCheckout(
		context.Background(), workspaceRoot, fixture.client, stateRoot, quarantine,
		head, "refs/heads/main", digest,
	); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantineCheckoutPreservesDirtyRepository(t *testing.T) {
	fixture := fastForwardFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.client, "dirty.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	if _, err := runner.QuarantineCheckout(
		context.Background(), filepath.Dir(fixture.client), fixture.client, stateRoot,
		filepath.Join(stateRoot, "quarantine", "checkouts", "repo_fixture", fixture.firstOID),
		fixture.firstOID, "refs/heads/main", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	); err == nil {
		t.Fatal("dirty checkout was quarantined")
	}
	if content, err := os.ReadFile(filepath.Join(fixture.client, "dirty.txt")); err != nil || string(content) != "preserve\n" {
		t.Fatalf("dirty work changed: %q %v", content, err)
	}
}
