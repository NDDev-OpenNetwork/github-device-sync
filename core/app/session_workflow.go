package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	contextresolver "github.com/NDDev-OpenNetwork/github-device-sync/core/context"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

const maxSessionBoundaries = 64

type SessionStartOptions struct {
	Scope     string
	Refresh   string
	StatePath string
}

type SessionBoundary struct {
	RepositoryID   string                   `json:"repository_id,omitempty"`
	Path           string                   `json:"path"`
	Mode           string                   `json:"mode"`
	Before         gitprovider.Status       `json:"before"`
	After          gitprovider.Status       `json:"after"`
	Fetch          *gitprovider.FetchReport `json:"fetch,omitempty"`
	ForcedUpdate   bool                     `json:"forced_update"`
	SafeActions    []string                 `json:"safe_actions"`
	BlockedActions []string                 `json:"blocked_actions"`
}

type SessionStartReport struct {
	Scope        string                  `json:"scope"`
	Refresh      string                  `json:"refresh"`
	StatePath    string                  `json:"state_path,omitempty"`
	Context      contextresolver.Context `json:"context"`
	Boundaries   []SessionBoundary       `json:"boundaries"`
	KillSwitches operations.KillSwitches `json:"kill_switches"`
}

func (services *Services) StartSession(
	ctx context.Context,
	path string,
	options SessionStartOptions,
) domain.Envelope {
	if options.Scope == "" {
		options.Scope = "current"
	}
	if options.Refresh == "" {
		options.Refresh = "none"
	}
	if options.Scope != "current" {
		return domain.NewEnvelope("gds session start", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_SESSION_SCOPE_INVALID", Severity: domain.SeverityHigh,
			Message: "Session start currently supports only --scope current.",
		})
	}
	if options.Refresh != "none" && options.Refresh != "origin" {
		return domain.NewEnvelope("gds session start", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_SESSION_REFRESH_INVALID", Severity: domain.SeverityHigh,
			Message: "Session refresh must be none or origin.",
		})
	}
	outcome := services.Context.Resolve(ctx, path)
	report := SessionStartReport{
		Scope: options.Scope, Refresh: options.Refresh, Context: outcome.Context,
		Boundaries: []SessionBoundary{},
	}
	findings := append([]domain.Finding(nil), outcome.Findings...)
	var sessionState *state.Store
	stateUnavailable := false
	statePersistenceFailed := false
	if options.Refresh == "origin" {
		statePath, err := resolveStatePath(options.StatePath)
		if err == nil {
			report.StatePath = statePath
			sessionState, err = state.Open(ctx, statePath)
		}
		if err != nil {
			stateUnavailable = true
			findings = append(findings, domain.Finding{
				Code: "GDS_SESSION_STATE_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  "Durable session-refresh state is unavailable; origin refs were not refreshed.",
				Evidence: map[string]any{"state_path": report.StatePath},
			})
		}
	}
	switches, switchErr := operations.LoadKillSwitches(os.LookupEnv)
	report.KillSwitches = switches
	if switchErr != nil {
		findings = append(findings, domain.Finding{
			Code: "GDS_KILL_SWITCH_INVALID", Severity: domain.SeverityCritical,
			Message: "A kill-switch value is invalid; session refresh fails closed.",
		})
	}
	paths, discoveryFindings := services.sessionBoundaryPaths(ctx, outcome.Context)
	findings = append(findings, discoveryFindings...)
	refreshAttempted := false
	refreshCompleted := true
	forcedUpdate := false
	providerFailure := false
	for _, boundaryPath := range paths {
		boundaryOutcome := services.Context.Resolve(ctx, boundaryPath)
		if boundaryPath != outcome.Context.Workspace.GitWorktreeRoot {
			findings = append(findings, boundaryOutcome.Findings...)
		}
		before, err := services.Git.InspectStatus(ctx, boundaryPath)
		if err != nil {
			findings = append(findings, domain.Finding{
				Code: "GDS_SESSION_STATUS_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  "A relevant Git boundary could not be classified.",
				Evidence: map[string]any{"path": boundaryPath},
			})
			continue
		}
		item := SessionBoundary{
			RepositoryID: boundaryOutcome.Context.Repository.ID, Path: boundaryPath,
			Mode: boundaryOutcome.Context.Mode.Kind, Before: before, After: before,
			SafeActions: []string{}, BlockedActions: []string{},
		}
		if options.Refresh == "origin" {
			switch {
			case stateUnavailable:
				refreshCompleted = false
				item.BlockedActions = append(item.BlockedActions, "refresh-origin")
			case item.RepositoryID == "":
				refreshCompleted = false
				item.BlockedActions = append(item.BlockedActions, "refresh-origin")
				findings = append(findings, domain.Finding{
					Code: "GDS_SESSION_IDENTITY_NOT_PROVEN", Severity: domain.SeverityHigh,
					Message:  "A relevant Git boundary has no stable GDS identity; origin refs were not refreshed.",
					Evidence: map[string]any{"path": boundaryPath},
				})
			case switchErr != nil || switches.MutationsDisabled:
				refreshCompleted = false
				item.BlockedActions = append(item.BlockedActions, "refresh-origin")
				if switches.MutationsDisabled {
					findings = append(findings, domain.Finding{
						Code: "GDS_MUTATIONS_DISABLED", Severity: domain.SeverityHigh,
						Message:  "Origin refresh was requested but global mutations are disabled.",
						Evidence: map[string]any{"path": boundaryPath},
					})
				}
			default:
				refreshAttempted = true
				fetch, fetchErr := services.GitMutations.FetchRemote(ctx, boundaryPath, "origin")
				if fetchErr != nil {
					refreshCompleted = false
					providerFailure = true
					findings = append(findings, domain.Finding{
						Code: "GDS_SESSION_REFRESH_NOT_PROVEN", Severity: domain.SeverityHigh,
						Message:  "Origin refresh failed without integrating the current branch.",
						Evidence: map[string]any{"path": boundaryPath},
					})
				} else {
					item.Fetch = &fetch
					for _, change := range fetch.Changes {
						if change.Kind == "forced-update" {
							item.ForcedUpdate = true
							forcedUpdate = true
							findings = append(findings, domain.Finding{
								Code: "GDS_SESSION_FORCED_UPDATE", Severity: domain.SeverityHigh,
								Message: "A refreshed remote-tracking ref was force-updated; automatic synchronization is blocked.",
								Evidence: map[string]any{
									"path": boundaryPath, "reference": change.Reference,
									"before_oid": change.BeforeOID, "after_oid": change.AfterOID,
								},
							})
						}
					}
					after, statusErr := services.Git.InspectStatus(ctx, boundaryPath)
					if statusErr != nil {
						refreshCompleted = false
						findings = append(findings, domain.Finding{
							Code: "GDS_SESSION_STATUS_NOT_PROVEN", Severity: domain.SeverityHigh,
							Message:  "Git status could not be classified after a non-integrating refresh.",
							Evidence: map[string]any{"path": boundaryPath},
						})
					} else {
						after.RemoteFreshness = "current"
						after.Classification = strings.Replace(after.Classification, "-cached", "-current", 1)
						item.After = after
					}
					refs, marshalErr := json.Marshal(fetch.After)
					if marshalErr != nil {
						refreshCompleted = false
						statePersistenceFailed = true
						stateUnavailable = true
					} else {
						_, stateErr := sessionState.PutRemoteRefresh(ctx, state.RemoteRefreshRecord{
							RepositoryID: item.RepositoryID, WorktreeRoot: boundaryPath,
							Remote: "origin", ObservedAt: services.Now().UTC(),
							HeadOID: before.Head.OID, Refs: refs, ForcedUpdate: item.ForcedUpdate,
						})
						if stateErr != nil {
							refreshCompleted = false
							statePersistenceFailed = true
							stateUnavailable = true
						}
					}
					if statePersistenceFailed {
						findings = append(findings, domain.Finding{
							Code: "GDS_SESSION_REFRESH_EVIDENCE_NOT_PROVEN", Severity: domain.SeverityHigh,
							Message:  "Origin refs changed, but durable refresh evidence could not be stored; later synchronization is blocked.",
							Evidence: map[string]any{"path": boundaryPath, "state_path": report.StatePath},
						})
					}
				}
			}
		}
		item.SafeActions, item.BlockedActions = sessionActions(item.After, item.ForcedUpdate, item.BlockedActions)
		report.Boundaries = append(report.Boundaries, item)
	}
	class := classifyFindings(findings)
	if providerFailure {
		class = strongestClass(class, domain.ExitProviderTransient)
	}
	if statePersistenceFailed {
		class = strongestClass(class, domain.ExitPartial)
	}
	if forcedUpdate {
		class = strongestClass(class, domain.ExitConflict)
	}
	if options.Refresh == "origin" && switches.MutationsDisabled {
		class = strongestClass(class, domain.ExitPolicy)
	}
	envelope := domain.NewEnvelope("gds session start", class, report, findings...)
	envelope.Mutation.Attempted = refreshAttempted
	envelope.Mutation.Completed = refreshAttempted && refreshCompleted
	if sessionState != nil {
		if err := sessionState.Close(); err != nil && refreshAttempted {
			envelope = domain.NewEnvelope("gds session start", domain.ExitPartial, report,
				append(findings, domain.Finding{
					Code: "GDS_SESSION_STATE_CLOSE_NOT_PROVEN", Severity: domain.SeverityHigh,
					Message:  "Session refresh completed, but the durable state close could not be verified.",
					Evidence: map[string]any{"state_path": report.StatePath},
				})...,
			)
			envelope.Mutation.Attempted = true
			envelope.Mutation.Completed = false
		}
	}
	if outcome.Context.Repository.ID != "" {
		envelope.Scope["repository_id"] = outcome.Context.Repository.ID
	}
	return envelope
}

