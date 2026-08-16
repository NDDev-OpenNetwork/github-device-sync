package harness

import (
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// Sync reconciles the harness set a device declares with the projections that
// are actually installed on it.
//
// GDS knows the whole catalogue (`harnesses/capability-registry.yaml`), while a
// device selects only the subset its owner works with. Those two facts alone do
// not converge: the per-harness install/update/remove transactions each act on
// exactly one harness and never read the device selection, so a harness added to
// a descriptor stays uninstalled and a harness removed from one stays installed.
// This package decides which existing transaction to run for which harness; it
// deliberately does not perform any of them.
const (
	SyncActionInstall = "install"
	SyncActionUpdate  = "update"
	SyncActionRemove  = "remove"
	SyncActionCurrent = "current"
)

// SyncObservation is the observed target state of one canonical harness.
// Present is false when the harness has no projection at the device target root;
// Drift counts the observed files that differ from the rendered candidate.
type SyncObservation struct {
	Harness string `json:"harness"`
	Present bool   `json:"present"`
	Drift   int    `json:"drift"`
}

// SyncEntry is the resolved reconciliation decision for one canonical harness.
type SyncEntry struct {
	Harness  string `json:"harness"`
	Selected bool   `json:"selected"`
	Present  bool   `json:"present"`
	Drift    int    `json:"drift"`
	Action   string `json:"action"`
}

// SyncPlan is the ordered set of decisions for one device.
//
// Order is remove, then update, then install: a deselected harness releases its
// target before a newly selected one claims it, so a replacement never leaves
// two projections briefly owning the same path.
type SyncPlan struct {
	DeviceID string      `json:"device_id"`
	Selected []string    `json:"selected"`
	Entries  []SyncEntry `json:"entries"`
	Install  int         `json:"install"`
	Update   int         `json:"update"`
	Remove   int         `json:"remove"`
	Current  int         `json:"current"`
}

// Pending reports whether the device diverges from its declared selection.
func (plan SyncPlan) Pending() int {
	return plan.Install + plan.Update + plan.Remove
}

// TargetCollision names one target path that more than one selected harness
// would own.
type TargetCollision struct {
	Path      string   `json:"path"`
	Harnesses []string `json:"harnesses"`
}

// DetectTargetCollisions reports the target paths that more than one selected
// harness claims.
//
// `claims` maps a selected harness id to the target-relative paths its rendered
// projection would own. The comparison is between *desired* candidates and is
// therefore independent of what the target currently holds: on an empty root
// nothing is installed yet, so a check that only asks "is this path already
// taken" sees no conflict and lets a run install the first harness before the
// second one discovers the collision — half-way through a mutation the read-only
// pass promised to prevent.
func DetectTargetCollisions(claims map[string][]string) []TargetCollision {
	owners := map[string]map[string]struct{}{}
	for id, paths := range claims {
		for _, target := range paths {
			if _, seen := owners[target]; !seen {
				owners[target] = map[string]struct{}{}
			}
			owners[target][id] = struct{}{}
		}
	}
	collisions := []TargetCollision{}
	for target, ids := range owners {
		if len(ids) < 2 {
			continue
		}
		harnesses := make([]string, 0, len(ids))
		for id := range ids {
			harnesses = append(harnesses, id)
		}
		sort.Strings(harnesses)
		collisions = append(collisions, TargetCollision{Path: target, Harnesses: harnesses})
	}
	sort.Slice(collisions, func(left, right int) bool {
		return collisions[left].Path < collisions[right].Path
	})
	return collisions
}

var syncActionOrder = map[string]int{
	SyncActionRemove:  0,
	SyncActionUpdate:  1,
	SyncActionInstall: 2,
	SyncActionCurrent: 3,
}

// BuildSyncPlan classifies every canonical harness for one device.
//
// `selected` is the device's declared set; an entry in it that the catalogue
// does not know is reported as `GDS_HARNESS_SELECTED_UNKNOWN` and takes no
// action, because GDS cannot render a projection it has no profile for.
// `observations` need not cover every canonical harness: a missing observation
// means absent, which is the safe reading — it can only ever schedule an
// install, never a remove.
func BuildSyncPlan(
	deviceID string,
	canonical []string,
	selected []string,
	observations []SyncObservation,
) (SyncPlan, []domain.Finding) {
	known := make(map[string]struct{}, len(canonical))
	for _, id := range canonical {
		known[id] = struct{}{}
	}
	wanted := make(map[string]struct{}, len(selected))
	findings := []domain.Finding{}
	unknown := false
	for _, id := range selected {
		if _, ok := known[id]; !ok {
			findings = append(findings, harnessFinding(
				"GDS_HARNESS_SELECTED_UNKNOWN",
				"A selected harness is not present in the canonical registry.",
				map[string]any{"harness": id, "device_id": deviceID, "known": canonical},
			))
			unknown = true
			continue
		}
		wanted[id] = struct{}{}
	}
	// One unreadable entry makes the whole selection unreadable. Classifying the
	// rest would answer a question the owner did not ask — "what would converge
	// the harnesses I spelled correctly" — and present it as the device's plan,
	// while the selection GDS was actually given cannot be rendered at all.
	// Convergence already refuses the run; the read-only plan says the same thing
	// rather than showing work that will never be scheduled.
	if unknown {
		sortFindings(findings)
		return SyncPlan{DeviceID: deviceID, Selected: []string{}, Entries: []SyncEntry{}}, findings
	}
	// An empty selection is ambiguous: it reads the same whether the owner
	// deliberately runs no harness on this device or simply has not filled the
	// field in yet. Reconciling it would schedule the wholesale removal of every
	// installed projection, so it is reported and nothing is planned.
	if len(wanted) == 0 {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_SELECTION_EMPTY",
			"Device selects no harness; reconciliation needs an explicit selection.",
			map[string]any{"device_id": deviceID},
		))
		sortFindings(findings)
		return SyncPlan{DeviceID: deviceID, Selected: []string{}, Entries: []SyncEntry{}}, findings
	}

	observed := make(map[string]SyncObservation, len(observations))
	for _, observation := range observations {
		observed[observation.Harness] = observation
	}

	plan := SyncPlan{DeviceID: deviceID, Selected: []string{}, Entries: []SyncEntry{}}
	for id := range wanted {
		plan.Selected = append(plan.Selected, id)
	}
	sort.Strings(plan.Selected)

	for _, id := range canonical {
		_, isSelected := wanted[id]
		observation := observed[id]
		entry := SyncEntry{
			Harness: id, Selected: isSelected,
			Present: observation.Present, Drift: observation.Drift,
		}
		switch {
		case isSelected && !observation.Present:
			entry.Action = SyncActionInstall
			plan.Install++
		case isSelected && observation.Drift != 0:
			entry.Action = SyncActionUpdate
			plan.Update++
		case isSelected:
			entry.Action = SyncActionCurrent
			plan.Current++
		case observation.Present:
			entry.Action = SyncActionRemove
			plan.Remove++
		default:
			// Neither selected nor installed: the catalogue entry is simply not
			// this device's concern, so it is not reported as an entry at all.
			continue
		}
		plan.Entries = append(plan.Entries, entry)
	}
	sort.Slice(plan.Entries, func(left, right int) bool {
		leftOrder := syncActionOrder[plan.Entries[left].Action]
		rightOrder := syncActionOrder[plan.Entries[right].Action]
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return plan.Entries[left].Harness < plan.Entries[right].Harness
	})
	if plan.Pending() != 0 {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_SELECTION_DRIFT",
			"Installed harness projections differ from the device selection.",
			map[string]any{
				"device_id": deviceID, "install": plan.Install,
				"update": plan.Update, "remove": plan.Remove,
			},
		))
	}
	sortFindings(findings)
	return plan, findings
}
