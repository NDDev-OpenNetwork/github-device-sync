package githubruntime

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/secrets"
)

type BuildOptions struct {
	BaseURL               string
	HTTPClient            *http.Client
	AllowInsecureLoopback bool
	// CLIRunner overrides how the gh-cli credential path executes
	// `gh auth token`. Left nil in production, where NewCLITokenSource
	// installs its sandboxed ExecRunner. A test supplies a stub so the
	// gh-cli branch can be exercised without an authenticated gh CLI on the
	// machine running the test: otherwise this branch is only reachable on a
	// developer laptop and never in CI, which is the same as untested.
	CLIRunner githubprovider.CommandRunner
}

func BuildReaders(
	config Config,
	desired estate.Config,
	options BuildOptions,
) (map[string]*githubprovider.Client, error) {
	desiredByID := make(map[string]estate.Installation, len(desired.Installations))
	for _, installation := range desired.Installations {
		desiredByID[installation.Installation.ID] = installation
	}
	if config.SecretStore.Provider == "gh-cli" {
		return buildCLIReaders(config, desiredByID, options)
	}
	store, err := BuildSecretStore(config.SecretStore)
	if err != nil {
		return nil, err
	}
	readers := make(map[string]*githubprovider.Client, len(config.GitHub.Installations))
	for logicalID, runtimeInstallation := range config.GitHub.Installations {
		desiredInstallation, found := desiredByID[logicalID]
		if !found {
			return nil, fmt.Errorf("GitHub runtime installation %q is not declared by the estate", logicalID)
		}
		permissionContract, err := githubprovider.NewPermissionContract(
			desiredInstallation.Permissions.Repository,
			desiredInstallation.Permissions.Organization,
			desiredInstallation.Permissions.RepositorySelection,
		)
		if err != nil {
			return nil, fmt.Errorf("build GitHub permission contract for %q: %w", logicalID, err)
		}
		tokens, err := githubprovider.NewAppTokenSource(githubprovider.AppTokenConfig{
			AppID: runtimeInstallation.AppID,
			InstallationIDs: map[string]string{
				logicalID: runtimeInstallation.ProviderInstallationID,
			},
			PrivateKeyReference:   desiredInstallation.Credentials.SecretRef,
			Secrets:               store,
			BaseURL:               options.BaseURL,
			HTTPClient:            options.HTTPClient,
			AllowInsecureLoopback: options.AllowInsecureLoopback,
		})
		if err != nil {
			return nil, fmt.Errorf("build GitHub App token source for %q: %w", logicalID, err)
		}
		client, err := githubprovider.NewClient(githubprovider.Config{
			BaseURL: options.BaseURL, HTTPClient: options.HTTPClient,
			TokenSource: tokens, InstallationID: logicalID,
			MaxResponseBytes:      githubprovider.DefaultBodyLimit,
			AllowInsecureLoopback: options.AllowInsecureLoopback,
			PermissionContract:    permissionContract,
		})
		if err != nil {
			return nil, fmt.Errorf("build GitHub reader for %q: %w", logicalID, err)
		}
		readers[logicalID] = client
	}
	return readers, nil
}

// buildCLIReaders builds one read client per installation backed by the gh CLI
// personal access token. A single gh account can serve both an organization and
// a personal installation, so each logical installation resolves its account
// (login + type) from the estate declaration and constructs an isolated
// CLITokenSource. The permission contract uses superset validation because the
// PAT's OAuth scopes are a coarse superset of the fine-grained installation
// permission map (ADR 0034).
func buildCLIReaders(
	config Config,
	desiredByID map[string]estate.Installation,
	options BuildOptions,
) (map[string]*githubprovider.Client, error) {
	readers := make(map[string]*githubprovider.Client, len(config.GitHub.Installations))
	for logicalID, runtimeInstallation := range config.GitHub.Installations {
		desiredInstallation, found := desiredByID[logicalID]
		if !found {
			return nil, fmt.Errorf("GitHub runtime installation %q is not declared by the estate", logicalID)
		}
		permissionContract, err := githubprovider.NewPermissionContract(
			desiredInstallation.Permissions.Repository,
			desiredInstallation.Permissions.Organization,
			desiredInstallation.Permissions.RepositorySelection,
		)
		if err != nil {
			return nil, fmt.Errorf("build GitHub permission contract for %q: %w", logicalID, err)
		}
		permissionContract.Mode = githubprovider.PermissionModeSuperset
		// The gh CLI variant is bound to a GitHub account, not a GitHub App id.
		// The runtime-declared account must exactly match the estate-declared
		// account so a device cannot silently observe a different installation.
		desiredAccount := desiredInstallation.Installation
		if runtimeInstallation.AccountLogin == "" || runtimeInstallation.AccountType == "" {
			return nil, fmt.Errorf(
				"GitHub gh-cli installation %q is missing its account login or type", logicalID,
			)
		}
		if !strings.EqualFold(runtimeInstallation.AccountLogin, desiredAccount.AccountLogin) ||
			runtimeInstallation.AccountType != desiredAccount.AccountType {
			return nil, fmt.Errorf(
				"GitHub gh-cli installation %q account does not match the estate", logicalID,
			)
		}
		tokens, err := githubprovider.NewCLITokenSource(githubprovider.CLITokenConfig{
			AccountLogin:          runtimeInstallation.AccountLogin,
			AccountType:           runtimeInstallation.AccountType,
			BaseURL:               options.BaseURL,
			HTTPClient:            options.HTTPClient,
			AllowInsecureLoopback: options.AllowInsecureLoopback,
			Runner:                options.CLIRunner,
		})
		if err != nil {
			return nil, fmt.Errorf("build GitHub gh CLI token source for %q: %w", logicalID, err)
		}
		client, err := githubprovider.NewClient(githubprovider.Config{
			BaseURL: options.BaseURL, HTTPClient: options.HTTPClient,
			TokenSource: tokens, InstallationID: logicalID,
			MaxResponseBytes:      githubprovider.DefaultBodyLimit,
			AllowInsecureLoopback: options.AllowInsecureLoopback,
			PermissionContract:    permissionContract,
			InventoryAccount: githubprovider.InventoryAccount{
				Login: runtimeInstallation.AccountLogin, Type: runtimeInstallation.AccountType,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("build GitHub gh CLI reader for %q: %w", logicalID, err)
		}
		readers[logicalID] = client
	}
	return readers, nil
}

func BuildSecretStore(config SecretStoreConfig) (secrets.Store, error) {
	switch config.Provider {
	case "environment":
		return secrets.EnvironmentStore{References: config.References}, nil
	case "file":
		return secrets.FileStore{References: config.References}, nil
	case "macos-keychain":
		if runtime.GOOS != "darwin" {
			return nil, &Error{
				Kind:  ErrorUnsupported,
				Cause: fmt.Errorf("macOS keychain secret store is unsupported on %s", runtime.GOOS),
			}
		}
		return secrets.MacOSKeychainStore{
			Service: config.Service, Accounts: config.References,
		}, nil
	case "linux-secret-service":
		if runtime.GOOS != "linux" {
			return nil, &Error{
				Kind:  ErrorUnsupported,
				Cause: fmt.Errorf("Linux Secret Service store is unsupported on %s", runtime.GOOS),
			}
		}
		return secrets.LinuxSecretServiceStore{
			CollectionAttribute: config.Attribute, References: config.References,
		}, nil
	default:
		return nil, &Error{
			Kind:  ErrorUnsupported,
			Cause: fmt.Errorf("unsupported secret store %q", config.Provider),
		}
	}
}
