package githubmutationruntime

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

type BuildOptions struct {
	BaseURL               string
	HTTPClient            *http.Client
	AllowInsecureLoopback bool
	Wait                  githubprovider.MutationWait
}

func BuildMutators(
	config Config,
	readConfig githubruntime.Config,
	desired estate.Config,
	options BuildOptions,
) (map[string]*githubprovider.Mutator, error) {
	if config.SecretStore.Provider == "gh-cli" {
		return buildCLIMutators(config, readConfig, desired, options)
	}
	if err := ValidateSeparation(readConfig, config, desired); err != nil {
		return nil, err
	}
	store, err := githubruntime.BuildSecretStore(config.SecretStore)
	if err != nil {
		return nil, err
	}
	desiredByID := make(map[string]estate.MutationCapability, len(desired.Mutations))
	for _, capability := range desired.Mutations {
		desiredByID[capability.Mutation.ID] = capability
	}
	mutators := make(map[string]*githubprovider.Mutator, len(config.GitHub.Capabilities))
	for logicalID, runtimeCapability := range config.GitHub.Capabilities {
		desiredCapability, found := desiredByID[logicalID]
		if !found {
			return nil, fmt.Errorf("GitHub mutation capability %q is not declared by the estate", logicalID)
		}
		permissionContract, err := githubprovider.NewPermissionContract(
			desiredCapability.Permissions.Repository,
			desiredCapability.Permissions.Organization,
			desiredCapability.Permissions.RepositorySelection,
		)
		if err != nil {
			return nil, fmt.Errorf("build GitHub mutation permission contract for %q: %w", logicalID, err)
		}
		tokens, err := githubprovider.NewAppTokenSource(githubprovider.AppTokenConfig{
			AppID: runtimeCapability.AppID,
			InstallationIDs: map[string]string{
				logicalID: runtimeCapability.ProviderInstallationID,
			},
			PrivateKeyReference:   desiredCapability.Credentials.SecretRef,
			Secrets:               store,
			BaseURL:               options.BaseURL,
			HTTPClient:            options.HTTPClient,
			AllowInsecureLoopback: options.AllowInsecureLoopback,
		})
		if err != nil {
			return nil, fmt.Errorf("build GitHub mutation token source for %q: %w", logicalID, err)
		}
		mutator, err := githubprovider.NewMutator(githubprovider.MutatorConfig{
			Client: githubprovider.Config{
				BaseURL: options.BaseURL, HTTPClient: options.HTTPClient,
				TokenSource: tokens, InstallationID: logicalID,
				MaxResponseBytes:      githubprovider.DefaultBodyLimit,
				AllowInsecureLoopback: options.AllowInsecureLoopback,
				PermissionContract:    permissionContract,
			},
			Operations:     desiredCapability.Operations,
			MinimumSpacing: time.Duration(config.GitHub.MinimumMutationSpacingMS) * time.Millisecond,
			Wait:           options.Wait,
		})
		if err != nil {
			return nil, fmt.Errorf("build GitHub mutator for %q: %w", logicalID, err)
		}
		mutators[logicalID] = mutator
	}
	return mutators, nil
}

// buildCLIMutators builds mutation clients backed by the gh CLI personal access
// token. A single PAT principal cannot satisfy ADR 0023's distinct-App-identity
// separation, so separation is enforced at the capability boundary instead: a
// mutation capability exposes only its declared write operations, the read
// clients expose no write methods, and the permission contract uses superset
// validation. The estate still gates the canonical mutation_mode before apply.
func buildCLIMutators(
	config Config,
	readConfig githubruntime.Config,
	desired estate.Config,
	options BuildOptions,
) (map[string]*githubprovider.Mutator, error) {
	if err := ValidateCLISeparation(readConfig, config, desired); err != nil {
		return nil, err
	}
	desiredByID := make(map[string]estate.MutationCapability, len(desired.Mutations))
	for _, capability := range desired.Mutations {
		desiredByID[capability.Mutation.ID] = capability
	}
	readInstallations := make(map[string]estate.Installation, len(desired.Installations))
	for _, installation := range desired.Installations {
		readInstallations[installation.Installation.ID] = installation
	}
	mutators := make(map[string]*githubprovider.Mutator, len(config.GitHub.Capabilities))
	for logicalID, runtimeCapability := range config.GitHub.Capabilities {
		desiredCapability, found := desiredByID[logicalID]
		if !found {
			return nil, fmt.Errorf("GitHub mutation capability %q is not declared by the estate", logicalID)
		}
		installation, found := readInstallations[desiredCapability.Mutation.Installation]
		if !found {
			return nil, fmt.Errorf(
				"GitHub mutation capability %q targets an undeclared installation", logicalID,
			)
		}
		permissionContract, err := githubprovider.NewPermissionContract(
			desiredCapability.Permissions.Repository,
			desiredCapability.Permissions.Organization,
			desiredCapability.Permissions.RepositorySelection,
		)
		if err != nil {
			return nil, fmt.Errorf("build GitHub mutation permission contract for %q: %w", logicalID, err)
		}
		permissionContract.Mode = githubprovider.PermissionModeSuperset
		// The gh CLI mutation capability must bind the same account the estate
		// declares for its target installation, mirroring the read runtime's
		// exact-account check.
		desiredAccount := installation.Installation
		if runtimeCapability.AccountLogin == "" || runtimeCapability.AccountType == "" {
			return nil, fmt.Errorf(
				"GitHub gh CLI mutation capability %q is missing its account login or type", logicalID,
			)
		}
		if !strings.EqualFold(runtimeCapability.AccountLogin, desiredAccount.AccountLogin) ||
			runtimeCapability.AccountType != desiredAccount.AccountType {
			return nil, fmt.Errorf(
				"GitHub gh CLI mutation capability %q account does not match the estate", logicalID,
			)
		}
		tokens, err := githubprovider.NewCLITokenSource(githubprovider.CLITokenConfig{
			AccountLogin:          runtimeCapability.AccountLogin,
			AccountType:           runtimeCapability.AccountType,
			BaseURL:               options.BaseURL,
			HTTPClient:            options.HTTPClient,
			AllowInsecureLoopback: options.AllowInsecureLoopback,
		})
		if err != nil {
			return nil, fmt.Errorf("build GitHub gh CLI mutation token source for %q: %w", logicalID, err)
		}
		mutator, err := githubprovider.NewMutator(githubprovider.MutatorConfig{
			Client: githubprovider.Config{
				BaseURL: options.BaseURL, HTTPClient: options.HTTPClient,
				TokenSource: tokens, InstallationID: logicalID,
				MaxResponseBytes:      githubprovider.DefaultBodyLimit,
				AllowInsecureLoopback: options.AllowInsecureLoopback,
				PermissionContract:    permissionContract,
			},
			Operations:     desiredCapability.Operations,
			MinimumSpacing: time.Duration(config.GitHub.MinimumMutationSpacingMS) * time.Millisecond,
			Wait:           options.Wait,
		})
		if err != nil {
			return nil, fmt.Errorf("build GitHub gh CLI mutator for %q: %w", logicalID, err)
		}
		mutators[logicalID] = mutator
	}
	return mutators, nil
}
