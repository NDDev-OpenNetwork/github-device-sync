package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type StateInventorySink struct {
	Store *state.Store
}

func (sink StateInventorySink) PersistInventory(
	ctx context.Context,
	inventory githubprovider.Inventory,
) error {
	if sink.Store == nil {
		return fmt.Errorf("state inventory sink requires a store")
	}
	requestID := ""
	if len(inventory.RequestIDs) != 0 {
		requestID = inventory.RequestIDs[len(inventory.RequestIDs)-1]
	}
	for _, repository := range inventory.Repositories {
		body, err := json.Marshal(repository)
		if err != nil {
			return fmt.Errorf("encode provider repository observation: %w", err)
		}
		if err := sink.Store.PutRepositoryObservation(ctx, state.RepositoryObservation{
			InstallationID:       inventory.InstallationID,
			ProviderRepositoryID: repository.ID,
			Owner:                repository.Owner, Name: repository.Name, AccessState: "available",
			ObservedAt: inventory.ObservedAt, Body: body, RequestID: requestID,
		}); err != nil {
			return err
		}
	}
	return nil
}

type ReconciliationRunner struct {
	Store           *state.Store
	Config          estate.Config
	Readers         map[string]reconciler.InstallationReader
	Concurrency     int
	MaxRepositories int
	Now             func() time.Time
	Audit           AuditRecorder
}

type AuditRecorder interface {
	Record(
		context.Context,
		string,
		string,
		reconciler.Result,
		time.Time,
	) (string, string, error)
}

type ReconciliationRunResult struct {
	ReconciliationID string            `json:"reconciliation_id"`
	Status           string            `json:"status"`
	Result           reconciler.Result `json:"result"`
	AuditSnapshotID  string            `json:"audit_snapshot_id,omitempty"`
	AuditDigest      string            `json:"audit_digest,omitempty"`
}

func (runner ReconciliationRunner) Run(ctx context.Context) (ReconciliationRunResult, error) {
	if runner.Store == nil || len(runner.Readers) == 0 || runner.Config.Root.Estate.ID == "" ||
		runner.Audit == nil {
		return ReconciliationRunResult{}, fmt.Errorf("reconciliation runner is incomplete")
	}
	now := time.Now().UTC()
	if runner.Now != nil {
		now = runner.Now().UTC()
	}
	reconciliationID, err := identity.New("reconciliation", now, nil)
	if err != nil {
		return ReconciliationRunResult{}, err
	}
	if err := runner.Store.StartReconciliation(ctx, state.ReconciliationRecord{
		ReconciliationID: reconciliationID, Scope: "estate",
		ScopeID: runner.Config.Root.Estate.ID, Status: "running", StartedAt: now,
	}); err != nil {
		return ReconciliationRunResult{}, err
	}
	result := (reconciler.Reconciler{
		Config: runner.Config, Readers: runner.Readers,
		Sink:        StateInventorySink{Store: runner.Store},
		Concurrency: runner.Concurrency, MaxRepositories: runner.MaxRepositories,
	}).ReconcileAll(ctx)
	status := "succeeded"
	lastError := ""
	if len(result.Findings) != 0 {
		status = "failed"
		for _, installation := range result.Installations {
			if installation.Status == "observed" {
				status = "partial"
				break
			}
		}
		lastError = result.Findings[0].Code
	}
	finishedAt := time.Now().UTC()
	if runner.Now != nil {
		finishedAt = runner.Now().UTC()
	}
	if finishedAt.Before(now) {
		finishedAt = now
	}
	auditSnapshotID, auditDigest, auditErr := runner.Audit.Record(
		ctx, runner.Config.Root.Estate.ID, reconciliationID, result, finishedAt,
	)
	if auditErr != nil {
		result.Findings = append(result.Findings, domain.Finding{
			Code: "GDS_AUDIT_SNAPSHOT_FAILED", Severity: domain.SeverityHigh,
			Message:  "Signed reconciliation audit snapshot could not be created.",
			Evidence: map[string]any{"error_type": fmt.Sprintf("%T", auditErr)},
		})
		status = "failed"
		for _, installation := range result.Installations {
			if installation.Status == "observed" {
				status = "partial"
				break
			}
		}
		if lastError == "" {
			lastError = "GDS_AUDIT_SNAPSHOT_FAILED"
		}
	}
	if err := runner.Store.FinishReconciliation(
		context.WithoutCancel(ctx), reconciliationID, status, finishedAt, result, lastError,
	); err != nil {
		return ReconciliationRunResult{}, err
	}
	return ReconciliationRunResult{
		ReconciliationID: reconciliationID, Status: status, Result: result,
		AuditSnapshotID: auditSnapshotID, AuditDigest: auditDigest,
	}, nil
}
