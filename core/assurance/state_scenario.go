package assurance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/controller"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type durableStateResult struct {
	ReconciliationMS        float64
	WebhookThroughput       float64
	QueueMaxLagMS           float64
	RestartMS               float64
	DatabaseBytes           int64
	FullReconciliationCalls int32
}

func exerciseDurableState(
	ctx context.Context,
	options Options,
	config estate.Config,
	fixtures []fixtureRepository,
) (durableStateResult, error) {
	parent := options.StateDirectory
	if parent == "" {
		parent = os.TempDir()
	}
	runDirectory, err := os.MkdirTemp(parent, "gds-assurance-")
	if err != nil {
		return durableStateResult{}, err
	}
	defer os.RemoveAll(runDirectory)
	if err := os.Chmod(runDirectory, 0o700); err != nil {
		return durableStateResult{}, err
	}
	statePath := filepath.Join(runDirectory, "state.db")
	store, err := state.Initialize(ctx, statePath)
	if err != nil {
		return durableStateResult{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = store.Close()
		}
	}()

	firstObserved := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	inventories := fixtureInventories(fixtures, firstObserved)
	personal := &fixtureReader{inventory: inventories["installation:github-personal"]}
	organization := &fixtureReader{inventory: inventories["installation:github-organization"]}
	exampleMedia := &fixtureReader{inventory: inventories["installation:github-example-media"]}
	guild := &fixtureReader{inventory: inventories["installation:github-guild"]}
	openNetwork := &fixtureReader{inventory: inventories["installation:github-opennetwork"]}
	reconcileStarted := time.Now()
	first, err := (controller.ReconciliationRunner{
		Store: store, Config: config,
		Readers: map[string]reconciler.InstallationReader{
			"installation:github-personal":      personal,
			"installation:github-organization":  organization,
			"installation:github-example-media": exampleMedia,
			"installation:github-guild":         guild,
			"installation:github-opennetwork":   openNetwork,
		},
		Concurrency:     options.ReconciliationConcurrency,
		MaxRepositories: options.RepositoryCount,
		Now:             func() time.Time { return firstObserved }, Audit: fixtureAudit{},
	}).Run(ctx)
	reconciliationMS := milliseconds(time.Since(reconcileStarted))
	if err != nil || first.Status != "succeeded" ||
		len(first.Result.Inventory.Repositories) != options.RepositoryCount {
		return durableStateResult{}, fmt.Errorf(
			"first reconciliation failed: status=%s err=%v", first.Status, err,
		)
	}
	fullCalls := personal.calls.Load() + organization.calls.Load() + exampleMedia.calls.Load() + guild.calls.Load() + openNetwork.calls.Load()
	if fullCalls != 5 {
		return durableStateResult{}, fmt.Errorf(
			"full reconciliation used %d provider reads, want 5", fullCalls,
		)
	}

	accessStates := []string{"inaccessible", "auth-failed", "not-found", "unknown"}
	for index, accessState := range accessStates {
		providerID := int64((index + 1) * 2)
		fixture := fixtures[int(providerID)-1]
		if err := store.PutRepositoryObservation(ctx, state.RepositoryObservation{
			InstallationID: "installation:github-organization", ProviderRepositoryID: providerID,
			Owner: fixture.Provider.Owner, Name: fixture.Provider.Name, AccessState: accessState,
			ObservedAt: firstObserved.Add(time.Minute), RequestID: "assurance-access-state",
		}); err != nil {
			return durableStateResult{}, err
		}
	}

	webhookStarted := time.Now()
	webhookReceivedAt := time.Now().UTC()
	for index := 0; index < options.WebhookDeliveryCount; index++ {
		payload := json.RawMessage(fmt.Sprintf("{\"repository_id\":%d}", index+1))
		inserted, err := store.EnqueueWebhook(ctx, state.WebhookDelivery{
			DeliveryID: fmt.Sprintf("assurance-delivery-%05d", index+1),
			EventType:  "repository", Payload: payload, ReceivedAt: webhookReceivedAt,
		})
		if err != nil || !inserted {
			return durableStateResult{}, fmt.Errorf(
				"enqueue webhook %d: inserted=%t err=%v", index+1, inserted, err,
			)
		}
	}
	maxLag := time.Duration(0)
	for index := 0; index < options.WebhookDeliveryCount; index++ {
		now := time.Now().UTC()
		delivery, err := store.ClaimWebhook(
			ctx, now, 3, state.DefaultWebhookProcessingTimeout,
		)
		if err != nil {
			return durableStateResult{}, fmt.Errorf("claim webhook %d: %w", index+1, err)
		}
		lag := now.Sub(delivery.ReceivedAt)
		if lag > maxLag {
			maxLag = lag
		}
		if err := store.CompleteWebhook(
			ctx, delivery.DeliveryID, *delivery.ClaimedAt, true, now, time.Time{}, 3, "",
		); err != nil {
			return durableStateResult{}, err
		}
	}
	webhookDuration := time.Since(webhookStarted)
	throughput := float64(options.WebhookDeliveryCount) / webhookDuration.Seconds()
	inserted, err := store.EnqueueWebhook(ctx, state.WebhookDelivery{
		DeliveryID: "assurance-delivery-00001", EventType: "repository",
		Payload: json.RawMessage("{\"repository_id\":1}"), ReceivedAt: webhookReceivedAt,
	})
	if err != nil || inserted {
		return durableStateResult{}, fmt.Errorf("idempotent webhook replay was not preserved")
	}
	_, conflictErr := store.EnqueueWebhook(ctx, state.WebhookDelivery{
		DeliveryID: "assurance-delivery-00001", EventType: "repository",
		Payload: json.RawMessage("{\"repository_id\":999999}"), ReceivedAt: webhookReceivedAt,
	})
	if !errors.Is(conflictErr, state.ErrWebhookConflict) {
		return durableStateResult{}, fmt.Errorf("webhook conflict did not fail closed: %v", conflictErr)
	}

	if err := store.Close(); err != nil {
		return durableStateResult{}, err
	}
	closed = true
	restartStarted := time.Now()
	store, err = state.Open(ctx, statePath)
	restartMS := milliseconds(time.Since(restartStarted))
	if err != nil {
		return durableStateResult{}, err
	}
	closed = false
	summary, err := store.Summary(ctx)
	if err != nil || summary.Observations != options.RepositoryCount ||
		summary.Reconciliations != 1 || summary.Webhooks != options.WebhookDeliveryCount {
		return durableStateResult{}, fmt.Errorf("restart summary mismatch: %+v err=%v", summary, err)
	}
	queue, err := store.WebhookQueueSummary(ctx)
	if err != nil || queue.Succeeded != options.WebhookDeliveryCount || queue.Queued != 0 ||
		queue.Processing != 0 || queue.Failed != 0 || queue.DeadLetter != 0 {
		return durableStateResult{}, fmt.Errorf(
			"webhook queue did not recover exactly: %+v err=%v", queue, err,
		)
	}
	for index, accessState := range accessStates {
		observation, err := store.GetRepositoryObservation(
			ctx, "installation:github-organization", int64((index+1)*2),
		)
		if err != nil || observation.AccessState != accessState || len(observation.Body) != 0 {
			return durableStateResult{}, fmt.Errorf("access state %s was not preserved", accessState)
		}
	}

	secondObserved := firstObserved.Add(2 * time.Hour)
	secondInventories := fixtureInventories(fixtures, secondObserved)
	personal = &fixtureReader{inventory: secondInventories["installation:github-personal"]}
	organization = &fixtureReader{err: errors.New("simulated provider outage")}
	exampleMedia = &fixtureReader{inventory: secondInventories["installation:github-example-media"]}
	openNetwork = &fixtureReader{inventory: secondInventories["installation:github-opennetwork"]}
	second, err := (controller.ReconciliationRunner{
		Store: store, Config: config,
		Readers: map[string]reconciler.InstallationReader{
			"installation:github-personal":      personal,
			"installation:github-organization":  organization,
			"installation:github-example-media": exampleMedia,
			"installation:github-guild":         guild,
			"installation:github-opennetwork":   openNetwork,
		},
		Concurrency:     options.ReconciliationConcurrency,
		MaxRepositories: options.RepositoryCount,
		Now:             func() time.Time { return secondObserved }, Audit: fixtureAudit{},
	}).Run(ctx)
	if err != nil || second.Status != "partial" ||
		len(second.Result.Inventory.Repositories) != len(secondInventories["installation:github-personal"].Repositories)+len(secondInventories["installation:github-example-media"].Repositories)+len(secondInventories["installation:github-opennetwork"].Repositories) ||
		!hasFinding(second.Result.Findings, "GDS_RECONCILE_INSTALLATION_NOT_PROVEN") {
		return durableStateResult{}, fmt.Errorf(
			"installation outage was not isolated: status=%s err=%v", second.Status, err,
		)
	}
	for index, accessState := range accessStates {
		observation, err := store.GetRepositoryObservation(
			ctx, "installation:github-organization", int64((index+1)*2),
		)
		if err != nil || observation.AccessState != accessState {
			return durableStateResult{}, fmt.Errorf(
				"outage overwrote preserved access state %s", accessState,
			)
		}
	}
	databaseBytes, err := stateFilesSize(statePath)
	if err != nil {
		return durableStateResult{}, err
	}
	return durableStateResult{
		ReconciliationMS: reconciliationMS, WebhookThroughput: throughput,
		QueueMaxLagMS: milliseconds(maxLag), RestartMS: restartMS,
		DatabaseBytes: databaseBytes, FullReconciliationCalls: fullCalls,
	}, nil
}

func exerciseKillSwitches() error {
	values := map[string]string{
		operations.MutationsDisabledEnvironment:    "true",
		operations.WebhookReadOnlyEnvironment:      "true",
		operations.RolloutPausedEnvironment:        "true",
		operations.HarnessHooksDisabledEnvironment: "true",
	}
	switches, err := operations.LoadKillSwitches(func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	})
	if err != nil || !switches.MutationsDisabled || !switches.WebhookProcessingReadOnly ||
		!switches.RolloutPaused || !switches.HarnessHooksDisabled {
		return fmt.Errorf("kill-switch activation contract failed: %+v %v", switches, err)
	}
	failClosed, err := operations.LoadKillSwitches(func(string) (string, bool) {
		return "invalid", true
	})
	if err == nil || !failClosed.MutationsDisabled {
		return fmt.Errorf("invalid kill-switch value did not fail closed")
	}
	return nil
}

func stateFilesSize(path string) (int64, error) {
	total := int64(0)
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("state file %s is not regular", candidate)
		}
		total += info.Size()
	}
	return total, nil
}
