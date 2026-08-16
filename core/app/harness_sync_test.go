package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
)

func TestHarnessMarkerAbsenceClosesUnrenderableLeftoverQuestion(t *testing.T) {
	root := t.TempDir()
	if !harnessMarkerIsAbsent(root, "cline", "core") {
		t.Fatal("a missing unique marker must prove that GDS did not install the adapter")
	}
	marker := filepath.Join(root, ".gds", "harness", "cline-core.lock.json")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if harnessMarkerIsAbsent(root, "cline", "core") {
		t.Fatal("a present marker must remain unobservable without its adapter candidate")
	}
}

func TestHarnessMarkerAbsenceRejectsUnsafeIdentity(t *testing.T) {
	if harnessMarkerIsAbsent(t.TempDir(), "../../outside", "core") {
		t.Fatal("an unsafe marker path must not be treated as proven absent")
	}
}

func TestHarnessMarkerAbsenceDoesNotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, ".gds", "harness", "cline-core.lock.json")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing-target"), marker); err != nil {
		t.Fatal(err)
	}
	if harnessMarkerIsAbsent(root, "cline", "core") {
		t.Fatal("a symlink marker must remain unobservable")
	}
}

func TestCombinedHarnessConvergenceFailsClosed(t *testing.T) {
	envelope := (&Services{}).ConvergeDeviceHarnesses(t.Context(), HarnessSyncConvergeOptions{})
	if envelope.ExitClass != domain.ExitUnsupported || len(envelope.Findings) != 1 || envelope.Findings[0].Code != "GDS_HARNESS_SYNC_CONVERGE_REMOVED" || envelope.Mutation.Attempted {
		t.Fatalf("unsafe combined convergence remained callable: %+v", envelope)
	}
}

// A device declares which harnesses it runs; GDS does not gate that selection on
// its ability to introspect the ones the device did not pick. The unobservable
// finding therefore bounds the result's claim instead of refusing the run — it
// is carried into the envelope as not-proven, never dropped.
func TestConvergenceBlocksOnEveryFindingExceptDriftAndUnobservable(t *testing.T) {
	for _, code := range []string{
		"GDS_HARNESS_SELECTION_DRIFT",
		"GDS_HARNESS_UNOBSERVABLE_NOT_PROVEN",
	} {
		if convergenceBlockedBy(code) {
			t.Fatalf("%s must not refuse a mutating run", code)
		}
	}
	for _, code := range []string{
		"GDS_HARNESS_TARGET_COLLISION",
		"GDS_HARNESS_SELECTED_UNKNOWN",
		"GDS_HARNESS_SELECTION_EMPTY",
	} {
		if !convergenceBlockedBy(code) {
			t.Fatalf("%s must refuse a mutating run", code)
		}
	}
}

// A verify failure after a successful apply must not report the mutated entry as
// untouched: recovery would then be aimed at a target that has already changed.
func TestHaltedConvergenceReportsAppliedButUnverifiedTruthfully(t *testing.T) {
	services := &Services{}
	plan := harness.SyncPlan{
		DeviceID: "device_test",
		Entries: []harness.SyncEntry{
			{Harness: "codex", Action: harness.SyncActionInstall},
			{Harness: "zcode", Action: harness.SyncActionInstall},
		},
		Install: 2,
	}
	steps := []HarnessSyncStep{{
		Harness: "codex", Action: harness.SyncActionInstall, Status: "applied-unverified",
	}}
	cause := domain.NewEnvelope("verify", domain.ExitConflict, nil, domain.Finding{
		Code: "GDS_HARNESS_VERIFY_FAILED", Severity: domain.SeverityHigh,
	})

	// stoppedAt is index+1 because the first entry was applied.
	envelope := services.haltedConvergence(
		"gds harness sync converge", cause, plan, steps, 1, 0, 1,
	)
	data, ok := envelope.Data.(HarnessSyncConvergeData)
	if !ok {
		t.Fatalf("unexpected payload %T", envelope.Data)
	}
	if data.Applied != 1 {
		t.Fatalf("the applied entry must be counted, got applied=%d", data.Applied)
	}
	if data.Verified != 0 {
		t.Fatalf("nothing was verified, got verified=%d", data.Verified)
	}
	if data.Remaining != 1 {
		t.Fatalf("only the untouched entry remains, got remaining=%d", data.Remaining)
	}
	if !envelope.Mutation.Completed {
		t.Fatal("the target mutated, so the envelope must not deny it")
	}
}

// A plan that fails before apply leaves its entry genuinely unchanged, so that
// entry is still remaining work.
func TestHaltedConvergenceCountsAnUnappliedEntryAsRemaining(t *testing.T) {
	services := &Services{}
	plan := harness.SyncPlan{
		DeviceID: "device_test",
		Entries: []harness.SyncEntry{
			{Harness: "codex", Action: harness.SyncActionInstall},
			{Harness: "zcode", Action: harness.SyncActionInstall},
		},
		Install: 2,
	}
	envelope := services.haltedConvergence(
		"gds harness sync converge",
		domain.NewEnvelope("plan", domain.ExitConflict, nil),
		plan, nil, 0, 0, 0,
	)
	data := envelope.Data.(HarnessSyncConvergeData)
	if data.Applied != 0 || data.Verified != 0 {
		t.Fatalf("nothing ran, got applied=%d verified=%d", data.Applied, data.Verified)
	}
	if data.Remaining != 2 {
		t.Fatalf("both entries remain, got remaining=%d", data.Remaining)
	}
	if envelope.Mutation.Completed {
		t.Fatal("no mutation completed")
	}
}
