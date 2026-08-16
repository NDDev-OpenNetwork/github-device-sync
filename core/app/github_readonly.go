package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/governance"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
)

type GitHubReadOptions struct {
	RuntimeConfig   string
	EstateRoot      string
	InstallationID  string
	MaxRepositories int
}

type GitHubGovernanceOptions struct {
	GitHubReadOptions
	Owner        string
	Repository   string
	CompareLocal bool
}

type GitHubInventoryData struct {
	Inventory       githubprovider.Inventory `json:"inventory"`
	AccountLogin    string                   `json:"account_login"`
	MaxRepositories int                      `json:"max_repositories"`
}

type GitHubGovernanceData struct {
	Snapshot   githubprovider.GovernanceSnapshot `json:"snapshot"`
	Comparison governance.Result                 `json:"comparison"`
}

type ReconciliationPlanData struct {
	Kind              string            `json:"kind"`
	MutationMode      string            `json:"mutation_mode"`
	ExternalMutations []string          `json:"external_mutations"`
	Result            reconciler.Result `json:"result"`
}

type githubRuntime struct {
	desired         estate.Config
	config          githubruntime.Config
	readers         map[string]*githubprovider.Client
	maxRepositories int
}

func (services *Services) GitHubInventory(
	ctx context.Context,
	path string,
	options GitHubReadOptions,
) domain.Envelope {
	if strings.TrimSpace(options.InstallationID) == "" {
		return domain.NewEnvelope("gds github inventory", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_GITHUB_INSTALLATION_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--installation must name one logical estate installation.",
		})
	}
	runtime, envelope := services.loadGitHubRuntime(ctx, path, options, "gds github inventory")
	if envelope != nil {
		return *envelope
	}
	installationID := normalizeInstallationID(options.InstallationID, runtime.readers)
	reader, found := runtime.readers[installationID]
	if !found {
		return domain.NewEnvelope("gds github inventory", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_GITHUB_INSTALLATION_UNKNOWN", Severity: domain.SeverityHigh,
			Message:  "The requested installation is not declared by the current estate.",
			Evidence: map[string]any{"installation": options.InstallationID},
		})
	}
	inventory, err := reader.ListInstallationRepositories(ctx, runtime.maxRepositories)
	if err != nil {
		return githubReadError("gds github inventory", installationID, err)
	}
	account := installationAccount(runtime.desired, installationID)
	for _, repository := range inventory.Repositories {
		if !strings.EqualFold(repository.Owner, account) {
			finding := domain.Finding{
				Code:     "GDS_GITHUB_INSTALLATION_ACCOUNT_MISMATCH",
				Severity: domain.SeverityCritical,
				Message:  "GitHub returned a repository outside the declared installation account.",
				Evidence: map[string]any{
					"installation": installationID, "expected_account": account,
				},
			}
			return domain.NewEnvelope("gds github inventory", domain.ExitSecurity, nil, finding)
		}
	}
	envelopeValue := domain.Success("gds github inventory", GitHubInventoryData{
		Inventory: inventory, AccountLogin: account, MaxRepositories: runtime.maxRepositories,
	})
	envelopeValue.Scope["installation_id"] = installationID
	return envelopeValue
}