func (services *Services) sessionBoundaryPaths(
	ctx context.Context,
	resolved contextresolver.Context,
) ([]string, []domain.Finding) {
	queue := []string{}
	if resolved.Workspace.GitWorktreeRoot != "" {
		queue = append(queue, resolved.Workspace.GitWorktreeRoot)
	}
	for _, boundary := range resolved.Boundaries {
		queue = append(queue, boundary.Path)
	}
	seen := map[string]struct{}{}
	result := []string{}
	findings := []domain.Finding{}
	for len(queue) != 0 {
		path := queue[0]
		queue = queue[1:]
		absolute, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		absolute = filepath.Clean(absolute)
		if _, found := seen[absolute]; found {
			continue
		}
		if len(result) >= maxSessionBoundaries {
			findings = append(findings, domain.Finding{
				Code: "GDS_SESSION_BOUNDARY_LIMIT", Severity: domain.SeverityHigh,
				Message:  "Relevant Git boundary discovery exceeded the bounded limit.",
				Evidence: map[string]any{"limit": maxSessionBoundaries},
			})
			break
		}
		seen[absolute] = struct{}{}
		result = append(result, absolute)
		topology, err := services.Git.InspectTopology(ctx, absolute)
		if err != nil {
			findings = append(findings, domain.Finding{
				Code: "GDS_SESSION_TOPOLOGY_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  "A relevant Git boundary topology could not be inspected.",
				Evidence: map[string]any{"path": absolute},
			})
			continue
		}
		for _, module := range topology.Submodules {
			switch module.WorktreeState {
			case "at-gitlink", "off-gitlink", "untracked-gitlink":
				queue = append(queue, filepath.Join(absolute, filepath.FromSlash(module.Path)))
			}
		}
	}
	if len(result) > 1 {
		head := result[0]
		tail := append([]string(nil), result[1:]...)
		sort.Strings(tail)
		result = append([]string{head}, tail...)
	}
	return result, findings
}

