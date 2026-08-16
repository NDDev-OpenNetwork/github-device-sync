// Package githubruntime binds private estate intent to device-local GitHub App
// identities and explicit secret-manager adapters.
package githubruntime

import "fmt"

type ErrorKind string

const (
	ErrorUnavailable    ErrorKind = "unavailable"
	ErrorSecurity       ErrorKind = "security"
	ErrorInvalid        ErrorKind = "invalid"
	ErrorEstateMismatch ErrorKind = "estate-mismatch"
	ErrorUnsupported    ErrorKind = "unsupported"
)

type Error struct {
	Kind  ErrorKind
	Cause error
}

func (runtimeError *Error) Error() string {
	if runtimeError.Cause == nil {
		return fmt.Sprintf("GitHub runtime configuration failed (%s)", runtimeError.Kind)
	}
	return fmt.Sprintf(
		"GitHub runtime configuration failed (%s): %v", runtimeError.Kind, runtimeError.Cause,
	)
}

func (runtimeError *Error) Unwrap() error { return runtimeError.Cause }

type Config struct {
	SchemaVersion int               `json:"schema_version"`
	GitHub        GitHubConfig      `json:"github"`
	SecretStore   SecretStoreConfig `json:"secret_store"`
	Webhook       WebhookConfig     `json:"webhook,omitempty"`
}

type GitHubConfig struct {
	Installations   map[string]Installation `json:"installations"`
	MaxRepositories int                     `json:"max_repositories"`
}

type Installation struct {
	AppID                  string `json:"app_id"`
	ProviderInstallationID string `json:"provider_installation_id"`
	// AccountLogin and AccountType bind the gh CLI variant of an installation:
	// a personal access token is identified by the GitHub account it serves,
	// not by a GitHub App id. The schema's runtimeInstallation oneOf admits
	// either the App pair or the gh CLI account pair.
	AccountLogin string `json:"account_login,omitempty"`
	AccountType  string `json:"account_type,omitempty"`
}

type SecretStoreConfig struct {
	Provider   string            `json:"provider"`
	Service    string            `json:"service,omitempty"`
	Attribute  string            `json:"attribute,omitempty"`
	References map[string]string `json:"references"`
}

type WebhookConfig struct {
	Enabled   bool   `json:"enabled"`
	SecretRef string `json:"secret_ref,omitempty"`
}
