package app

import (
	"context"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
)

type EstateSummaryReport struct {
	EstateID          string                          `json:"estate_id"`
	Installations     []reconciler.InstallationResult `json:"installations"`
	Repositories      int                             `json:"repositories"`
	ManagementModes   map[string]int                  `json:"management_modes"`
	IdentityStates    map[string]int                  `json:"identity_states"`
	DriftByClass      map[string]int                  `json:"drift_by_class"`
	ExternalMutations []string                        `json:"external_mutations"`
}

type DriftReport struct {
	EstateID          string             `json:"estate_id"`
	Repositories      int                `json:"repositories"`
	Drift             []reconciler.Drift `json:"drift"`
	ExternalMutations []string           `json:"external_mutations"`
}

func (services *Services) ReportEstateSummary(
	ctx context.Context,
	path string,
	options GitHubReadOptions,
) domain.Envelope {
	envelope := services.ReconcileGitHub(ctx, path, options)
	envelope.Command = "gds report estate-summary"
	data, ok := envelope.Data.(ReconciliationPlanData)
	if !ok {
		return envelope
	}
	estateID, _ := envelope.Scope["estate_id"].(string)
	report := EstateSummaryReport{
		EstateID:        estateID,
		Installations:   data.Result.Installations,
		Repositories:    len(data.Result.Inventory.Repositories),
		ManagementModes: map[string]int{}, IdentityStates: map[string]int{},
		DriftByClass: map[string]int{}, ExternalMutations: []string{},
	}
	for _, repository := range data.Result.Inventory.Repositories {
		report.ManagementModes[repository.ManagementMode]++
		report.IdentityStates[repository.IdentityState]++
	}
	for _, drift := range data.Result.Drift {
		report.DriftByClass[drift.Class]++
	}
	envelope.Data = report
	return envelope
}

func (services *Services) ReportDrift(
	ctx context.Context,
	path string,
	options GitHubReadOptions,
) domain.Envelope {
	envelope := services.ReconcileGitHub(ctx, path, options)
	envelope.Command = "gds report drift"
	data, ok := envelope.Data.(ReconciliationPlanData)
	if !ok {
		return envelope
	}
	estateID, _ := envelope.Scope["estate_id"].(string)
	envelope.Data = DriftReport{
		EstateID:     estateID,
		Repositories: len(data.Result.Inventory.Repositories),
		Drift:        data.Result.Drift, ExternalMutations: []string{},
	}
	return envelope
}

func (services *Services) ReportSourceFreshness(
	ctx context.Context,
	path string,
	asOf string,
) domain.Envelope {
	envelope := services.SourceStatus(ctx, path, asOf)
	envelope.Command = "gds report source-freshness"
	return envelope
}

func (services *Services) ReportHarnessCompatibility(
	ctx context.Context,
	path string,
) domain.Envelope {
	envelope := services.ValidateHarness(ctx, path, "all")
	envelope.Command = "gds report harness-compatibility"
	return envelope
}

func (services *Services) ReportSecurity(
	ctx context.Context,
	path string,
) domain.Envelope {
	envelope := services.ValidateSecurity(ctx, path, "security")
	envelope.Command = "gds report security"
	return envelope
}
