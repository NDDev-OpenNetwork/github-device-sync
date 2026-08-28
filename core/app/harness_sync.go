package app

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/materialize"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/workspace"
)

type HarnessSyncOptions struct {
	Path         string
	DevicePath   string
	TargetRoot   string
	SkillProfile string
	Scope        string
}

type HarnessSyncData struct {
	Device workspace.DeviceCandidate `json:"device_descriptor"`
	Plan   harness.SyncPlan          `json:"sync_plan"`
	Target string                    `json:"target_root"`
	// Unobservable names unselected catalogue entries whose adapter could not be
	// built or inspected, so their installed state is unknown.
	Unobservable []string `json:"unobservable"`
}

// ReconcileDeviceHarnesses reports how the harness projections installed at a
// device target differ from the set the device descriptor declares.
//
// It is read-only. The reconciliation it returns names, per harness, which of
// the existing single-harness transactions (`gds harness install|update|remove`)
// would converge the device; running them stays an explicit, approval-gated act.
func (services *Services) ReconcileDeviceHarnesses(
	ctx context.Context,
	options HarnessSyncOptions,
) domain.Envelope {
	const command = "gds harness sync"
	if strings.TrimSpace(options.DevicePath) == "" ||
		strings.TrimSpace(options.TargetRoot) == "" ||
		strings.TrimSpace(options.SkillProfile) == "" ||
		strings.TrimSpace(options.Scope) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_HARNESS_SYNC_SCOPE_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Sync requires a device descriptor, target root, skill profile, and projection scope.",
		})
	}
	info, err := services.Git.RepositoryInfo(ctx, options.Path)
	if err != nil {
		return envelopeForError(command, options.Path, err)
	}
	device, findings := workspace.LoadDeviceCandidate(options.DevicePath, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	request := harness.RenderRequest{
		SkillProfile: options.SkillProfile, Scope: options.Scope,
	}
	wanted := make(map[string]bool, len(device.Descriptor.Harnesses))
	for _, id := range device.Descriptor.Harnesses {
		wanted[id] = true
	}
	observations := make([]harness.SyncObservation, 0, len(harness.CanonicalIDs))
	unobservable := []string{}
	collisions := []map[string]any{}
	// What each selected harness *wants* to own, independent of what the target
	// currently holds. Two selected harnesses that share a skill root collide on
	// an empty root too, where no on-disk evidence of the conflict exists yet.
	claims := map[string][]string{}
	for _, id := range harness.CanonicalIDs {
		// A selected harness that cannot be rendered is a hard error: the device
		// cannot converge on it. An unselected one is skipped instead, so an
		// incomplete catalogue entry never blocks a device that does not run it —
		// the same separation ValidateSelected already makes. It is recorded, not
		// swallowed: an unobservable entry could be a leftover install, and
		// removing what cannot be seen is never safe.
		adapter, adapterFindings := harness.NewAdapter(info.WorktreeRoot, id, services.Schemas)
		if len(adapterFindings) != 0 {
			if wanted[id] {
				return domain.NewEnvelope(
					command, classifyFindings(adapterFindings), nil, adapterFindings...,
				)
			}
			if harnessMarkerIsAbsent(options.TargetRoot, id, options.SkillProfile) {
				observations = append(observations, harness.SyncObservation{Harness: id})
				continue
			}
			unobservable = append(unobservable, id)
			continue
		}
		inspection, inspectFindings := adapter.Inspect(options.TargetRoot, request)
		if len(inspectFindings) != 0 {
			if wanted[id] {
				return domain.NewEnvelope(
					command, classifyFindings(inspectFindings), nil, inspectFindings...,
				)
			}
			if harnessMarkerIsAbsent(options.TargetRoot, id, options.SkillProfile) {
				observations = append(observations, harness.SyncObservation{Harness: id})
				continue
			}
			unobservable = append(unobservable, id)
			continue
		}
		// Presence must be decided by a file only this harness owns. Adapters
		// share their skill tree — `.agents/skills/...` is the standard AGENTS
		// root — so "any owned file exists" reports every harness as installed as
		// soon as one of them is, and would then schedule removals that delete the
		// shared files the selected harnesses still need. Each adapter writes
		// exactly one unique marker, `.gds/harness/<id>-<profile>.lock.json`, so
		// that is the signal.
		lockPath := path.Join(".gds", "harness", id+"-"+options.SkillProfile+".lock.json")
		present := false
		for _, file := range inspection.Files {
			if file.Path == lockPath && file.State != "missing" {
				present = true
				break
			}
		}
		// An install refuses to overwrite any managed path it does not already
		// own, so a selected-but-absent harness whose files are already on disk
		// cannot be installed at this target. Two harnesses that share a skill
		// root — `.agents/skills` is the AGENTS standard, so codex and opencode
		// collide while claude-code under `.claude/skills` does not — cannot occupy one
		// target root. Say so here, while the command is still read-only, rather
		// than let the owner discover it half-way through a mutating run.
		if wanted[id] {
			owned := make([]string, 0, len(inspection.Files))
			for _, file := range inspection.Files {
				if file.Path != lockPath {
					owned = append(owned, file.Path)
				}
			}
			claims[id] = owned
		}
		if wanted[id] && !present {
			for _, file := range inspection.Files {
				if file.Path != lockPath && file.State != "missing" {
					collisions = append(collisions, map[string]any{
						"harness": id, "path": file.Path,
					})
					break
				}
			}
		}
		observations = append(observations, harness.SyncObservation{
			Harness: id, Present: present, Drift: inspection.Drift,
		})
	}
	plan, planFindings := harness.BuildSyncPlan(
		device.Descriptor.Device.ID, harness.CanonicalIDs,
		device.Descriptor.Harnesses, observations,
	)
	// Two kinds of conflict end the same way — the selection cannot occupy this
	// root — so they are one finding. `collisions` is a selected harness meeting
	// a path something else already owns; `shared` is two selected harnesses
	// wanting the same path, which is true before either is installed and is the
	// only one an empty target root can show.
	shared := harness.DetectTargetCollisions(claims)
	if len(collisions) != 0 || len(shared) != 0 {
		planFindings = append(planFindings, domain.Finding{
			Code: "GDS_HARNESS_TARGET_COLLISION", Severity: domain.SeverityHigh,
			Message: "A selected harness cannot be installed at this target: another " +
				"projection already owns a path it needs, or two selected harnesses " +
				"claim one. Harnesses that share a skill root cannot occupy one " +
				"target root.",
			Evidence: map[string]any{
				"device_id":  device.Descriptor.Device.ID,
				"collisions": collisions, "shared_targets": shared,
			},
		})
	}
	if len(unobservable) != 0 {
		planFindings = append(planFindings, domain.Finding{
			Code: "GDS_HARNESS_UNOBSERVABLE_NOT_PROVEN", Severity: domain.SeverityMedium,
			Message: "Some unselected catalogue entries could not be observed; " +
				"a leftover install of one of them would not be reported.",
			Evidence: map[string]any{"harnesses": unobservable, "device_id": device.Descriptor.Device.ID},
		})
	}
	envelope := domain.NewEnvelope(command, classifyFindings(planFindings), HarnessSyncData{
		Device: device, Plan: plan, Target: options.TargetRoot,
		Unobservable: unobservable,
	}, planFindings...)
	envelope.Scope["device_id"] = device.Descriptor.Device.ID
	return envelope
}

