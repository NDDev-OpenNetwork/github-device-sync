package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	moduleworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/module"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/redaction"
)

// ModuleVerifyOptions bounds an execution, because this is the one GDS surface
// that runs commands it did not write.
type ModuleVerifyOptions struct {
	Module         string
	CommandTimeout time.Duration
}

type ModuleVerifyData struct {
	Modules []ModuleVerification `json:"modules"`
}

type ModuleVerification struct {
	GitmodulesName string       `json:"gitmodules_name"`
	Path           string       `json:"path"`
	GitlinkOID     string       `json:"gitlink_oid"`
	RepositoryID   string       `json:"repository_id,omitempty"`
	Lanes          []LaneReport `json:"lanes"`
}

type LaneReport struct {
	Lane     string          `json:"lane"`
	Commands []CommandReport `json:"commands"`
}

type CommandReport struct {
	Command    string `json:"command"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	// Diagnostic is the tail of stderr, bounded and redacted. It is the whole
	// difference between a usable report and a guess: a failing command may be a
	// defect in the module or a tool missing from this device, and nothing in
	// the exit code separates those. `python3 -m pytest` exiting 1 in 14ms with
	// "No module named pytest" is not a broken module, and a reader must be able
	// to see that without rerunning anything.
	Diagnostic string `json:"diagnostic,omitempty"`
}

const defaultModuleCommandTimeout = 10 * time.Minute

// VerifyModules runs each declared module's required verification lanes against
// a clean checkout of the commit this repository actually pins.
//
// It is a command rather than a validator, and the distinction is the point.
// Every other check here reasons about the estate without running it, which is
// why a module could declare `python3 scripts/validate_all.py` for as long as it
// liked after that invocation stopped working: no amount of static reasoning
// reaches a `ModuleNotFoundError` raised at import time. Answering "does the
// declared command still work" requires running it, so this executes, says so,
// and stays out of the validators.
//
// The subject is the indexed gitlink rather than whatever the module's worktree
// currently holds. That is the commit this repository consumes and ships
// against; the worktree may sit on somebody's task branch, and verifying that
// would answer a question nobody asked.
func (services *Services) VerifyModules(
	ctx context.Context,
	path string,
	options ModuleVerifyOptions,
) domain.Envelope {
	const command = "gds module verify"
	_, consumer, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError(command, path, err)
	}
	topology, err := services.Git.InspectTopology(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError(command, path, err)
	}
	submodules := map[string]string{}
	paths := map[string]string{}
	for _, submodule := range topology.Submodules {
		if submodule.Name == "" {
			continue
		}
		submodules[submodule.Name] = submodule.GitlinkOID
		paths[submodule.Name] = submodule.Path
	}

	timeout := options.CommandTimeout
	if timeout <= 0 {
		timeout = defaultModuleCommandTimeout
	}
	selected := strings.TrimSpace(options.Module)
	data := ModuleVerifyData{Modules: []ModuleVerification{}}
	matched := false

	for _, relationship := range consumer.Relationships {
		if relationship.Type != "git-submodule-consumer" {
			continue
		}
		name := relationship.GitmodulesName
		if selected != "" && selected != name {
			continue
		}
		matched = true
		gitlink, configured := submodules[name]
		modulePath := filepath.Join(info.WorktreeRoot, filepath.FromSlash(paths[name]))
		if !configured || gitlink == "" {
			findings = append(findings, domain.Finding{
				Code: "GDS_MODULE_VERIFICATION_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  "A declared module has no stage-zero gitlink to verify against.",
				Evidence: map[string]any{"gitmodules_name": name},
			})
			continue
		}
		if _, err := os.Stat(filepath.Join(modulePath, ".git")); err != nil {
			// Not checked out is not a failure of the module. It is evidence GDS
			// does not have, and saying so is different from saying it passed.
			findings = append(findings, domain.Finding{
				Code: "GDS_MODULE_VERIFICATION_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message: "A declared module is not checked out, so its verification lanes were not run.",
				Evidence: map[string]any{
					"gitmodules_name": name, "path": modulePath, "gitlink_oid": gitlink,
				},
			})
			continue
		}
		anchor, anchorFindings := manifest.NewLoader(services.Schemas).LoadRepository(modulePath)
		if len(anchorFindings) != 0 {
			findings = append(findings, anchorFindings...)
			continue
		}
		plan, planFindings := moduleworkflow.PlanVerification(anchor, name, modulePath, gitlink)
		findings = append(findings, planFindings...)
		if len(plan.Lanes) == 0 {
			continue
		}
		report, runFindings := services.runModuleLanes(ctx, modulePath, plan, timeout)
		findings = append(findings, runFindings...)
		data.Modules = append(data.Modules, report)
	}

	if selected != "" && !matched {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_MODULE_VERIFICATION_SELECTION_UNKNOWN", Severity: domain.SeverityHigh,
			Message:  "No declared git-submodule-consumer relationship carries that .gitmodules name.",
			Evidence: map[string]any{"module": selected},
		})
	}
	envelope := domain.NewEnvelope(command, classifyFindings(findings), data, findings...)
	envelope.Scope["repository_id"] = consumer.Repository.ID
	return envelope
}

// runModuleLanes executes one module's lanes in a throwaway worktree of its
// pinned commit.
//
// The worktree is why this can run at all while another agent is working in the
// module: it never touches their checkout, their branch or their index. It is
// removed on every path out, including failure, because a stray registration in
// somebody else's Git store is a worse outcome than an unverified lane.
func (services *Services) runModuleLanes(
	ctx context.Context,
	modulePath string,
	plan moduleworkflow.VerificationPlan,
	timeout time.Duration,
) (ModuleVerification, []domain.Finding) {
	report := ModuleVerification{
		GitmodulesName: plan.GitmodulesName, Path: plan.Path,
		GitlinkOID: plan.GitlinkOID, RepositoryID: plan.RepositoryID,
		Lanes: []LaneReport{},
	}
	findings := []domain.Finding{}

	workspace, err := os.MkdirTemp("", "gds-module-verify-")
	if err != nil {
		return report, append(findings, domain.Finding{
			Code: "GDS_MODULE_VERIFICATION_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  "A private workspace for the pinned checkout could not be created.",
			Evidence: map[string]any{"gitmodules_name": plan.GitmodulesName},
		})
	}
	defer os.RemoveAll(workspace)
	checkout := filepath.Join(workspace, "checkout")

	if err := services.GitMutations.AddDetachedWorktree(
		ctx, modulePath, checkout, plan.GitlinkOID,
	); err != nil {
		return report, append(findings, domain.Finding{
			Code: "GDS_MODULE_VERIFICATION_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "The pinned commit could not be materialized for verification.",
			Evidence: map[string]any{
				"gitmodules_name": plan.GitmodulesName, "gitlink_oid": plan.GitlinkOID,
			},
		})
	}
	defer func() {
		_ = services.GitMutations.RemoveWorktree(ctx, modulePath, checkout)
	}()

	for _, lane := range plan.Lanes {
		laneReport := LaneReport{Lane: lane.Lane, Commands: []CommandReport{}}
		failed := false
		for _, declared := range lane.Commands {
			result := runDeclaredCommand(ctx, checkout, declared, timeout)
			laneReport.Commands = append(laneReport.Commands, result)
			if result.Status == "passed" {
				continue
			}
			failed = true
			// Deliberately not "the module is broken". The command did not
			// succeed here, and whether that is the module or this device is what
			// the diagnostic says; asserting more than was observed is how a
			// report starts being wrong.
			//
			// A prerequisite failing says something different again: the checks
			// were never attempted. Reporting that as a failed verification would
			// read as "the module does not pass", which was not observed.
			code := "GDS_MODULE_VERIFICATION_COMMAND_FAILED"
			message := "A declared verification command did not succeed in a clean checkout " +
				"of the pinned commit on this device."
			if lane.Prerequisite {
				code = "GDS_MODULE_VERIFICATION_BOOTSTRAP_FAILED"
				message = "A declared verification prerequisite did not succeed on this device, " +
					"so the required lanes were not attempted."
			}
			findings = append(findings, domain.Finding{
				Code: code, Severity: domain.SeverityHigh, Message: message,
				Evidence: map[string]any{
					"gitmodules_name": plan.GitmodulesName, "gitlink_oid": plan.GitlinkOID,
					"lane": lane.Lane, "command": declared,
					"status": result.Status, "exit_code": result.ExitCode,
					"diagnostic": result.Diagnostic,
				},
			})
		}
		report.Lanes = append(report.Lanes, laneReport)
		if failed && lane.Prerequisite {
			// Running the required lanes against an unprepared checkout produces
			// failures that describe the missing preparation, not the module.
			return report, findings
		}
	}
	return report, findings
}

// runDeclaredCommand runs one declared command the way CI does.
//
// `bash -euo pipefail -c` matches the reusable workflows this estate calls, so a
// command that passes here and fails there is a difference in the command rather
// than in how it was invoked. A timeout distinguishes "took too long" from
// "failed", because reporting a hung command as a failure would send whoever
// reads it looking for a defect that is not there.
func runDeclaredCommand(
	ctx context.Context,
	directory string,
	declared string,
	timeout time.Duration,
) CommandReport {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	command := exec.CommandContext(bounded, "bash", "-euo", "pipefail", "-c", declared)
	command.Dir = directory
	command.Stdin = nil
	diagnostic := &strings.Builder{}
	command.Stderr = diagnostic
	err := command.Run()
	report := CommandReport{
		Command: declared, Status: "passed",
		DurationMS: time.Since(started).Milliseconds(),
	}
	if err == nil {
		return report
	}
	report.Diagnostic = boundedDiagnostic(diagnostic.String())
	if errors.Is(bounded.Err(), context.DeadlineExceeded) {
		report.Status = "timeout"
		report.ExitCode = -1
		return report
	}
	report.Status = "failed"
	report.ExitCode = command.ProcessState.ExitCode()
	return report
}

// boundedDiagnostic keeps the last lines of stderr, redacted and bounded.
//
// The tail rather than the head: a failing build prints its progress first and
// its reason last, so the head is the part nobody needs. The bound exists
// because this text lands in a result envelope that has a size contract, and
// the redaction because a module's own output is not something GDS has vetted.
func boundedDiagnostic(text string) string {
	const maxDiagnostic = 1200
	trimmed := strings.TrimSpace(redaction.String(text))
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= maxDiagnostic {
		return trimmed
	}
	tail := trimmed[len(trimmed)-maxDiagnostic:]
	if cut := strings.IndexByte(tail, '\n'); cut >= 0 && cut+1 < len(tail) {
		tail = tail[cut+1:]
	}
	return "...\n" + tail
}
