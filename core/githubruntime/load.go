package githubruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/privateconfig"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// ConfigFileName is the canonical device-local runtime configuration filename.
const ConfigFileName = "github-runtime.yaml"

// DefaultPath resolves the canonical device-local GitHub runtime configuration
// path under the XDG config home, mirroring state.DefaultPath and
// estateregistry.DefaultPath. It returns
// "$XDG_CONFIG_HOME/github-device-sync/github-runtime.yaml" (or
// "$HOME/.config/github-device-sync/github-runtime.yaml" as a fallback) so the
// GitHub read commands can default to it when --runtime-config is omitted.
func DefaultPath() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for GDS runtime config: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	} else if !filepath.IsAbs(configHome) {
		return "", fmt.Errorf("XDG_CONFIG_HOME must be an absolute path")
	}
	return filepath.Join(configHome, "github-device-sync", ConfigFileName), nil
}

func Load(path string, desired estate.Config, schemas *validation.Set) (Config, error) {
	return LoadWithRequiredSecretReferences(path, desired, schemas, nil)
}

func LoadWithRequiredSecretReferences(
	path string,
	desired estate.Config,
	schemas *validation.Set,
	requiredReferences []string,
) (Config, error) {
	// One canonical boundary for the documented default. Call sites that omit
	// --runtime-config must resolve the same path as those that default it
	// themselves, so the default cannot depend on which command was invoked.
	if path == "" {
		defaulted, err := DefaultPath()
		if err != nil {
			return Config{}, &Error{Kind: ErrorUnavailable, Cause: err}
		}
		path = defaulted
	}
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
	if findings := schemas.Validate("github-runtime", value, absolute); len(findings) != 0 {
		return Config{}, &Error{
			Kind:  ErrorInvalid,
			Cause: fmt.Errorf("schema validation failed with %s", findings[0].Code),
		}
	}
	var config Config
	if err := serialization.DecodeInto(absolute, raw, &config); err != nil {
		return Config{}, &Error{Kind: ErrorInvalid, Cause: err}
	}
	if err := validateAgainstEstate(config, desired, requiredReferences); err != nil {
		return Config{}, &Error{Kind: ErrorEstateMismatch, Cause: err}
	}
	return config, nil
}

func validateAgainstEstate(
	config Config,
	desired estate.Config,
	additionalReferences []string,
) error {
	desiredInstallations := make(map[string]estate.Installation, len(desired.Installations))
	requiredReferences := make(map[string]struct{}, len(desired.Installations))
	for _, installation := range desired.Installations {
		id := installation.Installation.ID
		desiredInstallations[id] = installation
		requiredReferences[installation.Credentials.SecretRef] = struct{}{}
	}
	if config.Webhook.Enabled {
		requiredReferences[config.Webhook.SecretRef] = struct{}{}
	}
	for _, reference := range additionalReferences {
		if reference == "" {
			return fmt.Errorf("GitHub runtime additional secret reference is empty")
		}
		requiredReferences[reference] = struct{}{}
	}
	if len(config.GitHub.Installations) != len(desiredInstallations) {
		return fmt.Errorf("GitHub runtime installation set does not exactly match estate intent")
	}
	for id := range config.GitHub.Installations {
		if _, found := desiredInstallations[id]; !found {
			return fmt.Errorf("GitHub runtime contains unknown installation %q", id)
		}
	}
	if len(config.SecretStore.References) != len(requiredReferences) {
		return fmt.Errorf("GitHub runtime secret reference set does not exactly match estate intent")
	}
	missing := make([]string, 0)
	for reference := range requiredReferences {
		if _, found := config.SecretStore.References[reference]; !found {
			missing = append(missing, reference)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("GitHub runtime is missing required secret references")
	}
	return nil
}