// harnessMarkerIsAbsent safely proves that GDS never installed one adapter at
// this target even when its provisional profile cannot currently render. A GDS
// installation always owns one unique lock marker. Absence therefore closes
// the leftover-install question; any present, unreadable, or non-regular marker
// remains unobservable and fail-closed because its managed file set cannot be
// reconstructed without the adapter candidate.
func harnessMarkerIsAbsent(targetRoot string, id string, skillProfile string) bool {
	if id == "" || skillProfile == "" || strings.ContainsAny(id+skillProfile, "/\\") {
		return false
	}
	lockPath := path.Join(".gds", "harness", id+"-"+skillProfile+".lock.json")
	set, err := materialize.NewSet(targetRoot, []materialize.File{{Path: lockPath}})
	if err != nil {
		return false
	}
	observed, err := set.Observe()
	return err == nil && len(observed) == 1 && observed[0].State == "missing"
}

// HarnessSyncConvergeOptions extends a reconciliation with the identity and
// approval a mutating run needs.
type HarnessSyncConvergeOptions struct {
	HarnessSyncOptions
	ProjectionOperationOptions
}

// HarnessSyncStep records what one classified entry actually did.
type HarnessSyncStep struct {
	Harness     string `json:"harness"`
	Action      string `json:"action"`
	PlanID      string `json:"plan_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	Status      string `json:"status"`
}

// HarnessSyncConvergeData separates the two truths a partial run must not
// conflate. Applied counts entries whose mutation reached the target; Verified
// counts the subset that was afterwards confirmed. They differ by exactly the
// entry whose verification failed, and an operator recovering from that failure
// needs to know the target already changed.
type HarnessSyncConvergeData struct {
	Plan      harness.SyncPlan  `json:"sync_plan"`
	Steps     []HarnessSyncStep `json:"steps"`
	Applied   int               `json:"applied"`
	Verified  int               `json:"verified"`
	Remaining int               `json:"remaining"`
}

// ConvergeDeviceHarnesses brings one device to its declared harness selection.
//
// It is deliberately a *sequence* of the existing single-harness transactions,
// not one atomic plan. The operations engine resolves an action handler by action
// name, and every harness install shares the action `materialize-harness-adapter`
// while each handler is bound to one exact target and candidate — so a multi-step
// plan would apply the first harness's projection for every later step of the same
// action. Sequencing keeps each mutation inside the transaction that is already
// proven, approval-gated, and idempotent.
//
// The cost is stated rather than hidden: convergence is not atomic. A failure
// stops the run, everything already applied stays applied, and the remaining work
// is reported. Re-running is safe and resumes, because an entry that converged is
// classified `current` on the next pass and is not repeated.
func (services *Services) ConvergeDeviceHarnesses(
	ctx context.Context,
	options HarnessSyncConvergeOptions,
) domain.Envelope {
	const command = "gds harness sync converge"
	if combinedHarnessConvergenceRemoved() {
		return domain.NewEnvelope(command, domain.ExitUnsupported, nil, domain.Finding{
			Code: "GDS_HARNESS_SYNC_CONVERGE_REMOVED", Severity: domain.SeverityHigh,
			Message: "Combined convergence is removed because one approval cannot authorize multiple internally generated exact plans. Use explicit per-harness transactions.",
		})
	}
	// Legacy implementation below is intentionally unreachable until it is
	// deleted with the next internal API break. Keeping the types for one release
	// preserves source compatibility while every callable path fails closed.
	if strings.TrimSpace(options.ApprovalReference) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_HARNESS_SYNC_APPROVAL_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Converging a device mutates it and requires an exact approval reference.",
		})
	}
	reconciled := services.ReconcileDeviceHarnesses(ctx, options.HarnessSyncOptions)
	// Drift is the reason to converge, not a reason to refuse. Every other
	// finding refuses, including an unobservable unselected entry: convergence
	// claims the whole device reached its declared selection, and an entry whose
	// installed state could not be read is exactly the evidence that claim needs.
	// Reporting success while a leftover projection may still sit at the target
	// would be asserting something this run did not establish.
	// Findings that do not refuse the run but do bound what its result may
	// claim. They travel into the final envelope instead of being dropped.
	unproven := []domain.Finding{}
	for _, finding := range reconciled.Findings {
		if convergenceBlockedBy(finding.Code) {
			reconciled.Command = command
			return reconciled
		}
		if finding.Code == "GDS_HARNESS_UNOBSERVABLE_NOT_PROVEN" {
			unproven = append(unproven, finding)
		}
	}
	if reconciled.Data == nil {
		reconciled.Command = command
		return reconciled
	}
	data, ok := reconciled.Data.(HarnessSyncData)
	if !ok {
		return domain.InternalError(command, errors.New("reconciliation returned an unexpected payload"))
	}

	steps := []HarnessSyncStep{}
	applied := 0
	verifiedCount := 0
	for index, entry := range data.Plan.Entries {
		if entry.Action == harness.SyncActionCurrent {
			continue
		}
		operation := entry.Action
		harnessOptions := HarnessOperationOptions{
			ProjectionOperationOptions: options.ProjectionOperationOptions,
			HarnessID:                  entry.Harness,
			TargetRoot:                 options.TargetRoot,
			SkillProfile:               options.SkillProfile,
			Scope:                      options.Scope,
		}
		planned := services.PlanHarnessOperation(ctx, options.Path, operation, harnessOptions)
		if planned.ExitCode != 0 {
			steps = append(steps, HarnessSyncStep{
				Harness: entry.Harness, Action: operation, Status: "plan-failed",
			})
			return services.haltedConvergence(
				command, planned, data.Plan, steps, applied, verifiedCount, index,
			)
		}
		planData, planOK := planned.Data.(HarnessPlanData)
		if !planOK {
			return domain.InternalError(command, errors.New("harness plan returned an unexpected payload"))
		}
		planID := planData.Plan.PlanID
		result := services.ApplyHarnessOperation(ctx, options.Path, operation, planID, harnessOptions)
		if result.ExitCode != 0 {
			steps = append(steps, HarnessSyncStep{
				Harness: entry.Harness, Action: operation, PlanID: planID,
				OperationID: result.OperationID, Status: "apply-failed",
			})
			return services.haltedConvergence(
				command, result, data.Plan, steps, applied, verifiedCount, index,
			)
		}
		// The target has mutated the moment apply succeeds, so it is counted here
		// rather than after verification. Counting it later would let a failed
		// verify report the entry as untouched and still queued, sending recovery
		// at a target that has in fact already changed.
		applied++
		// plan -> apply -> verify is the estate's transaction shape, and a
		// sequenced convergence must not quietly drop the third step: without it
		// the run reports success on an apply whose result was never confirmed,
		// and the next harness would build on an unverified target.
		verified := services.VerifyHarnessOperation(
			ctx, options.Path, operation, result.OperationID, harnessOptions,
		)
		if verified.ExitCode != 0 {
			steps = append(steps, HarnessSyncStep{
				Harness: entry.Harness, Action: operation, PlanID: planID,
				OperationID: result.OperationID, Status: "applied-unverified",
			})
			// index+1: this entry is applied, so it is not remaining work.
			return services.haltedConvergence(
				command, verified, data.Plan, steps, applied, verifiedCount, index+1,
			)
		}
		verifiedCount++
		steps = append(steps, HarnessSyncStep{
			Harness: entry.Harness, Action: operation, PlanID: planID,
			OperationID: result.OperationID, Status: "verified",
		})
	}

	// Every selected entry converged. Whether that proves the *device* converged
	// is a different question: if a catalogue entry's installed state could not
	// be read, a leftover projection of it may still sit at this target. The
	// selected work is reported as done either way, and the unread dimension
	// travels with it rather than disappearing into a bare success.
	result := HarnessSyncConvergeData{
		Plan: data.Plan, Steps: steps,
		Applied: applied, Verified: verifiedCount, Remaining: 0,
	}
	exit := domain.ExitSuccess
	if len(unproven) != 0 {
		exit = domain.ExitNotProven
	}
	envelope := domain.NewEnvelope(command, exit, result, unproven...)
	envelope.Mutation.Attempted = applied != 0
	envelope.Mutation.Completed = applied != 0
	envelope.Scope["device_id"] = data.Plan.DeviceID
	return envelope
}

func combinedHarnessConvergenceRemoved() bool { return true }

// convergenceBlockedBy reports whether a reconciliation finding refuses a
// mutating run.
//
// Two do not. Drift is the condition convergence exists to resolve. An
// unselected catalogue entry GDS could not observe is not the device's problem:
// a device declares which harnesses it runs, and GDS does not gate that
// selection on its ability to introspect the ones the device did not pick —
// verifying harnesses is the harness repository's job, not this estate's. It is
// not silently discarded either; see the caller, which carries the finding into
// the result and reports not-proven rather than claiming whole-device
// convergence it cannot support.
//
// Everything else refuses: an unknown or empty selection, a target collision, an
// unreadable descriptor, or a *selected* adapter that will not build all leave
// the run unable to reason about the work it was asked to do.
func convergenceBlockedBy(code string) bool {
	switch code {
	case "GDS_HARNESS_SELECTION_DRIFT", "GDS_HARNESS_UNOBSERVABLE_NOT_PROVEN":
		return false
	default:
		return true
	}
}

// haltedConvergence reports a partial run truthfully: what was applied stays
// applied, and the caller learns exactly where the sequence stopped.
func (services *Services) haltedConvergence(
	command string,
	cause domain.Envelope,
	plan harness.SyncPlan,
	steps []HarnessSyncStep,
	applied int,
	verified int,
	stoppedAt int,
) domain.Envelope {
	remaining := 0
	for index, entry := range plan.Entries {
		if index >= stoppedAt && entry.Action != harness.SyncActionCurrent {
			remaining++
		}
	}
	findings := append([]domain.Finding{{
		Code: "GDS_HARNESS_SYNC_PARTIAL", Severity: domain.SeverityHigh,
		Message: "Convergence stopped; applied steps remain applied and the rest are unchanged.",
		Evidence: map[string]any{
			"device_id": plan.DeviceID, "applied": applied,
			"verified": verified, "remaining": remaining,
		},
	}}, cause.Findings...)
	envelope := domain.NewEnvelope(command, cause.ExitClass, HarnessSyncConvergeData{
		Plan: plan, Steps: steps,
		Applied: applied, Verified: verified, Remaining: remaining,
	}, findings...)
	envelope.Mutation.Attempted = applied != 0 || cause.Mutation.Attempted
	envelope.Mutation.Completed = applied != 0
	envelope.Scope["device_id"] = plan.DeviceID
	return envelope
}
