package harness

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func actionOf(plan SyncPlan, id string) string {
	for _, entry := range plan.Entries {
		if entry.Harness == id {
			return entry.Action
		}
	}
	return ""
}

func codeCount(plan SyncPlan) (int, int, int) {
	return plan.Install, plan.Update, plan.Remove
}

func TestBuildSyncPlanClassifiesEveryTransition(t *testing.T) {
	canonical := []string{"codex", "cursor-cli", "opencode", "pi", "zcode"}
	plan, findings := BuildSyncPlan(
		"device_test",
		canonical,
		[]string{"codex", "cursor-cli", "zcode"},
		[]SyncObservation{
			{Harness: "codex", Present: true, Drift: 0},
			{Harness: "zcode", Present: true, Drift: 3},
			{Harness: "opencode", Present: true, Drift: 0},
		},
	)
	if got := actionOf(plan, "cursor-cli"); got != SyncActionInstall {
		t.Fatalf("selected and absent must install, got %q", got)
	}
	if got := actionOf(plan, "zcode"); got != SyncActionUpdate {
		t.Fatalf("selected and drifted must update, got %q", got)
	}
	if got := actionOf(plan, "opencode"); got != SyncActionRemove {
		t.Fatalf("deselected and present must remove, got %q", got)
	}
	if got := actionOf(plan, "codex"); got != SyncActionCurrent {
		t.Fatalf("selected and clean must be current, got %q", got)
	}
	// Neither selected nor installed is not this device's concern.
	if got := actionOf(plan, "pi"); got != "" {
		t.Fatalf("unselected and absent must not appear, got %q", got)
	}
	if install, update, remove := codeCount(plan); install != 1 || update != 1 || remove != 1 {
		t.Fatalf("counts = %d/%d/%d", install, update, remove)
	}
	if !hasFinding(findings, "GDS_HARNESS_SELECTION_DRIFT") {
		t.Fatalf("pending actions must report drift, got %+v", findings)
	}
}

func TestBuildSyncPlanOrdersRemovalsBeforeInstalls(t *testing.T) {
	plan, _ := BuildSyncPlan(
		"device_test",
		[]string{"codex", "opencode"},
		[]string{"codex"},
		[]SyncObservation{{Harness: "opencode", Present: true}},
	)
	if len(plan.Entries) != 2 || plan.Entries[0].Action != SyncActionRemove {
		t.Fatalf("removals must be planned first, got %+v", plan.Entries)
	}
}

func TestBuildSyncPlanIsQuietWhenConverged(t *testing.T) {
	plan, findings := BuildSyncPlan(
		"device_test",
		[]string{"codex", "zcode", "pi"},
		[]string{"codex", "zcode"},
		[]SyncObservation{
			{Harness: "codex", Present: true},
			{Harness: "zcode", Present: true},
		},
	)
	if plan.Pending() != 0 {
		t.Fatalf("converged device must have no pending action, got %+v", plan)
	}
	if len(findings) != 0 {
		t.Fatalf("converged device must report nothing, got %+v", findings)
	}
}

func TestBuildSyncPlanRejectsUnknownSelectionWithoutScheduling(t *testing.T) {
	plan, findings := BuildSyncPlan(
		"device_test",
		[]string{"codex"},
		[]string{"codex", "cursor-cli-app"},
		[]SyncObservation{{Harness: "codex", Present: true}},
	)
	if !hasFinding(findings, "GDS_HARNESS_SELECTED_UNKNOWN") {
		t.Fatalf("unknown selection must be reported, got %+v", findings)
	}
	if plan.Pending() != 0 {
		t.Fatalf("unknown selection must schedule nothing, got %+v", plan)
	}
	for _, entry := range plan.Entries {
		if entry.Harness == "cursor-cli-app" {
			t.Fatalf("unknown harness must not become an entry")
		}
	}
}

func TestBuildSyncPlanTreatsMissingObservationAsAbsent(t *testing.T) {
	plan, _ := BuildSyncPlan("device_test", []string{"codex"}, []string{"codex"}, nil)
	if got := actionOf(plan, "codex"); got != SyncActionInstall {
		t.Fatalf("absent observation must install, never remove, got %q", got)
	}
}

func hasFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestBuildSyncPlanRefusesToActOnAnEmptySelection(t *testing.T) {
	// A blank `harnesses:` must never be read as "remove everything installed".
	plan, findings := BuildSyncPlan(
		"device_test",
		[]string{"codex", "zcode"},
		nil,
		[]SyncObservation{
			{Harness: "codex", Present: true},
			{Harness: "zcode", Present: true},
		},
	)
	if !hasFinding(findings, "GDS_HARNESS_SELECTION_EMPTY") {
		t.Fatalf("expected GDS_HARNESS_SELECTION_EMPTY, got %+v", findings)
	}
	if plan.Remove != 0 || len(plan.Entries) != 0 {
		t.Fatalf("empty selection must schedule nothing, got %+v", plan)
	}
}

func TestBuildSyncPlanKeepsDriftedSelectionAsUpdateNotReinstall(t *testing.T) {
	// Converge maps update to the update transaction, which preserves the
	// previously installed candidate; misclassifying it as install would hit the
	// adapter's own collision guard instead.
	plan, _ := BuildSyncPlan(
		"device_test", []string{"codex"}, []string{"codex"},
		[]SyncObservation{{Harness: "codex", Present: true, Drift: 2}},
	)
	if got := actionOf(plan, "codex"); got != SyncActionUpdate {
		t.Fatalf("drifted selection must update, got %q", got)
	}
}

func TestBuildSyncPlanNeverRemovesWhatItCannotSee(t *testing.T) {
	// The safety property behind both the lock-file presence rule and the
	// unobservable handling: an entry with no observation must never be planned
	// for removal, because removing what cannot be observed is unrecoverable.
	plan, _ := BuildSyncPlan(
		"device_test",
		[]string{"codex", "opencode", "pi"},
		[]string{"codex"},
		[]SyncObservation{{Harness: "codex", Present: true}},
	)
	for _, entry := range plan.Entries {
		if entry.Action == SyncActionRemove {
			t.Fatalf("unobserved entry must not be removed: %+v", entry)
		}
	}
	if plan.Remove != 0 {
		t.Fatalf("expected no removals, got %d", plan.Remove)
	}
}

// The fixture the older unknown-selection test lacked: a *known and absent*
// entry alongside the unknown one. With codex already present and clean the plan
// was empty for an unrelated reason, so per-id skipping passed a test whose name
// promised plan-wide suppression.
func TestBuildSyncPlanSchedulesNothingWhenAnySelectionIsUnknown(t *testing.T) {
	plan, findings := BuildSyncPlan(
		"device_test",
		[]string{"codex", "zcode"},
		[]string{"codex", "cursor-cli-app"},
		[]SyncObservation{{Harness: "codex", Present: false}},
	)
	if !hasFinding(findings, "GDS_HARNESS_SELECTED_UNKNOWN") {
		t.Fatalf("unknown selection must be reported, got %+v", findings)
	}
	if len(plan.Entries) != 0 || plan.Pending() != 0 {
		t.Fatalf("an unreadable selection must schedule nothing, got %+v", plan)
	}
	if hasFinding(findings, "GDS_HARNESS_SELECTION_DRIFT") {
		t.Fatal("a suppressed plan must not also claim drift")
	}
}

func TestDetectTargetCollisionsFindsTwoSelectedClaimingOnePath(t *testing.T) {
	collisions := DetectTargetCollisions(map[string][]string{
		"codex":    {".agents/skills/review/SKILL.md", ".codex/config.toml"},
		"opencode": {".agents/skills/review/SKILL.md", ".opencode/config.json"},
		"zcode":    {".zcode/skills/review/SKILL.md"},
	})
	if len(collisions) != 1 {
		t.Fatalf("exactly one shared path, got %+v", collisions)
	}
	if collisions[0].Path != ".agents/skills/review/SKILL.md" {
		t.Fatalf("wrong path %q", collisions[0].Path)
	}
	if len(collisions[0].Harnesses) != 2 ||
		collisions[0].Harnesses[0] != "codex" || collisions[0].Harnesses[1] != "opencode" {
		t.Fatalf("both owners must be named in order, got %+v", collisions[0].Harnesses)
	}
}

// Nothing is installed on an empty target root, so a check that only asks
// "is this path already taken" reports no conflict. Desired-state comparison is
// what makes the empty-root case detectable at all.
func TestDetectTargetCollisionsIsIndependentOfInstalledState(t *testing.T) {
	if got := DetectTargetCollisions(map[string][]string{
		"codex": {".agents/skills/review/SKILL.md"},
		"zcode": {".zcode/skills/review/SKILL.md"},
	}); len(got) != 0 {
		t.Fatalf("separate roots coexist, got %+v", got)
	}
	if got := DetectTargetCollisions(map[string][]string{"codex": {"a", "b"}}); len(got) != 0 {
		t.Fatalf("a sole owner is not a collision, got %+v", got)
	}
}
