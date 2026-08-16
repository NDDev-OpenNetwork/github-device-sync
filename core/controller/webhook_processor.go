package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type RepositoryReader interface {
	GetRepository(
		context.Context,
		string,
		string,
		string,
	) (githubprovider.Repository, githubprovider.ResponseMeta, bool, error)
	GetRepositoryGovernance(
		context.Context,
		string,
		string,
	) (githubprovider.GovernanceSnapshot, error)
}

type FullReconciler interface {
	Run(context.Context) (ReconciliationRunResult, error)
}

type RepositoryProcessor struct {
	Store                 *state.Store
	Readers               map[string]RepositoryReader
	Full                  FullReconciler
	ProviderInstallations map[int64]string
	Accounts              map[string]string
	Now                   func() time.Time
	NewReconciliationID   func(time.Time) (string, error)
}

type webhookEnvelope struct {
	Installation *struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository *struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

type safeControllerError string

func (controllerError safeControllerError) Error() string       { return string(controllerError) }
func (controllerError safeControllerError) SafeMessage() string { return string(controllerError) }

func NewRepositoryProcessor(
	store *state.Store,
	desired estate.Config,
	runtime githubruntime.Config,
	readers map[string]RepositoryReader,
	full FullReconciler,
) (*RepositoryProcessor, error) {
	if store == nil || full == nil || len(readers) != len(desired.Root.Installations) ||
		len(runtime.GitHub.Installations) != len(desired.Root.Installations) {
		return nil, fmt.Errorf("webhook processor dependencies are incomplete")
	}
	providerInstallations := make(map[int64]string, len(runtime.GitHub.Installations))
	for logical, installation := range runtime.GitHub.Installations {
		providerID, err := strconv.ParseInt(installation.ProviderInstallationID, 10, 64)
		if err != nil || providerID <= 0 {
			return nil, fmt.Errorf("invalid provider installation identity")
		}
		if _, duplicate := providerInstallations[providerID]; duplicate {
			return nil, fmt.Errorf("duplicate provider installation identity")
		}
		if readers[logical] == nil {
			return nil, fmt.Errorf("repository reader is missing for %q", logical)
		}
		providerInstallations[providerID] = logical
	}
	accounts := make(map[string]string, len(desired.Installations))
	for _, installation := range desired.Installations {
		accounts[installation.Installation.ID] = installation.Installation.AccountLogin
	}
	return &RepositoryProcessor{
		Store: store, Readers: readers, Full: full,
		ProviderInstallations: providerInstallations, Accounts: accounts,
	}, nil
}

func (processor *RepositoryProcessor) ProcessWebhook(
	ctx context.Context,
	delivery state.WebhookDelivery,
) error {
	if processor == nil || processor.Store == nil || processor.Full == nil {
		return &PermanentError{Err: safeControllerError("webhook processor is unavailable")}
	}
	var event webhookEnvelope
	if err := json.Unmarshal(delivery.Payload, &event); err != nil {
		return &PermanentError{Err: safeControllerError("webhook payload shape is invalid")}
	}
	if delivery.EventType == "ping" {
		return nil
	}
	if event.Installation == nil || event.Installation.ID <= 0 {
		return &PermanentError{Err: safeControllerError("webhook installation identity is missing")}
	}
	logicalInstallation, found := processor.ProviderInstallations[event.Installation.ID]
	if !found {
		return &PermanentError{Err: safeControllerError("webhook installation is outside the configured estate")}
	}
	if event.Repository == nil {
		run, err := processor.Full.Run(ctx)
		if err != nil {
			return err
		}
		if run.Status != "succeeded" {
			return errors.New("authoritative full reconciliation was incomplete")
		}
		return nil
	}
	repository := event.Repository
	expectedAccount := processor.Accounts[logicalInstallation]
	if repository.ID <= 0 || repository.Name == "" || repository.Owner.Login == "" ||
		!strings.EqualFold(repository.Owner.Login, expectedAccount) {
		return &PermanentError{Err: safeControllerError("webhook repository identity conflicts with estate scope")}
	}
	reader := processor.Readers[logicalInstallation]
	return processor.reconcileRepository(
		ctx, logicalInstallation, expectedAccount, repository.ID, repository.Name, reader,
		governanceEvent(delivery.EventType),
	)
}

func (processor *RepositoryProcessor) reconcileRepository(
	ctx context.Context,
	installationID string,
	owner string,
	repositoryID int64,
	name string,
	reader RepositoryReader,
	includeGovernance bool,
) error {
	now := processor.currentTime()
	newReconciliationID := func(value time.Time) (string, error) {
		return identity.New("reconciliation", value, nil)
	}
	if processor.NewReconciliationID != nil {
		newReconciliationID = processor.NewReconciliationID
	}
	reconciliationID, err := newReconciliationID(now)
	if err != nil {
		return err
	}
	if err := processor.Store.StartReconciliation(ctx, state.ReconciliationRecord{
		ReconciliationID: reconciliationID, Scope: "repository",
		ScopeID: fmt.Sprintf("%s/%d", installationID, repositoryID),
		Status:  "running", StartedAt: now,
	}); err != nil {
		return err
	}
	var observed githubprovider.Repository
	var body []byte
	var requestID string
	observedInstallation := installationID
	var readErr error
	if includeGovernance {
		var governance githubprovider.GovernanceSnapshot
		governance, readErr = reader.GetRepositoryGovernance(ctx, owner, name)
		if readErr == nil {
			observed = governance.Repository
			observedInstallation = governance.InstallationID
			requestID = lastString(governance.RequestIDs)
			body, readErr = json.Marshal(governance)
		}
	} else {
		var meta githubprovider.ResponseMeta
		observed, meta, _, readErr = reader.GetRepository(ctx, owner, name, "")
		requestID = meta.RequestID
		if readErr == nil {
			body, readErr = json.Marshal(observed)
		}
	}
	if readErr != nil {
		accessState, code, permanent := classifyRepositoryReadError(readErr)
		if providerID := providerRequestID(readErr); providerID != "" {
			requestID = providerID
		}
		observationErr := processor.Store.PutRepositoryObservation(ctx, state.RepositoryObservation{
			InstallationID: installationID, ProviderRepositoryID: repositoryID,
			Owner: owner, Name: name, AccessState: accessState,
			ObservedAt: now, RequestID: requestID,
		})
		if observationErr != nil {
			code = "GDS_RECONCILE_OBSERVATION_PERSIST_FAILED"
			readErr = observationErr
			permanent = false
		}
		finishErr := processor.Store.FinishReconciliation(
			context.WithoutCancel(ctx), reconciliationID, "blocked", processor.currentTime(),
			map[string]any{
				"installation_id": installationID, "repository_id": repositoryID,
				"access_state": accessState, "request_id": requestID,
			}, code,
		)
		if finishErr != nil {
			return finishErr
		}
		if permanent {
			return &PermanentError{Err: safeControllerError(code)}
		}
		return readErr
	}
	if observedInstallation != installationID || observed.ID != repositoryID ||
		!strings.EqualFold(observed.Owner, owner) || !strings.EqualFold(observed.Name, name) {
		code := "GDS_GITHUB_REPOSITORY_IDENTITY_MISMATCH"
		if err := processor.Store.FinishReconciliation(
			context.WithoutCancel(ctx), reconciliationID, "blocked", processor.currentTime(),
			map[string]any{"installation_id": installationID, "repository_id": repositoryID}, code,
		); err != nil {
			return err
		}
		return &PermanentError{Err: safeControllerError(code)}
	}
	err = processor.Store.PutRepositoryObservation(ctx, state.RepositoryObservation{
		InstallationID: installationID, ProviderRepositoryID: repositoryID,
		Owner: owner, Name: name, AccessState: "available",
		ObservedAt: now, Body: body, RequestID: requestID,
	})
	if err != nil {
		if finishErr := processor.Store.FinishReconciliation(
			context.WithoutCancel(ctx), reconciliationID, "failed", processor.currentTime(),
			map[string]any{"installation_id": installationID, "repository_id": repositoryID},
			"GDS_RECONCILE_OBSERVATION_PERSIST_FAILED",
		); finishErr != nil {
			return finishErr
		}
		return err
	}
	return processor.Store.FinishReconciliation(
		context.WithoutCancel(ctx), reconciliationID, "succeeded", processor.currentTime(),
		map[string]any{
			"installation_id": installationID, "repository_id": repositoryID,
			"access_state": "available", "request_id": requestID,
		}, "",
	)
}

func (processor *RepositoryProcessor) currentTime() time.Time {
	if processor.Now != nil {
		return processor.Now().UTC()
	}
	return time.Now().UTC()
}

func governanceEvent(eventType string) bool {
	switch eventType {
	case "repository", "repository_ruleset", "security_and_analysis",
		"branch_protection_configuration", "branch_protection_rule":
		return true
	default:
		return false
	}
}

func classifyRepositoryReadError(err error) (string, string, bool) {
	var apiError *githubprovider.APIError
	if !errors.As(err, &apiError) {
		return "unknown", "GDS_GITHUB_PROVIDER_TRANSIENT", false
	}
	switch apiError.Kind {
	case githubprovider.ErrorAuthentication:
		return "auth-failed", "GDS_GITHUB_AUTHORIZATION_FAILED", false
	case githubprovider.ErrorAuthorization, githubprovider.ErrorNotFoundOrInaccessible:
		return "inaccessible", "GDS_GITHUB_REPOSITORY_INACCESSIBLE", true
	case githubprovider.ErrorRateLimited, githubprovider.ErrorTransient:
		return "unknown", "GDS_GITHUB_PROVIDER_TRANSIENT", false
	default:
		return "unknown", "GDS_GITHUB_PROVIDER_RESPONSE_INVALID", true
	}
}

func providerRequestID(err error) string {
	var apiError *githubprovider.APIError
	if errors.As(err, &apiError) {
		return apiError.RequestID
	}
	return ""
}

func lastString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}
