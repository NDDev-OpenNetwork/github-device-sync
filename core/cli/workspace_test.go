package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The workspace tests drive the second example device, whose descriptor is the
// one they select; the identity passed on the command line has to be its own.
const workspaceTestDeviceID = "device_01JEXAMPZ00000000000000001"

func TestWorkspacePlanAndMaterializationUseExactDevicePlacement(t *testing.T) {
	fixture := sessionFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	workspaceRoot := filepath.Join(home, "Developer", "example-user")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	anchorPath := filepath.Join(fixture.client, ".gds", "repository.yaml")
	devicePath := filepath.Join(repositoryRoot(t), "estate", "devices", "example-user-mac2.yaml")
	exitCode, placement, stderr := executeJSON(
		t, "--json", "workspace", "plan", "--anchor", anchorPath, "--device", devicePath,
	)
	if exitCode != 0 || placement.Mutation.Attempted {
		t.Fatalf("placement exit=%d stderr=%q envelope=%#v", exitCode, stderr, placement)
	}
	statePath := sessionStatePath(t)
	exitCode, planned, stderr := executeJSON(
		t, "--json", "repository", "materialize", "--plan",
		"--anchor", anchorPath, "--device", devicePath, "--source", fixture.remote,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-materialize",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	target := filepath.Join(workspaceRoot, "example-project")
	exitCode, unapproved, stderr := executeJSON(
		t, "--json", "repository", "materialize", "--apply", planID,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-materialize",
	)
	if exitCode != 6 || unapproved.Mutation.Attempted {
		t.Fatalf("unapproved exit=%d stderr=%q envelope=%#v", exitCode, stderr, unapproved)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("unapproved materialization created target: %v", err)
	}
	exitCode, applied, stderr := executeJSON(
		t, "--json", "repository", "materialize", "--apply", planID,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-materialize",
		"--approval-ref", "owner-approved:workspace-materialize",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	if head := runSessionGit(t, target, "rev-parse", "HEAD"); head != fixture.firstOID {
		t.Fatalf("target head=%s want=%s", head, fixture.firstOID)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "repository", "materialize", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-materialize",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
	deviceStateRoot := filepath.Join(home, ".local", "state", "github-device-sync")
	if err := os.MkdirAll(deviceStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	exitCode, removalPlan, stderr := executeJSON(
		t, "--json", "--cwd", target, "repository", "remove-checkout", "--plan",
		"--device", devicePath, "--state-path", statePath,
		"--device-id", workspaceTestDeviceID, "--session-id", "workspace-remove",
	)
	if exitCode != 0 || removalPlan.Mutation.Attempted {
		t.Fatalf("remove plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, removalPlan)
	}
	removalPlanID := syncPlanID(t, removalPlan.Data)
	exitCode, removalUnapproved, stderr := executeJSON(
		t, "--json", "repository", "remove-checkout", "--apply", removalPlanID,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-remove",
	)
	if exitCode != 6 || removalUnapproved.Mutation.Attempted {
		t.Fatalf("remove unapproved exit=%d stderr=%q envelope=%#v", exitCode, stderr, removalUnapproved)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("unapproved removal changed checkout: %v", err)
	}
	exitCode, removed, stderr := executeJSON(
		t, "--json", "repository", "remove-checkout", "--apply", removalPlanID,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-remove", "--approval-ref", "owner-approved:workspace-remove",
	)
	if exitCode != 0 || !removed.Mutation.Completed || removed.OperationID == "" {
		t.Fatalf("remove apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, removed)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("workspace target remains after quarantine: %v", err)
	}
	quarantine := filepath.Join(
		deviceStateRoot, "quarantine", "checkouts",
		"repo_01JEXAMPZ0000000000000000C", fixture.firstOID,
	)
	if _, err := os.Stat(filepath.Join(quarantine, ".gds", "repository.yaml")); err != nil {
		t.Fatalf("quarantined checkout missing: %v", err)
	}
	exitCode, removalVerified, stderr := executeJSON(
		t, "--json", "repository", "remove-checkout", "--verify", removed.OperationID,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-remove",
	)
	if exitCode != 0 || removalVerified.Mutation.Attempted {
		t.Fatalf("remove verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, removalVerified)
	}
}

func TestWorkspaceAuditReportsPlacedStandaloneCheckout(t *testing.T) {
	fixture := sessionFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	workspaceRoot := filepath.Join(home, "Developer", "example-user")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspaceRoot, "example-project")
	if err := os.Rename(fixture.client, target); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	devicePath := filepath.Join(repositoryRoot(t), "estate", "devices", "example-user-mac2.yaml")
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "workspace", "audit", "--root", workspaceRoot,
		"--device", devicePath,
	)
	if exitCode != 0 || envelope.Mutation.Attempted {
		t.Fatalf("audit exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok || data["discovered"] != float64(1) || data["anchored"] != float64(1) {
		t.Fatalf("data=%#v", envelope.Data)
	}
	layout, ok := data["layout"].(map[string]any)
	if !ok || layout["compliant"] != float64(1) || layout["standalone"] != float64(1) ||
		layout["embedded"] != float64(0) || layout["drifted"] != float64(0) {
		t.Fatalf("layout=%#v", data["layout"])
	}
}

func TestWorkspaceRegisterEstateUsesPlanApplyVerify(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("GDS_ESTATE_ROOT", "")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	statePath := sessionStatePath(t)
	root := filepath.Join(t.TempDir(), "control-plane")
	clone := exec.Command("git", "clone", "--quiet", "--no-local", testEstateRoot(t), root)
	if output, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone control-plane fixture: %v: %s", err, output)
	}
	attach := exec.Command("git", "switch", "--create", "fixture-main")
	attach.Dir = root
	if output, err := attach.CombinedOutput(); err != nil {
		t.Fatalf("attach control-plane fixture HEAD: %v: %s", err, output)
	}
	exitCode, planned, stderr := executeJSON(
		t, "--json", "--cwd", root, "workspace", "register-estate", "--plan",
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-register-estate",
	)
	if exitCode != 0 || planned.Mutation.Attempted {
		t.Fatalf("plan exit=%d stderr=%q envelope=%#v", exitCode, stderr, planned)
	}
	planID := syncPlanID(t, planned.Data)
	registrationPath := filepath.Join(configHome, "github-device-sync", "estate-registration.json")
	exitCode, unapproved, stderr := executeJSON(
		t, "--json", "workspace", "register-estate", "--apply", planID,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-register-estate",
	)
	if exitCode != 6 || unapproved.Mutation.Attempted {
		t.Fatalf("unapproved exit=%d stderr=%q envelope=%#v", exitCode, stderr, unapproved)
	}
	if _, err := os.Lstat(registrationPath); !os.IsNotExist(err) {
		t.Fatalf("unapproved registration changed the device: %v", err)
	}
	exitCode, applied, stderr := executeJSON(
		t, "--json", "workspace", "register-estate", "--apply", planID,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-register-estate",
		"--approval-ref", "owner-approved:workspace-register-estate",
	)
	if exitCode != 0 || !applied.Mutation.Completed || applied.OperationID == "" {
		t.Fatalf("apply exit=%d stderr=%q envelope=%#v", exitCode, stderr, applied)
	}
	info, err := os.Lstat(registrationPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("registration is not a regular file: info=%v err=%v", info, err)
	}
	exitCode, verified, stderr := executeJSON(
		t, "--json", "workspace", "register-estate", "--verify", applied.OperationID,
		"--state-path", statePath, "--device-id", workspaceTestDeviceID,
		"--session-id", "workspace-register-estate",
	)
	if exitCode != 0 || verified.Mutation.Attempted {
		t.Fatalf("verify exit=%d stderr=%q envelope=%#v", exitCode, stderr, verified)
	}
}
