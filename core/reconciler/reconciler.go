// Package reconciler compares provider observations with desired estate intent.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

type InstallationReader interface {
	ListInstallationRepositories(context.Context, int) (githubprovider.Inventory, error)
}

type InstallationSink interface {
	PersistInventory(context.Context, githubprovider.Inventory) error
}

type Reconciler struct {
	Config          estate.Config
	Readers         map[string]InstallationReader
	Sink            InstallationSink
	Concurrency     int
	MaxRepositories int
}

type Result struct {
	Inventory     estate.CompiledInventory `json:"inventory"`
	Installations []InstallationResult     `json:"installations"`
	Drift         []Drift                  `json:"drift"`
	Findings      []domain.Finding         `json:"findings"`
}

type InstallationResult struct {
	InstallationID  string                            `json:"installation_id"`
	RepositoryCount int                               `json:"repository_count"`
	Status          string                            `json:"status"`
	ObservedAt      time.Time                         `json:"observed_at,omitempty"`
	Rate            githubprovider.Rate               `json:"rate"`
	RequestIDs      []string                          `json:"request_ids"`
	Permissions     githubprovider.PermissionEvidence `json:"permissions"`
}

type Drift struct {
	ProviderID  int64  `json:"provider_id"`
	Class       string `json:"class"`
	Severity    string `json:"severity"`
	Remediation string `json:"remediation"`
}

