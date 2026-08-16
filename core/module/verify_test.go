package module

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func anchorWith(required []string, commands domain.VerificationCommands) domain.RepositoryAnchor {
	return domain.RepositoryAnchor{
		Repository: domain.RepositoryIdentity{ID: "repo_01JEXAMPZ0000000000000000K"},
		Verification: domain.VerificationPolicy{
			Commands: commands, Required: required,
		},
	}
}

func codes(findings []domain.Finding) []string {
	result := make([]string, 0, len(findings))
	for _, finding := range findings {
		result = append(result, finding.Code)
	}
	return result
}

func TestPlanVerificationSelectsOnlyRequiredLanes(t *testing.T) {
	t.Parallel()
	anchor := anchorWith([]string{"lint", "test"}, domain.VerificationCommands{
		Lint:  []string{"go vet ./..."},
		Test:  []string{"go test ./...", "go run ./cmd/check-fuzz"},
		Build: []string{"go build ./..."},
	})
	plan, findings := PlanVerification(anchor, "modules/example", "/tmp/example", "a1b2")
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if len(plan.Lanes) != 2 || plan.Lanes[0].Lane != "lint" || plan.Lanes[1].Lane != "test" {
		t.Fatalf("lanes = %#v", plan.Lanes)
	}
	if len(plan.Lanes[1].Commands) != 2 {
		t.Fatalf("test lane = %#v", plan.Lanes[1])
	}
	// `build` is declared but not required. Running it would verify more than
	// the module says it owes, which is a different claim from the one being
	// checked.
	for _, lane := range plan.Lanes {
		if lane.Lane == "build" {
			t.Fatal("a declared but unrequired lane was selected")
		}
	}
}

// A required lane with no commands is the anchor claiming a gate it does not
// describe. That reads as stronger assurance than exists, which is the same
// direction of error as declaring a command that cannot run.
func TestPlanVerificationRejectsARequiredLaneWithNoCommands(t *testing.T) {
	t.Parallel()
	anchor := anchorWith([]string{"lint", "test"}, domain.VerificationCommands{
		Lint: []string{"go vet ./..."},
	})
	plan, findings := PlanVerification(anchor, "modules/example", "/tmp/example", "a1b2")
	if len(findings) != 1 || findings[0].Code != "GDS_MODULE_VERIFICATION_LANE_EMPTY" {
		t.Fatalf("findings = %#v", codes(findings))
	}
	if findings[0].Evidence["lane"] != "test" {
		t.Fatalf("evidence = %#v", findings[0].Evidence)
	}
	// The coherent half still runs. One incoherent lane is not a reason to stop
	// proving the rest.
	if len(plan.Lanes) != 1 || plan.Lanes[0].Lane != "lint" {
		t.Fatalf("lanes = %#v", plan.Lanes)
	}
}

func TestPlanVerificationRejectsALaneOutsideTheVocabulary(t *testing.T) {
	t.Parallel()
	anchor := anchorWith([]string{"smoke"}, domain.VerificationCommands{
		Test: []string{"go test ./..."},
	})
	_, findings := PlanVerification(anchor, "modules/example", "/tmp/example", "a1b2")
	if len(findings) != 1 || findings[0].Code != "GDS_MODULE_VERIFICATION_LANE_UNKNOWN" {
		t.Fatalf("findings = %#v", codes(findings))
	}
}

// A module that declares commands but requires no lane has said nothing about
// how it is proven. Treating that as "nothing to check" would report success
// for a module nobody verifies.
func TestPlanVerificationRejectsAnAnchorThatRequiresNothing(t *testing.T) {
	t.Parallel()
	anchor := anchorWith(nil, domain.VerificationCommands{Test: []string{"go test ./..."}})
	plan, findings := PlanVerification(anchor, "modules/example", "/tmp/example", "a1b2")
	if len(findings) != 1 || findings[0].Code != "GDS_MODULE_VERIFICATION_UNDECLARED" {
		t.Fatalf("findings = %#v", codes(findings))
	}
	if len(plan.Lanes) != 0 {
		t.Fatalf("lanes = %#v", plan.Lanes)
	}
}

