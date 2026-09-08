package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	moduleworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/module"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func TestCleanupPendingStopsLaterLanesInSource(t *testing.T) {
	t.Parallel()
	source := moduleVerifySource(t)
	for _, need := range []string{
		"if result.CleanupPending {",
		"preserve = true",
		"no later lane was started",
		"return report, findings",
	} {
		if !strings.Contains(source, need) {
			t.Fatalf("missing %q", need)
		}
	}
}

func TestFailedRemoveWorktreeDoesNotDeleteWorkspace(t *testing.T) {
	root, oid := moduleVerifyRepository(t)
	runner, err := gitprovider.NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(root, ".git"), 0o700)
	})
	services := &Services{GitMutations: runner}
	plan := moduleworkflow.VerificationPlan{
		GitmodulesName: "example",
		Path:           "modules/example",
		GitlinkOID:     oid,
		Lanes: []moduleworkflow.LaneSelection{
			{Lane: "lint", Commands: []string{`chmod 000 "$(git rev-parse --git-common-dir)"`}},
		},
	}
	report, findings := services.runModuleLanes(context.Background(), root, plan, 30*time.Second)
	if len(report.Lanes) != 1 || len(report.Lanes[0].Commands) != 1 || report.Lanes[0].Commands[0].Status != "passed" {
		t.Fatalf("command=%#v", report)
	}
	foundCleanup := false
	for _, finding := range findings {
		if finding.Code == "GDS_MODULE_VERIFICATION_CLEANUP_NOT_PROVEN" {
			foundCleanup = true
		}
	}
	if !foundCleanup {
		t.Fatalf("cleanup finding missing: %#v", findings)
	}
}

func TestFailedRemoveWorktreeSourceContract(t *testing.T) {
	t.Parallel()
	source := moduleVerifySource(t)
	if !strings.Contains(source, "GDS_MODULE_VERIFICATION_CLEANUP_NOT_PROVEN") ||
		!strings.Contains(source, "if cleanupErr == nil") ||
		!strings.Contains(source, "os.RemoveAll(workspace)") {
		t.Fatal("failed RemoveWorktree no longer blocks workspace deletion")
	}
}

func TestUnsupportedOSProcessOwnershipFailsClosed(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(moduleVerifyDir(t), "module_process_other.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "supported only on Linux and macOS") {
		t.Fatal("unsupported OS no longer fails closed")
	}
}

func moduleVerifyDir(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(current)
}

func moduleVerifySource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(moduleVerifyDir(t), "module_verify.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func moduleVerifyRepository(t *testing.T) (string, string) {
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
	oid := strings.TrimSpace(string(output))
	if len(oid) != 40 {
		t.Fatalf("fixture OID %q is not a 40-character object name", oid)
	}
	return root, oid
}
