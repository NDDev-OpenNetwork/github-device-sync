package githubmutationruntime

import (
	"errors"
	"fmt"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/privateconfig"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func Load(path string, desired estate.Config, schemas *validation.Set) (Config, error) {
	absolute, raw, err := privateconfig.Read(path)
	if err != nil {
		kind := ErrorUnavailable
		var configError *privateconfig.Error
		if errors.As(err, &configError) && configError.Kind == privateconfig.ErrorSecurity {
			kind = ErrorSecurity
		}
		return Config{}, &Error{Kind: kind, Cause: err}
	}
	value, err := serialization.Decode(absolute, raw)
	if err != nil {
		return Config{}, &Error{Kind: ErrorInvalid, Cause: err}
	}
	if findings := schemas.Validate("github-mutation-runtime", value, absolute); len(findings) != 0 {
		return Config{}, &Error{
			Kind:  ErrorInvalid,
			Cause: fmt.Errorf("schema validation failed with %s", findings[0].Code),
		}
	}
	var config Config
	if err := serialization.DecodeInto(absolute, raw, &config); err != nil {
		return Config{}, &Error{Kind: ErrorInvalid, Cause: err}
	}
	if err := validateAgainstEstate(config, desired); err != nil {
		return Config{}, &Error{Kind: ErrorEstateMismatch, Cause: err}
	}
	return config, nil
}

func validateAgainstEstate(config Config, desired estate.Config) error {
	desiredByID := make(map[string]estate.MutationCapability, len(desired.Mutations))
	requiredReferences := make(map[string]struct{}, len(desired.Mutations))
	installations := make(map[string]struct{}, len(desired.Mutations))
	for _, capability := range desired.Mutations {
		id := capability.Mutation.ID
		desiredByID[id] = capability
		requiredReferences[capability.Credentials.SecretRef] = struct{}{}
		if _, duplicate := installations[capability.Mutation.Installation]; duplicate {
			return fmt.Errorf("more than one mutation capability targets one installation")
		}
		installations[capability.Mutation.Installation] = struct{}{}
	}
	if len(config.GitHub.Capabilities) != len(desiredByID) {
		return fmt.Errorf("GitHub mutation runtime capability set does not exactly match estate intent")
	}
	for id := range config.GitHub.Capabilities {
		if _, found := desiredByID[id]; !found {
			return fmt.Errorf("GitHub mutation runtime contains unknown capability %q", id)
		}
	}
	if len(config.SecretStore.References) != len(requiredReferences) {
		return fmt.Errorf("GitHub mutation runtime secret reference set does not exactly match estate intent")
	}
	missing := make([]string, 0)
	for reference := range requiredReferences {
		if _, found := config.SecretStore.References[reference]; !found {
			missing = append(missing, reference)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("GitHub mutation runtime is missing required secret references")
	}
	return nil
}

func ValidateSeparation(
	readConfig githubruntime.Config,
	mutationConfig Config,
	desired estate.Config,
) error {
	readByInstallation := readConfig.GitHub.Installations
	for _, capability := range desired.Mutations {
		mutationRuntime, mutationFound := mutationConfig.GitHub.Capabilities[capability.Mutation.ID]
		readRuntime, readFound := readByInstallation[capability.Mutation.Installation]
		if !mutationFound || !readFound {
			return fmt.Errorf("read and mutation runtime bindings are incomplete")
		}
		if mutationRuntime.AppID == readRuntime.AppID ||
			mutationRuntime.ProviderInstallationID == readRuntime.ProviderInstallationID {
			return fmt.Errorf("read and mutation App identities must be distinct")
		}
		mutationLocator := secretLocator(
			mutationConfig.SecretStore, capability.Credentials.SecretRef,
		)
		for readReference := range readConfig.SecretStore.References {
			if mutationLocator == secretLocator(readConfig.SecretStore, readReference) {
				return fmt.Errorf("read and mutation App secret locators must be distinct")
			}
		}
	}
	return nil
}

func secretLocator(config githubruntime.SecretStoreConfig, reference string) string {
	return config.Provider + "\x00" + config.Service + "\x00" + config.Attribute +
		"\x00" + config.References[reference]
}

// ValidateCLISeparation enforces the gh CLI separation contract. Because one
// personal access token is a single principal, ADR 0023's distinct-App-identity
// rule cannot apply; instead separation is structural: the read and mutation
// runtimes must both use the gh CLI provider, every mutation capability must
// target a read installation declared by the estate, and the mutation secret
// reference set must still match the estate capability contract exactly.
func ValidateCLISeparation(
	readConfig githubruntime.Config,
	mutationConfig Config,
	desired estate.Config,
) error {
	if readConfig.SecretStore.Provider != "gh-cli" {
		return fmt.Errorf("gh CLI mutation runtime requires a gh CLI read runtime")
	}
	readByInstallation := readConfig.GitHub.Installations
	for _, capability := range desired.Mutations {
		if _, mutationFound := mutationConfig.GitHub.Capabilities[capability.Mutation.ID]; !mutationFound {
			return fmt.Errorf("read and mutation runtime bindings are incomplete")
		}
		if _, readFound := readByInstallation[capability.Mutation.Installation]; !readFound {
			return fmt.Errorf(
				"mutation capability %q targets installation %q that is not declared for reads",
				capability.Mutation.ID, capability.Mutation.Installation,
			)
		}
	}
	return nil
}
