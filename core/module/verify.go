package module

import (
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// A module's `verification.commands` is how it tells this control plane which
// commands prove it. Nothing checked that the declaration still runs, so the
// two drifted silently: `ci-workflows` declared `python3 scripts/validate_all.py`
// long after that invocation stopped working, and the failure looked like a
// broken module rather than a stale field.
//
// The selection is separated from the running on purpose. Which lanes a module
// owes, and whether it declares them coherently, is a property of the anchor
// alone and is answerable without executing anything. Only the last question --
// does the command actually succeed -- needs a process, and that is why it lives
// behind an explicit command rather than inside a validator.

// LaneSelection is one selected lane and the commands it declares.
type LaneSelection struct {
	Lane     string   `json:"lane"`
	Commands []string `json:"commands"`
	// Prerequisite marks a lane that establishes the conditions the required
	// lanes need rather than proving anything itself. Its failure is a different
	// statement: the check could not be attempted, not that the module failed it.
	Prerequisite bool `json:"prerequisite,omitempty"`
}

// VerificationPlan is what a single module owes, read from its own anchor.
type VerificationPlan struct {
	GitmodulesName string          `json:"gitmodules_name"`
	Path           string          `json:"path"`
	GitlinkOID     string          `json:"gitlink_oid"`
	RepositoryID   string          `json:"repository_id"`
	Lanes          []LaneSelection `json:"lanes"`
}

// lanesByName keeps the mapping in one place. `verification.required` names
// lanes as strings while the anchor stores them as struct fields, so without
// this the two vocabularies drift the same way the anchor and its CI did.
func lanesByName(commands domain.VerificationCommands) map[string][]string {
	return map[string][]string{
		"bootstrap":     commands.Bootstrap,
		"lint":          commands.Lint,
		"typecheck":     commands.Typecheck,
		"test":          commands.Test,
		"build":         commands.Build,
		"compatibility": commands.Compatibility,
		"package":       commands.Package,
		"fast":          commands.Fast,
		"pr-required":   commands.PRRequired,
		"full":          commands.Full,
		"release":       commands.Release,
	}
}

// PlanVerification reads what a module declares it owes.
//
// A lane named by `verification.required` that carries no commands is reported
// rather than skipped. It is the anchor claiming a gate it does not describe,
// which reads as stronger assurance than exists -- the same direction of error
// as declaring a command that cannot run, arrived at from the other side.
func PlanVerification(
	anchor domain.RepositoryAnchor,
	gitmodulesName string,
	path string,
	gitlinkOID string,
) (VerificationPlan, []domain.Finding) {
	plan := VerificationPlan{
		GitmodulesName: gitmodulesName, Path: path, GitlinkOID: gitlinkOID,
		RepositoryID: anchor.Repository.ID, Lanes: []LaneSelection{},
	}
	findings := []domain.Finding{}
	available := lanesByName(anchor.Verification.Commands)

	// `bootstrap` is the one lane `schemas/v1/repository.schema.json` keeps out
	// of `verification.required`, and until now nothing selected it, so a module
	// could declare it and it would never run. That is not a harmless omission:
	// `macos-ubuntu-bootstrap` requires `python3 -m pytest` on a clean checkout
	// with nothing installing pytest, and a module in that shape has no way to
	// state its own prerequisite. Declaring it is now how it gets established.
	//
	// It runs first and is never itself the proof. Keeping it out of `required`
	// is right: a module is not verified by having been prepared.
	if bootstrap := available["bootstrap"]; len(bootstrap) != 0 {
		plan.Lanes = append(plan.Lanes, LaneSelection{
			Lane: "bootstrap", Commands: bootstrap, Prerequisite: true,
		})
	}

	if len(anchor.Verification.Required) == 0 {
		findings = append(findings, domain.Finding{
			Code: "GDS_MODULE_VERIFICATION_UNDECLARED", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf(
				"Module %q declares no required verification lane, so nothing states how it is proven.",
				gitmodulesName,
			),
			Evidence: map[string]any{
				"gitmodules_name": gitmodulesName, "repository_id": anchor.Repository.ID,
			},
		})
		// A prerequisite with nothing to prepare for is not work worth doing, and
		// running it alone would report a module as exercised when nothing
		// verified it.
		plan.Lanes = []LaneSelection{}
		return plan, findings
	}

	for _, lane := range anchor.Verification.Required {
		if lane == "bootstrap" {
			// Already selected as a prerequisite, and the schema does not permit
			// it here. Selecting it twice would run it twice and let a module
			// claim preparation as proof.
			continue
		}
		commands, known := available[lane]
		if !known {
			findings = append(findings, domain.Finding{
				Code: "GDS_MODULE_VERIFICATION_LANE_UNKNOWN", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf(
					"Module %q requires lane %q, which is not part of the verification vocabulary.",
					gitmodulesName, lane,
				),
				Evidence: map[string]any{"gitmodules_name": gitmodulesName, "lane": lane},
			})
			continue
		}
		if len(commands) == 0 {
			findings = append(findings, domain.Finding{
				Code: "GDS_MODULE_VERIFICATION_LANE_EMPTY", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf(
					"Module %q requires lane %q and declares no command for it.",
					gitmodulesName, lane,
				),
				Evidence: map[string]any{"gitmodules_name": gitmodulesName, "lane": lane},
			})
			continue
		}
		plan.Lanes = append(plan.Lanes, LaneSelection{Lane: lane, Commands: commands})
	}
	return plan, findings
}