// Every lane in the schema's vocabulary must be reachable. A lane the schema
// accepts and this mapping omits would validate, be required, and silently
// never run -- the exact shape of the defect this command exists to catch.
func TestEveryDeclaredLaneIsReachable(t *testing.T) {
	t.Parallel()
	vocabulary := []string{
		"bootstrap", "lint", "typecheck", "test", "build",
		"compatibility", "package", "fast", "pr-required", "full", "release",
	}
	commands := domain.VerificationCommands{
		Bootstrap: []string{"b"}, Lint: []string{"l"}, Typecheck: []string{"t"},
		Test: []string{"te"}, Build: []string{"bu"}, Compatibility: []string{"c"},
		Package: []string{"p"}, Fast: []string{"f"}, PRRequired: []string{"pr"},
		Full: []string{"fu"}, Release: []string{"r"},
	}
	plan, findings := PlanVerification(
		anchorWith(vocabulary, commands), "modules/example", "/tmp/example", "a1b2",
	)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", codes(findings))
	}
	if len(plan.Lanes) != len(vocabulary) {
		t.Fatalf("selected %d lanes of %d", len(plan.Lanes), len(vocabulary))
	}
}

// `bootstrap` is the one lane the repository schema keeps out of
// `verification.required`, and nothing selected it, so a module could declare it
// and it would never run. That is not harmless: `macos-ubuntu-bootstrap`
// requires `python3 -m pytest` on a clean checkout with nothing installing
// pytest, and had no way to state its own prerequisite.
func TestADeclaredBootstrapLaneRunsFirstAsAPrerequisite(t *testing.T) {
	t.Parallel()
	anchor := anchorWith([]string{"test"}, domain.VerificationCommands{
		Bootstrap: []string{"python3 -m pip install -r requirements-test.txt"},
		Test:      []string{"python3 -m pytest"},
	})
	plan, findings := PlanVerification(anchor, "modules/example", "/tmp/example", "a1b2")
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", codes(findings))
	}
	if len(plan.Lanes) != 2 {
		t.Fatalf("lanes = %#v", plan.Lanes)
	}
	if plan.Lanes[0].Lane != "bootstrap" || !plan.Lanes[0].Prerequisite {
		t.Fatalf("first lane = %#v", plan.Lanes[0])
	}
	// The prerequisite is never itself the proof.
	if plan.Lanes[1].Lane != "test" || plan.Lanes[1].Prerequisite {
		t.Fatalf("second lane = %#v", plan.Lanes[1])
	}
}

// A module that declares no bootstrap keeps exactly its previous plan, so the
// change cannot quietly add work to modules that never asked for it.
func TestAnAnchorWithNoBootstrapSelectsOnlyItsRequiredLanes(t *testing.T) {
	t.Parallel()
	plan, findings := PlanVerification(
		anchorWith([]string{"test"}, domain.VerificationCommands{Test: []string{"go test ./..."}}),
		"modules/example", "/tmp/example", "a1b2",
	)
	if len(findings) != 0 || len(plan.Lanes) != 1 || plan.Lanes[0].Lane != "test" {
		t.Fatalf("lanes = %#v findings = %#v", plan.Lanes, codes(findings))
	}
}

// Preparing a module is not verifying it. A prerequisite with nothing to prepare
// for would otherwise run and report a module as exercised when nothing proved
// anything about it.
func TestABootstrapWithNoRequiredLaneIsNotRunAlone(t *testing.T) {
	t.Parallel()
	plan, findings := PlanVerification(
		anchorWith(nil, domain.VerificationCommands{Bootstrap: []string{"make deps"}}),
		"modules/example", "/tmp/example", "a1b2",
	)
	if len(findings) != 1 || findings[0].Code != "GDS_MODULE_VERIFICATION_UNDECLARED" {
		t.Fatalf("findings = %#v", codes(findings))
	}
	if len(plan.Lanes) != 0 {
		t.Fatalf("lanes = %#v", plan.Lanes)
	}
}

// The schema forbids `bootstrap` in `required`, but an anchor that predates the
// constraint, or a hand-edited one, must not run it twice -- that would let a
// module count preparation as proof.
func TestBootstrapNamedAsRequiredIsNotSelectedTwice(t *testing.T) {
	t.Parallel()
	plan, findings := PlanVerification(
		anchorWith([]string{"bootstrap", "test"}, domain.VerificationCommands{
			Bootstrap: []string{"make deps"}, Test: []string{"go test ./..."},
		}),
		"modules/example", "/tmp/example", "a1b2",
	)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", codes(findings))
	}
	if len(plan.Lanes) != 2 || plan.Lanes[0].Lane != "bootstrap" || plan.Lanes[1].Lane != "test" {
		t.Fatalf("lanes = %#v", plan.Lanes)
	}
}