func (services *Services) GitHubGovernance(
	ctx context.Context,
	path string,
	options GitHubGovernanceOptions,
) domain.Envelope {
	command := "gds github governance"
	if strings.TrimSpace(options.InstallationID) == "" ||
		strings.TrimSpace(options.Owner) == "" || strings.TrimSpace(options.Repository) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_GITHUB_GOVERNANCE_SCOPE_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--installation, --owner, and --repository must identify one exact repository.",
		})
	}
	runtime, envelope := services.loadGitHubRuntime(
		ctx, path, options.GitHubReadOptions, command,
	)
	if envelope != nil {
		return *envelope
	}
	installationID := normalizeInstallationID(options.InstallationID, runtime.readers)
	reader, found := runtime.readers[installationID]
	if !found {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_GITHUB_INSTALLATION_UNKNOWN", Severity: domain.SeverityHigh,
			Message:  "The requested installation is not declared by the current estate.",
			Evidence: map[string]any{"installation": options.InstallationID},
		})
	}
	expectedAccount := installationAccount(runtime.desired, installationID)
	if expectedAccount == "" || !strings.EqualFold(options.Owner, expectedAccount) {
		return domain.NewEnvelope(command, domain.ExitSecurity, nil, domain.Finding{
			Code: "GDS_GITHUB_GOVERNANCE_ACCOUNT_MISMATCH", Severity: domain.SeverityCritical,
			Message: "Requested governance scope is outside the declared installation account.",
			Evidence: map[string]any{
				"installation": options.InstallationID, "expected_account": expectedAccount,
			},
		})
	}
	snapshot, err := reader.GetRepositoryGovernance(ctx, options.Owner, options.Repository)
	if err != nil {
		return githubReadError(command, installationID, err)
	}
	if snapshot.InstallationID != installationID ||
		!strings.EqualFold(snapshot.Repository.Owner, expectedAccount) ||
		!strings.EqualFold(snapshot.Repository.Name, options.Repository) {
		return domain.NewEnvelope(command, domain.ExitSecurity, nil, domain.Finding{
			Code: "GDS_GITHUB_GOVERNANCE_IDENTITY_MISMATCH", Severity: domain.SeverityCritical,
			Message: "GitHub governance evidence does not match the requested repository identity.",
		})
	}
	comparison := governance.Result{
		Status: "observed-only", Counts: map[string]int{}, Fields: []governance.FieldResult{},
	}
	if options.CompareLocal {
		estateRoot, anchor, findings := services.policyInputs(ctx, path)
		if len(findings) != 0 {
			return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		}
		if anchor.Provider.RepositoryID != snapshot.Repository.ID ||
			!strings.EqualFold(anchor.Provider.Owner, snapshot.Repository.Owner) ||
			!strings.EqualFold(anchor.Provider.Name, snapshot.Repository.Name) {
			return domain.NewEnvelope(command, domain.ExitSecurity, nil, domain.Finding{
				Code:     "GDS_GITHUB_GOVERNANCE_LOCAL_IDENTITY_MISMATCH",
				Severity: domain.SeverityCritical,
				Message:  "Local repository identity does not match the observed GitHub repository.",
			})
		}
		compiled := services.Compiler.CompileDirectory(
			estateRoot, anchor, compiler.DevelopmentBundleVersion,
		)
		if len(compiled.Findings) != 0 {
			return domain.NewEnvelope(
				command, classifyFindings(compiled.Findings), nil, compiled.Findings...,
			)
		}
		comparison = governance.Compare(compiled.Document, snapshot)
	}
	envelopeValue := domain.Success(command, GitHubGovernanceData{
		Snapshot: snapshot, Comparison: comparison,
	})
	envelopeValue.Scope["installation_id"] = installationID
	envelopeValue.Scope["repository_id"] = snapshot.Repository.ID
	return envelopeValue
}

func (services *Services) ReconcileGitHub(
	ctx context.Context,
	path string,
	options GitHubReadOptions,
) domain.Envelope {
	runtime, envelope := services.loadGitHubRuntime(ctx, path, options, "gds reconcile")
	if envelope != nil {
		return *envelope
	}
	readers := make(map[string]reconciler.InstallationReader, len(runtime.readers))
	for id, reader := range runtime.readers {
		readers[id] = reader
	}
	result := (reconciler.Reconciler{
		Config: runtime.desired, Readers: readers,
		Concurrency:     runtime.desired.Root.Rollout.MaxParallelObservation,
		MaxRepositories: runtime.maxRepositories,
	}).ReconcileAll(ctx)
	class := domain.ExitSuccess
	if len(result.Findings) != 0 {
		class = domain.ExitNotProven
	}
	envelopeValue := domain.NewEnvelope("gds reconcile", class, ReconciliationPlanData{
		Kind: "read-only-reconciliation", MutationMode: "none",
		ExternalMutations: []string{}, Result: result,
	}, result.Findings...)
	envelopeValue.Scope["estate_id"] = runtime.desired.Root.Estate.ID
	return envelopeValue
}

func (services *Services) loadGitHubRuntime(
	ctx context.Context,
	path string,
	options GitHubReadOptions,
	command string,
) (githubRuntime, *domain.Envelope) {
	root := options.EstateRoot
	if root == "" {
		resolved := services.Context.Resolve(ctx, path)
		root = resolved.Context.Estate.Root
		if root == "" {
			info, err := services.Git.RepositoryInfo(ctx, path)
			if err != nil {
				envelope := envelopeForError(command, path, err)
				return githubRuntime{}, &envelope
			}
			root = info.WorktreeRoot
		}
	}
	desired, findings := estate.Load(root, services.Schemas)
	if len(findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return githubRuntime{}, &envelope
	}
	runtimeConfigPath := options.RuntimeConfig
	if runtimeConfigPath == "" {
		var pathErr error
		runtimeConfigPath, pathErr = githubruntime.DefaultPath()
		if pathErr != nil {
			envelope := githubRuntimeError(command, pathErr)
			return githubRuntime{}, &envelope
		}
	}
	config, err := githubruntime.Load(runtimeConfigPath, desired, services.Schemas)
	if err != nil {
		envelope := githubRuntimeError(command, err)
		return githubRuntime{}, &envelope
	}
	maxRepositories := config.GitHub.MaxRepositories
	if options.MaxRepositories != 0 {
		if options.MaxRepositories < 1 || options.MaxRepositories > maxRepositories {
			envelope := domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
				Code: "GDS_GITHUB_REPOSITORY_LIMIT_INVALID", Severity: domain.SeverityHigh,
				Message:  "--max-repositories must be positive and cannot exceed the runtime safety bound.",
				Evidence: map[string]any{"runtime_limit": maxRepositories},
			})
			return githubRuntime{}, &envelope
		}
		maxRepositories = options.MaxRepositories
	}
	readers, err := githubruntime.BuildReaders(
		config, desired, services.GitHubRuntimeBuildOptions,
	)
	if err != nil {
		envelope := githubRuntimeError(command, err)
		return githubRuntime{}, &envelope
	}
	return githubRuntime{
		desired: desired, config: config, readers: readers, maxRepositories: maxRepositories,
	}, nil
}

