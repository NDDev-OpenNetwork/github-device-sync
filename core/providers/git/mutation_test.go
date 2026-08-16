package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mutationRepository(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	commands := [][]string{
		{"init", "-q"},
		{"config", "user.name", "GDS Test"},
		{"config", "user.email", "gds@example.invalid"},
	}
	for _, arguments := range commands {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "fixture.txt"}, {"commit", "-qm", "fixture"}} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(string(output))
}

func TestMutationRunnerOnlyCASUpdatesRecoveryRefs(t *testing.T) {
	root, head := mutationRepository(t)
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	reference := "refs/gds/recovery/operation-1"
	missing, err := runner.ObserveRecoveryRef(context.Background(), root, reference)
	if err != nil || missing != zeroOID(40) {
		t.Fatalf("missing ref=%q err=%v", missing, err)
	}
	if err := runner.UpdateRecoveryRef(
		context.Background(), root, reference, head, zeroOID(40),
	); err != nil {
		t.Fatal(err)
	}
	observed, err := runner.ObserveRecoveryRef(context.Background(), root, reference)
	if err != nil || observed != head {
		t.Fatalf("observed=%q err=%v", observed, err)
	}
	if err := runner.UpdateRecoveryRef(
		context.Background(), root, reference, head, zeroOID(40),
	); err == nil {
		t.Fatal("stale expected OID updated recovery ref")
	}
	if err := runner.UpdateRecoveryRef(
		context.Background(), root, "refs/heads/main", head, zeroOID(40),
	); err == nil {
		t.Fatal("mutation runner accepted a non-recovery ref")
	}
	for _, unsafe := range []string{
		"refs/gds/recovery/a//b", "refs/gds/recovery/.hidden",
		"refs/gds/recovery/name.lock", "refs/gds/recovery/name.",
	} {
		if err := runner.UpdateRecoveryRef(
			context.Background(), root, unsafe, head, zeroOID(40),
		); err == nil {
			t.Fatalf("mutation runner accepted unsafe ref %q", unsafe)
		}
	}
}
