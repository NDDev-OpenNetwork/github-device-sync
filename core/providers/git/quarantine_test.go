package git

import (
	"context"
	"crypto/sha256"
	"errors"
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

func TestQuarantineRemoteValidationAdmitsNetworkTransportsForReadOnlyObservation(t *testing.T) {
	client, _ := mutationRepository(t)
	runFetchGit(t, client, "remote", "add", "origin", "git@github.com:example/repository.git")
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	_, url, err := runner.validatedRemoteURL(context.Background(), client, "origin")
	if err != nil {
		t.Fatalf("read-only observation must admit the fetch URL: %v", err)
	}
	if url != "git@github.com:example/repository.git" {
		t.Fatalf("url=%q", url)
	}
	if _, err := runner.validatedPushURL(context.Background(), client, "origin"); !errors.Is(err, ErrNetworkMutationDisabled) {
		t.Fatalf("push URL must remain mutation-gated: %v", err)
	}
}

func TestQuarantineCheckoutNetworkRemoteFailsAtObservationNotAtTheMutationGate(t *testing.T) {
	fixture := fastForwardFixture(t)
	runFetchGit(t, fixture.client, "remote", "set-url", "origin", "ssh://127.0.0.1:1/repository.git")
	head := stringsTrim(runFetchGit(t, fixture.client, "rev-parse", "HEAD"))
	workspaceRoot := filepath.Dir(fixture.client)
	stateRoot := t.TempDir()
	quarantine := filepath.Join(stateRoot, "quarantine", "checkouts", "repo_fixture", head)
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.QuarantineCheckout(
		context.Background(), workspaceRoot, fixture.client, stateRoot, quarantine,
		head, "refs/heads/main", fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("x"))),
	)
	if err == nil {
		t.Fatal("an unreachable remote must fail the observation")
	}
	if errors.Is(err, ErrNetworkMutationDisabled) {
		t.Fatalf("read-only remote observation must not hit the mutation gate: %v", err)
	}
}
