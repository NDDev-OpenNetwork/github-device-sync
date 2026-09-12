package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagedAnchorAndProjectionsLandInOneCommit(t *testing.T) {
	root := testEstateRoot(t)
	t.Setenv("GDS_ESTATE_ROOT", root)
	anchorPath := filepath.Join(root, ".gds", "repository.yaml")
	raw, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `display_name: "github-device-sync"`, `display_name: "staged projection fixture"`, 1))
	if err := os.WriteFile(anchorPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, root, "add", ".gds/repository.yaml")
	base := []string{"--json", "--cwd", root, "generate", "repository",
		"--state-path", sessionStatePath(t), "--device-id", "device_01JEXAMPZ00000000000000000",
		"--session-id", "staged-projection"}
	exit, planned, stderr := executeJSON(t, append(base, "--plan")...)
	if exit != 0 || len(planned.Findings) != 0 {
		t.Fatalf("plan exit=%d stderr=%s result=%#v", exit, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)

	// An edit after planning must not be authorized by the earlier plan.
	changed := append(append([]byte(nil), raw...), []byte("\n# changed after planning\n")...)
	if err := os.WriteFile(anchorPath, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	exit, refused, _ := executeJSON(t, append(base, "--apply", planID)...)
	if exit == 0 || refused.Mutation.Completed {
		t.Fatalf("changed manifest was applied: %#v", refused)
	}
	if err := os.WriteFile(anchorPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// A refused attempt has its own journal; plan again against restored input.
	exit, planned, stderr = executeJSON(t, append(base, "--plan")...)
	if exit != 0 {
		t.Fatalf("replan exit=%d stderr=%s result=%#v", exit, stderr, planned)
	}
	exit, applied, stderr := executeJSON(t, append(base, "--apply", syncPlanID(t, planned.Data))...)
	if exit != 0 || !applied.Mutation.Completed {
		t.Fatalf("apply exit=%d stderr=%s result=%#v", exit, stderr, applied)
	}
	exit, verified, stderr := executeJSON(t, append(base, "--verify", applied.OperationID)...)
	if exit != 0 {
		t.Fatalf("verify exit=%d stderr=%s result=%#v", exit, stderr, verified)
	}
	runSessionGit(t, root, "add", ".")
	runSessionGit(t, root, "commit", "-qm", "stage anchor and generated projections together")
	exit, checked, stderr := executeJSON(t, "--json", "--cwd", root, "generate", "repository", "--check")
	if exit != 0 || len(checked.Findings) != 0 {
		t.Fatalf("post-commit drift: exit=%d stderr=%s result=%#v", exit, stderr, checked)
	}
}