func sessionActions(
	status gitprovider.Status,
	forced bool,
	blocked []string,
) ([]string, []string) {
	safe := []string{"inspect"}
	if forced {
		return append(safe, "review-forced-update"), appendUnique(blocked, "sync")
	}
	switch status.Classification {
	case "clean-current", "clean-cached":
		safe = append(safe, "continue-work")
	case "behind-current", "behind-cached":
		safe = append(safe, "plan-sync")
	case "ahead-current", "ahead-cached":
		safe = append(safe, "plan-handoff")
	case "dirty":
		safe = append(safe, "continue-local-work", "plan-handoff")
		blocked = appendUnique(blocked, "sync", "cleanup")
	case "diverged-current", "diverged-cached":
		safe = append(safe, "inspect-commit-graph")
		blocked = appendUnique(blocked, "sync", "automatic-integration")
	case "no-upstream":
		safe = append(safe, "plan-handoff")
		blocked = appendUnique(blocked, "sync")
	case "detached", "unborn", "conflicted":
		safe = append(safe, "inspect-git-state")
		blocked = appendUnique(blocked, "sync", "handoff", "cleanup")
	default:
		blocked = appendUnique(blocked, "all-mutations")
	}
	return safe, blocked
}

func appendUnique(values []string, additions ...string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}
