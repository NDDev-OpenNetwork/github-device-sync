// Package controllerruntime loads the private single-controller deployment
// contract and binds it to canonical estate and GitHub runtime intent.
package controllerruntime

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/privateconfig"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type Config struct {
	SchemaVersion int        `json:"schema_version"`
	Controller    Controller `json:"controller"`
}

type Controller struct {
	EstateRoot    string    `json:"estate_root"`
	GitHubRuntime string    `json:"github_runtime"`
	StatePath     string    `json:"state_path"`
	Listen        string    `json:"listen"`
	WebhookPath   string    `json:"webhook_path"`
	Schedule      Schedule  `json:"schedule"`
	Backup        Backup    `json:"backup"`
	Retention     Retention `json:"retention"`
	Audit         Audit     `json:"audit"`
}

type Schedule struct {
	FullReconcileSeconds            int `json:"full_reconcile_seconds"`
	WebhookPollMilliseconds         int `json:"webhook_poll_milliseconds"`
	MaxWebhookAttempts              int `json:"max_webhook_attempts"`
	WebhookProcessingTimeoutSeconds int `json:"webhook_processing_timeout_seconds"`
	ShutdownTimeoutSeconds          int `json:"shutdown_timeout_seconds"`
}

type Backup struct {
	Directory       string `json:"directory"`
	IntervalSeconds int    `json:"interval_seconds"`
	Retain          int    `json:"retain"`
}

type Retention struct {
	TerminalWebhookDays int `json:"terminal_webhook_days"`
	ReconciliationDays  int `json:"reconciliation_days"`
}

type Audit struct {
	Directory     string `json:"directory"`
	SigningKeyRef string `json:"signing_key_ref"`
	PublicKey     string `json:"public_key"`
	RetainDays    int    `json:"retain_days"`
}

type Runtime struct {
	Config Config
	Estate estate.Config
	GitHub githubruntime.Config
}

func Load(path string, schemas *validation.Set) (Runtime, error) {
	absolute, raw, err := privateconfig.Read(path)
	if err != nil {
		return Runtime{}, err
	}
	value, err := serialization.Decode(absolute, raw)
	if err != nil {
		return Runtime{}, err
	}
	if findings := schemas.Validate("controller-runtime", value, absolute); len(findings) != 0 {
		return Runtime{}, fmt.Errorf("controller runtime schema validation failed with %s", findings[0].Code)
	}
	var config Config
	if err := serialization.DecodeInto(absolute, raw, &config); err != nil {
		return Runtime{}, err
	}
	if err := validate(config); err != nil {
		return Runtime{}, err
	}
	desired, findings := estate.Load(config.Controller.EstateRoot, schemas)
	if len(findings) != 0 {
		return Runtime{}, fmt.Errorf("controller estate validation failed with %s", findings[0].Code)
	}
	githubConfig, err := githubruntime.LoadWithRequiredSecretReferences(
		config.Controller.GitHubRuntime, desired, schemas,
		[]string{config.Controller.Audit.SigningKeyRef},
	)
	if err != nil {
		return Runtime{}, err
	}
	if !githubConfig.Webhook.Enabled || githubConfig.Webhook.SecretRef == "" {
		return Runtime{}, fmt.Errorf("controller requires an enabled GitHub webhook secret reference")
	}
	return Runtime{Config: config, Estate: desired, GitHub: githubConfig}, nil
}

func validate(config Config) error {
	address := config.Controller.Listen
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("controller listen address is invalid")
	}
	ip := net.ParseIP(host)
	port, portErr := strconv.Atoi(portText)
	if ip == nil || !ip.IsLoopback() || portErr != nil || port < 1 || port > 65535 {
		return fmt.Errorf("controller must listen on an explicit loopback IP and valid port")
	}
	for _, path := range []string{
		config.Controller.EstateRoot, config.Controller.GitHubRuntime,
		config.Controller.StatePath, config.Controller.Backup.Directory,
		config.Controller.Audit.Directory,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("controller runtime paths must be clean and absolute")
		}
	}
	if config.Controller.StatePath == config.Controller.Backup.Directory ||
		filepath.Dir(config.Controller.StatePath) == config.Controller.StatePath {
		return fmt.Errorf("controller state and backup paths are inconsistent")
	}
	return nil
}