func (reconciler Reconciler) ReconcileAll(ctx context.Context) Result {
	result := Result{}
	maxRepositories := reconciler.MaxRepositories
	if maxRepositories == 0 {
		maxRepositories = 2000
	}
	if maxRepositories < 1 || maxRepositories > 2000 {
		result.Findings = append(result.Findings, domain.Finding{
			Code: "GDS_RECONCILE_REPOSITORY_LIMIT_INVALID", Severity: domain.SeverityHigh,
			Message: "Reconciliation repository limit must be between 1 and 2000.",
		})
		return result
	}
	concurrency := reconciler.Concurrency
	if concurrency == 0 {
		concurrency = reconciler.Config.Root.Rollout.MaxParallelObservation
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 16 {
		concurrency = 16
	}
	type outcome struct {
		id        string
		inventory githubprovider.Inventory
		err       error
	}
	sem := make(chan struct{}, concurrency)
	output := make(chan outcome, len(reconciler.Config.Root.Installations))
	var wait sync.WaitGroup
	for _, installationID := range reconciler.Config.Root.Installations {
		reader := reconciler.Readers[installationID]
		if reader == nil {
			result.Findings = append(result.Findings, domain.Finding{
				Code: "GDS_RECONCILE_INSTALLATION_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("No read-only provider is configured for %q.", installationID),
				Evidence: map[string]any{"installation": installationID},
			})
			result.Installations = append(result.Installations, InstallationResult{
				InstallationID: installationID, Status: "not-proven",
			})
			continue
		}
		wait.Add(1)
		go func(id string, client InstallationReader) {
			defer wait.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				output <- outcome{id: id, err: ctx.Err()}
				return
			}
			inventory, err := client.ListInstallationRepositories(ctx, maxRepositories)
			output <- outcome{id: id, inventory: inventory, err: err}
		}(installationID, reader)
	}
	wait.Wait()
	close(output)
	observed := []estate.ObservedRepository{}
	accepted := []outcome{}
	expectedAccounts := make(map[string]string, len(reconciler.Config.Installations))
	for _, installation := range reconciler.Config.Installations {
		expectedAccounts[installation.Installation.ID] = installation.Installation.AccountLogin
	}
	for outcome := range output {
		if outcome.err != nil {
			result.Findings = append(result.Findings, installationReadFinding(outcome.id, outcome.err))
			result.Installations = append(result.Installations, InstallationResult{
				InstallationID: outcome.id, Status: "not-proven",
			})
			continue
		}
		if outcome.inventory.InstallationID != outcome.id {
			result.Findings = append(result.Findings, domain.Finding{
				Code: "GDS_RECONCILE_INSTALLATION_IDENTITY_MISMATCH", Severity: domain.SeverityHigh,
				Message: "Provider inventory installation identity does not match the requested scope.",
				Evidence: map[string]any{
					"expected": outcome.id, "observed": outcome.inventory.InstallationID,
				},
			})
			continue
		}
		expectedAccount := expectedAccounts[outcome.id]
		identityMismatch := false
		for _, repository := range outcome.inventory.Repositories {
			if !strings.EqualFold(repository.Owner, expectedAccount) {
				identityMismatch = true
				break
			}
		}
		if expectedAccount == "" || identityMismatch {
			result.Findings = append(result.Findings, domain.Finding{
				Code: "GDS_RECONCILE_INSTALLATION_ACCOUNT_MISMATCH", Severity: domain.SeverityHigh,
				Message: "Provider inventory does not belong exclusively to the estate installation account.",
				Evidence: map[string]any{
					"installation": outcome.id, "expected_account": expectedAccount,
				},
			})
			result.Installations = append(result.Installations, InstallationResult{
				InstallationID: outcome.id, Status: "identity-mismatch",
			})
			continue
		}
		accepted = append(accepted, outcome)
		for _, repository := range outcome.inventory.Repositories {
			observed = append(observed, estate.ObservedRepository{
				ProviderID: repository.ID, Owner: repository.Owner, Name: repository.Name,
				Fork: repository.Fork, Archived: repository.Archived,
				Visibility: repository.Visibility, DefaultBranch: repository.DefaultBranch,
			})
		}
	}
	if len(observed) > maxRepositories {
		result.Findings = append(result.Findings, domain.Finding{
			Code: "GDS_RECONCILE_ESTATE_LIMIT_EXCEEDED", Severity: domain.SeverityHigh,
			Message:  "Combined installation inventory exceeds the configured estate bound.",
			Evidence: map[string]any{"count": len(observed), "limit": maxRepositories},
		})
		for _, outcome := range accepted {
			result.Installations = append(result.Installations, InstallationResult{
				InstallationID: outcome.id, RepositoryCount: len(outcome.inventory.Repositories),
				Status: "rejected-estate-limit", ObservedAt: outcome.inventory.ObservedAt,
				Rate:        outcome.inventory.Rate,
				RequestIDs:  append([]string(nil), outcome.inventory.RequestIDs...),
				Permissions: outcome.inventory.Permissions,
			})
		}
		sortReconciliationResult(&result)
		return result
	}
	for _, outcome := range accepted {
		persisted := true
		if reconciler.Sink != nil {
			if err := reconciler.Sink.PersistInventory(ctx, outcome.inventory); err != nil {
				persisted = false
				result.Findings = append(result.Findings, domain.Finding{
					Code: "GDS_RECONCILE_OBSERVATION_PERSIST_FAILED", Severity: domain.SeverityHigh,
					Message: "Current provider inventory could not be persisted durably.",
					Evidence: map[string]any{
						"installation": outcome.id, "error_type": fmt.Sprintf("%T", err),
					},
				})
			}
		}
		status := "observed"
		if !persisted {
			status = "observed-unpersisted"
		}
		result.Installations = append(result.Installations, InstallationResult{
			InstallationID: outcome.id, RepositoryCount: len(outcome.inventory.Repositories),
			Status: status, ObservedAt: outcome.inventory.ObservedAt,
			Rate:        outcome.inventory.Rate,
			RequestIDs:  append([]string(nil), outcome.inventory.RequestIDs...),
			Permissions: outcome.inventory.Permissions,
		})
	}
	compiled, compileFindings := estate.Compile(reconciler.Config, observed)
	result.Inventory = compiled
	result.Findings = append(result.Findings, compileFindings...)
	for _, assignment := range compiled.Repositories {
		if assignment.IdentityState == "unassigned" {
			result.Drift = append(result.Drift, Drift{
				ProviderID: assignment.ProviderID, Class: "identity", Severity: "medium",
				Remediation: "repository-onboarding-plan",
			})
		}
	}
	sortReconciliationResult(&result)
	return result
}

func sortReconciliationResult(result *Result) {
	sort.Slice(result.Installations, func(left, right int) bool {
		return result.Installations[left].InstallationID < result.Installations[right].InstallationID
	})
	sort.Slice(result.Findings, func(left, right int) bool {
		return result.Findings[left].Code < result.Findings[right].Code
	})
}

func installationReadFinding(installationID string, err error) domain.Finding {
	finding := domain.Finding{
		Code: "GDS_RECONCILE_INSTALLATION_NOT_PROVEN", Severity: domain.SeverityHigh,
		Message: fmt.Sprintf("Installation %q inventory is unavailable.", installationID),
		Evidence: map[string]any{
			"installation": installationID, "error_type": fmt.Sprintf("%T", err),
		},
	}
	var apiError *githubprovider.APIError
	if errors.As(err, &apiError) && apiError.Kind == githubprovider.ErrorPermissionContract {
		finding.Code = "GDS_RECONCILE_PERMISSION_CONTRACT_MISMATCH"
		finding.Severity = domain.SeverityCritical
		finding.Message = "Effective GitHub App permissions do not match canonical estate intent."
		finding.Evidence["provider_error_kind"] = apiError.Kind
	}
	return finding
}
