// Package githubmutationruntime binds a separate write-capable GitHub App to
// canonical mutation capabilities without exposing it to the read controller.
package githubmutationruntime

import (
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
)

type ErrorKind string

const (
	ErrorUnavailable    ErrorKind = "unavailable"
	ErrorSecurity       ErrorKind = "security"
	ErrorInvalid        ErrorKind = "invalid"
	ErrorEstateMismatch ErrorKind = "estate-mismatch"
)

type Error struct {
	Kind  ErrorKind
	Cause error
}

func (runtimeError *Error) Error() string {
	if runtimeError.Cause == nil {
		return fmt.Sprintf("GitHub mutation runtime configuration failed (%s)", runtimeError.Kind)
	}
	return fmt.Sprintf(
		"GitHub mutation runtime configuration failed (%s): %v",
		runtimeError.Kind, runtimeError.Cause,
	)
}

func (runtimeError *Error) Unwrap() error { return runtimeError.Cause }

type Config struct {
	SchemaVersion int                             `json:"schema_version"`
	GitHub        GitHubConfig                    `json:"github"`
	SecretStore   githubruntime.SecretStoreConfig `json:"secret_store"`
}

type GitHubConfig struct {
	Capabilities             map[string]Capability `json:"capabilities"`
	MaxRepositories          int                   `json:"max_repositories"`
	MinimumMutationSpacingMS int                   `json:"minimum_mutation_spacing_ms"`
}

type Capability struct {
	AppID                  string `json:"app_id"`
	ProviderInstallationID string `json:"provider_installation_id"`
	// AccountLogin and AccountType bind the gh CLI variant of a mutation
	// capability, mirroring the read runtime Installation.
	AccountLogin string `json:"account_login,omitempty"`
	AccountType  string `json:"account_type,omitempty"`
}
