package cli

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHarnessLifecycleRequiresApprovalAndPreservesUnmanagedState(t *testing.T) {
	root := repositoryRoot(t)
	target := t.TempDir()
	prior := t.TempDir()
	statePath := sessionStatePath(t)
	t.Setenv("GDS_ESTATE_ROOT", root)
	if err := os.WriteFile(filepath.Join(target, "user-owned.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	common := []string{
		"--harness", "codex", "--target-root", target,
		"--skill-profile", "core", "--scope", "project",
		"--state-path", statePath, "--device-id", syncTestDeviceID,
		"--session-id", "harness-lifecycle-session",
	}
	planArgs := append([]string{"--json", "--cwd", root, "harness", "install"}, common...)
	planArgs = append(planArgs, "--plan")
	exitCode, planned, stderr := executeJSON(t, planArgs...)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	applyArgs := append([]string{"--json", "--cwd", root, "harness", "install"}, common...)
	applyArgs = append(applyArgs, "--apply", planID)
	exitCode, rejected, stderr := executeJSON(t, applyArgs...)
	if exitCode != 6 || rejected.Mutation.Attempted ||
		!containsFinding(rejected.Findings, "GDS_SIGNED_APPROVAL_REQUIRED") {
		t.Fatalf("unapproved exit=%d stderr=%q envelope=%#v", exitCode, stderr, rejected)
	}
	applyArgs = append(applyArgs, "--approval-ref", "owner-approved:harness-fixture")
	exitCode, applied, stderr := executeJSON(t, applyArgs...)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	verifyHarnessLifecycle(t, root, "install", common, applied.OperationID)
	copyHarnessFixture(t, target, prior)

	updateOperation := applyHarnessLifecycle(t, root, "update", common, nil)
	verifyHarnessLifecycle(t, root, "update", common, updateOperation)
	rollbackFlags := []string{"--rollback-source", prior}
	rollbackOperation := applyHarnessLifecycle(t, root, "rollback", common, rollbackFlags)
	verifyHarnessLifecycle(
		t, root, "rollback", append(common, rollbackFlags...), rollbackOperation,
	)
	removeOperation := applyHarnessLifecycle(t, root, "remove", common, nil)
	verifyHarnessLifecycle(t, root, "remove", common, removeOperation)

	content, err := os.ReadFile(filepath.Join(target, "user-owned.txt"))
	if err != nil || string(content) != "preserve\n" {
		t.Fatalf("unmanaged file content=%q err=%v", content, err)
	}
	for _, managed := range []string{
		filepath.Join(target, ".agents", "skills", "gds-orient", "SKILL.md"),
		filepath.Join(target, ".gds", "harness", "codex-core.lock.json"),
	} {
		if _, err := os.Lstat(managed); !os.IsNotExist(err) {
			t.Fatalf("managed path remains after remove: %s err=%v", managed, err)
		}
	}
}

func TestHarnessEvalEmitsEvidenceAndPreservesNotProven(t *testing.T) {
	root := repositoryRoot(t)
	isolatedPath := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(isolatedPath, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", isolatedPath)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "harness", "eval",
		"--harness", "codex", "--skill-profile", "core",
		"--model-label", "not-proven", "--execution-profile", "read-only",
		"--tool", "shell",
	)
	if exitCode != 3 || envelope.ExitClass != "not-proven" || envelope.Mutation.Attempted ||
		!containsFinding(envelope.Findings, "GDS_HARNESS_EVAL_NOT_PROVEN") {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok || data["result"] != "not-proven" ||
		len(data["cases"].([]any)) != 12 || data["result_digest"] == "" {
		t.Fatalf("evaluation data=%#v", envelope.Data)
	}
	assertEnvelopeSchema(t, envelope)
}

func applyHarnessLifecycle(
	t *testing.T,
	root string,
	operation string,
	common []string,
	extra []string,
) string {
	t.Helper()
	planArgs := append([]string{"--json", "--cwd", root, "harness", operation}, common...)
	planArgs = append(planArgs, extra...)
	planArgs = append(planArgs, "--plan")
	exitCode, planned, stderr := executeJSON(t, planArgs...)
	if exitCode != 0 {
		t.Fatalf("%s plan exit=%d stderr=%q envelope=%#v", operation, exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	applyArgs := append([]string{"--json", "--cwd", root, "harness", operation}, common...)
	applyArgs = append(applyArgs, extra...)
	applyArgs = append(
		applyArgs, "--apply", planID, "--approval-ref", "owner-approved:harness-fixture",
	)
	exitCode, applied, stderr := executeJSON(t, applyArgs...)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("%s apply exit=%d stderr=%q envelope=%#v", operation, exitCode, stderr, applied)
	}
	return applied.OperationID
}

func verifyHarnessLifecycle(
	t *testing.T,
	root string,
	operation string,
	common []string,
	operationID string,
) {
	t.Helper()
	args := append([]string{"--json", "--cwd", root, "harness", operation}, common...)
	args = append(args, "--verify", operationID)
	exitCode, verified, stderr := executeJSON(t, args...)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("%s verify exit=%d stderr=%q envelope=%#v", operation, exitCode, stderr, verified)
	}
}

func copyHarnessFixture(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
