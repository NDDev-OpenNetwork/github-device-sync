package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// `gds module update-pin` refused every module that declared a required check,
// with "Module required checks have no verified execution evidence" and no way
// to supply any. Every module in this estate declares at least one, so the
// command could not advance a single pin. The happy-path test did not catch it:
// its module fixture declared no verification, which is the one shape the
// refusal let through and the one no real module has.
//
// These read the source rather than driving the saga, because the saga needs a
// consumer worktree, an uninitialized gitlink, a separate module checkout on its
// published default branch, and a signed approval. What can be asserted cheaply
// is that neither dead end is still in the code, and that is exactly what
// regressed unseen before.
func modulePinSource(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "module_pin.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestModulePinDoesNotRefuseEveryModuleThatDeclaresACheck(t *testing.T) {
	t.Parallel()
	source := modulePinSource(t)
	if strings.Contains(source, "len(moduleAnchor.Verification.Required) != 0") {
		t.Fatal("the blanket required-checks refusal is back; no module could ever be repinned")
	}
	// The refusal is only meaningful if something now produces the evidence.
	if !strings.Contains(source, "runModuleLanes") ||
		!strings.Contains(source, "moduleworkflow.PlanVerification") {
		t.Fatal("the pin no longer gathers verification evidence")
	}
	// And the plan must be bound to it, or an approval could outlive the green
	// run that justified it.
	if !strings.Contains(source, "verificationDigest") {
		t.Fatal("the plan fingerprint is not bound to the verification evidence")
	}
}

// `LocalPushSupported` asks whether this device may push to the module's remote.
// The pin never writes to the module -- the only mutation is a gitlink rewrite
// in the consumer -- and the guard refuses every remote that is not a local
// path, so on a real estate it refused every module.
func TestModulePinDoesNotGateAReadOnAPushCapability(t *testing.T) {
	t.Parallel()
	// The call, not the word: the comment that records why it left names it.
	if strings.Contains(modulePinSource(t), "GitMutations.LocalPushSupported(") {
		t.Fatal("the pin gates its remote observation on push capability again")
	}
}

// The report carries per-command durations, so digesting it directly makes the
// plan stale the moment apply re-observes -- which re-runs the lanes. The claim
// is which commands passed, not how long they took, and only the claim may enter
// the fingerprint.
func TestTheVerifiedOutcomeIgnoresEverythingThatChangesBetweenRuns(t *testing.T) {
	t.Parallel()
	first := ModuleVerification{
		GitlinkOID: "a1b2", Lanes: []LaneReport{{
			Lane: "test", Commands: []CommandReport{
				{Command: "go test ./...", Status: "passed", ExitCode: 0, DurationMS: 1204},
			},
		}},
	}
	second := first
	second.Lanes = []LaneReport{{
		Lane: "test", Commands: []CommandReport{
			{Command: "go test ./...", Status: "passed", ExitCode: 0, DurationMS: 9871,
				Diagnostic: "a warning nobody acted on"},
		},
	}}

	if got, want := verifiedOutcome(second), verifiedOutcome(first); len(got) != len(want) {
		t.Fatalf("outcome = %#v vs %#v", got, want)
	} else {
		for index := range got {
			if got[index] != want[index] {
				t.Fatalf("outcome differs at %d: %q vs %q", index, got[index], want[index])
			}
		}
	}

	// A different status is a different claim and must change the digest.
	failed := first
	failed.Lanes = []LaneReport{{
		Lane: "test", Commands: []CommandReport{
			{Command: "go test ./...", Status: "failed", ExitCode: 1},
		},
	}}
	if verifiedOutcome(failed)[1] == verifiedOutcome(first)[1] {
		t.Fatal("a failed command produced the same outcome as a passing one")
	}

	// So is a different commit: the same commands passing elsewhere prove nothing
	// about the commit being pinned.
	elsewhere := first
	elsewhere.GitlinkOID = "c3d4"
	if verifiedOutcome(elsewhere)[0] == verifiedOutcome(first)[0] {
		t.Fatal("the outcome does not distinguish the commit it was observed at")
	}
}
