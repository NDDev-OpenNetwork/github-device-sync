package controllerruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestLoadBindsPrivateControllerGitHubAndEstateConfiguration(t *testing.T) {
	root := controllerTestRoot(t)
	directory := t.TempDir()
	githubPath := filepath.Join(directory, "github-runtime.yaml")
	githubDocument := `schema_version: 1
github:
  installations:
    "installation:github-organization":
      app_id: "123456"
      provider_installation_id: "900001"
    "installation:github-personal":
      app_id: "123457"
      provider_installation_id: "900002"
    "installation:github-example-media":
      app_id: "123458"
      provider_installation_id: "900003"
    "installation:github-guild":
      app_id: "123459"
      provider_installation_id: "900004"
    "installation:github-opennetwork":
      app_id: "123460"
      provider_installation_id: "900005"
  max_repositories: 2000
secret_store:
  provider: "environment"
  references:
    "secret:gds/github-app/organization": "GDS_GITHUB_APP_ORGANIZATION_KEY"
    "secret:gds/github-app/personal": "GDS_GITHUB_APP_PERSONAL_KEY"
    "secret:gds/github-app/example-media": "GDS_GITHUB_APP_EXAMPLE_MEDIA_KEY"
    "secret:gds/github-app/guild": "GDS_GITHUB_APP_GUILD_KEY"
    "secret:gds/github-app/opennetwork": "GDS_GITHUB_APP_OPENNETWORK_KEY"
    "secret:gds/github/webhook": "GDS_GITHUB_WEBHOOK_SECRET"
    "secret:gds/controller/audit-signing-key": "GDS_AUDIT_SIGNING_KEY"
webhook:
  enabled: true
  secret_ref: "secret:gds/github/webhook"
`
	if err := os.WriteFile(githubPath, []byte(githubDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, "state.db")
	backupPath := filepath.Join(directory, "backups")
	controllerPath := filepath.Join(directory, "controller-runtime.yaml")
	controllerDocument := fmt.Sprintf(`schema_version: 1
controller:
  estate_root: %q
  github_runtime: %q
  state_path: %q
  listen: "127.0.0.1:8787"
  webhook_path: "/github/webhook"
  schedule:
    full_reconcile_seconds: 3600
    webhook_poll_milliseconds: 500
    max_webhook_attempts: 5
    webhook_processing_timeout_seconds: 3600
    shutdown_timeout_seconds: 15
  backup:
    directory: %q
    interval_seconds: 86400
    retain: 14
  retention:
    terminal_webhook_days: 14
    reconciliation_days: 400
  audit:
    directory: %q
    signing_key_ref: "secret:gds/controller/audit-signing-key"
    public_key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
    retain_days: 400
`, root, githubPath, statePath, backupPath, filepath.Join(directory, "audit"))
	if err := os.WriteFile(controllerPath, []byte(controllerDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(controllerPath, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Controller.StatePath != statePath ||
		loaded.Config.Controller.Schedule.WebhookProcessingTimeoutSeconds != 3600 ||
		!loaded.GitHub.Webhook.Enabled || len(loaded.Estate.Installations) != 5 {
		t.Fatalf("loaded=%+v", loaded)
	}
	if err := os.Chmod(controllerPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(controllerPath, schemas); err == nil {
		t.Fatal("world-readable controller runtime was accepted")
	}
}

func TestValidateRejectsPublicListener(t *testing.T) {
	config := Config{Controller: Controller{
		EstateRoot: "/estate", GitHubRuntime: "/runtime/github.yaml",
		StatePath: "/state/state.db", Listen: "0.0.0.0:8787",
		Backup: Backup{Directory: "/state/backups"},
	}}
	if err := validate(config); err == nil {
		t.Fatal("public listener was accepted")
	}
}

func controllerTestRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}