func githubRuntimeError(command string, err error) domain.Envelope {
	class := domain.ExitNotProven
	code := "GDS_GITHUB_RUNTIME_NOT_PROVEN"
	message := "GitHub runtime configuration or credential binding is unavailable."
	var runtimeError *githubruntime.Error
	if errors.As(err, &runtimeError) {
		switch runtimeError.Kind {
		case githubruntime.ErrorSecurity:
			class, code = domain.ExitSecurity, "GDS_GITHUB_RUNTIME_SECURITY_VIOLATION"
			message = "GitHub runtime configuration violates the private-file security contract."
		case githubruntime.ErrorInvalid:
			class, code = domain.ExitInput, "GDS_GITHUB_RUNTIME_INVALID"
			message = "GitHub runtime configuration does not satisfy its schema contract."
		case githubruntime.ErrorEstateMismatch:
			class, code = domain.ExitPolicy, "GDS_GITHUB_RUNTIME_ESTATE_MISMATCH"
			message = "GitHub runtime bindings do not exactly match the canonical estate."
		case githubruntime.ErrorUnsupported:
			class, code = domain.ExitUnsupported, "GDS_GITHUB_RUNTIME_UNSUPPORTED"
			message = "The selected GitHub secret backend is unsupported on this device."
		}
	}
	return domain.NewEnvelope(command, class, nil, domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: map[string]any{"error_type": fmt.Sprintf("%T", err)},
	})
}

func githubReadError(command string, installationID string, err error) domain.Envelope {
	class := domain.ExitNotProven
	code := "GDS_GITHUB_INVENTORY_NOT_PROVEN"
	message := "GitHub installation inventory could not be proven."
	evidence := map[string]any{
		"installation": installationID, "error_type": fmt.Sprintf("%T", err),
	}
	var apiError *githubprovider.APIError
	if errors.As(err, &apiError) {
		evidence["provider_error_kind"] = apiError.Kind
		if apiError.RequestID != "" {
			evidence["request_id"] = apiError.RequestID
		}
		switch apiError.Kind {
		case githubprovider.ErrorAuthentication, githubprovider.ErrorAuthorization:
			class, code = domain.ExitAuthorization, "GDS_GITHUB_AUTHORIZATION_FAILED"
			message = "GitHub App authentication or installation authorization failed."
		case githubprovider.ErrorPermissionContract:
			class, code = domain.ExitSecurity, "GDS_GITHUB_PERMISSION_CONTRACT_MISMATCH"
			message = "Effective GitHub App permissions do not exactly match canonical estate intent."
		case githubprovider.ErrorRateLimited, githubprovider.ErrorTransient:
			class, code = domain.ExitProviderTransient, "GDS_GITHUB_PROVIDER_TRANSIENT"
			message = "GitHub could not provide current inventory because of a transient provider condition."
		}
	}
	return domain.NewEnvelope(command, class, nil, domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence,
	})
}

func installationAccount(config estate.Config, id string) string {
	for _, installation := range config.Installations {
		if installation.Installation.ID == id {
			return installation.Installation.AccountLogin
		}
	}
	return ""
}

// normalizeInstallationID accepts the canonical estate installation id form
// ("installation:github-personal") and a short convenience form without the
// "installation:" prefix ("github-personal"). It returns the input unchanged
// when it already matches a declared reader key; otherwise, when the prefix is
// absent, it retries with the canonical prefix. The original input is still
// surfaced verbatim in error evidence by the callers so diagnostics stay honest.
func normalizeInstallationID(input string, readers map[string]*githubprovider.Client) string {
	if _, found := readers[input]; found {
		return input
	}
	const prefix = "installation:"
	if strings.HasPrefix(input, prefix) {
		return input
	}
	prefixed := prefix + input
	if _, found := readers[prefixed]; found {
		return prefixed
	}
	return input
}
