package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/audit"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/controller"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/controllerruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/telemetry"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/webhooks"
)

var version = "0.4.0-dev"

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("gds-controller", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runtimePath := flags.String("runtime-config", "", "private controller runtime YAML/JSON")
	showVersion := flags.Bool("version", false, "print controller version")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	}
	if *runtimePath == "" {
		_, _ = fmt.Fprintln(stderr, "gds-controller: --runtime-config is required")
		return 2
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		return startupError(stderr, "schema", err)
	}
	runtime, err := controllerruntime.Load(*runtimePath, schemas)
	if err != nil {
		return startupError(stderr, "runtime", err)
	}
	store, err := openState(ctx, runtime.Config.Controller.StatePath)
	if err != nil {
		return startupError(stderr, "state", err)
	}
	defer store.Close()
	readers, err := githubruntime.BuildReaders(
		runtime.GitHub, runtime.Estate, githubruntime.BuildOptions{},
	)
	if err != nil {
		return startupError(stderr, "github", err)
	}
	secretStore, err := githubruntime.BuildSecretStore(runtime.GitHub.SecretStore)
	if err != nil {
		return startupError(stderr, "secret-store", err)
	}
	reconciliationReaders := make(map[string]reconciler.InstallationReader, len(readers))
	repositoryReaders := make(map[string]controller.RepositoryReader, len(readers))
	for id, reader := range readers {
		reconciliationReaders[id] = reader
		repositoryReaders[id] = reader
	}
	auditRecorder := &audit.Recorder{
		Store: secretStore, Reference: runtime.Config.Controller.Audit.SigningKeyRef,
		ExpectedPublicKey: runtime.Config.Controller.Audit.PublicKey,
		Directory:         runtime.Config.Controller.Audit.Directory,
		RetentionAge:      time.Duration(runtime.Config.Controller.Audit.RetainDays) * 24 * time.Hour,
		Schemas:           schemas,
	}
	if err := auditRecorder.Prepare(); err != nil {
		return startupError(stderr, "audit", err)
	}
	runner := &controller.ReconciliationRunner{
		Store: store, Config: runtime.Estate, Readers: reconciliationReaders,
		Concurrency:     runtime.Estate.Root.Rollout.MaxParallelObservation,
		MaxRepositories: runtime.GitHub.GitHub.MaxRepositories,
		Now:             time.Now,
		Audit:           auditRecorder,
	}
	processor, err := controller.NewRepositoryProcessor(
		store, runtime.Estate, runtime.GitHub, repositoryReaders, runner,
	)
	if err != nil {
		return startupError(stderr, "processor", err)
	}
	receiver := &webhooks.Receiver{
		Store: store,
		Secrets: controller.WebhookSecretSource{
			Store: secretStore, Reference: runtime.GitHub.Webhook.SecretRef,
		},
		Events: webhooks.DefaultEventRules(),
	}
	worker := &controller.Worker{
		Store: store, Processor: processor,
		MaxAttempts: runtime.Config.Controller.Schedule.MaxWebhookAttempts,
		ProcessingTimeout: time.Duration(
			runtime.Config.Controller.Schedule.WebhookProcessingTimeoutSeconds,
		) * time.Second,
	}
	backups := &controller.BackupManager{
		Store: store, Directory: runtime.Config.Controller.Backup.Directory,
		Retain: runtime.Config.Controller.Backup.Retain,
		Retention: state.RetentionPolicy{
			TerminalWebhookAge: time.Duration(
				runtime.Config.Controller.Retention.TerminalWebhookDays,
			) * 24 * time.Hour,
			ReconciliationAge: time.Duration(
				runtime.Config.Controller.Retention.ReconciliationDays,
			) * 24 * time.Hour,
		},
	}
	if err := backups.Prepare(); err != nil {
		return startupError(stderr, "backup", err)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	telemetryExporter, err := telemetry.FromEnvironment(store)
	if err != nil {
		return startupError(stderr, "telemetry", err)
	}
	service := &controller.Service{
		Store: store, Webhook: receiver, Worker: worker, Reconciler: runner, Backup: backups,
		Telemetry:   telemetryExporter,
		WebhookPath: runtime.Config.Controller.WebhookPath,
		WebhookPoll: time.Duration(
			runtime.Config.Controller.Schedule.WebhookPollMilliseconds,
		) * time.Millisecond,
		ReconcileInterval: time.Duration(
			runtime.Config.Controller.Schedule.FullReconcileSeconds,
		) * time.Second,
		BackupInterval: time.Duration(
			runtime.Config.Controller.Backup.IntervalSeconds,
		) * time.Second,
		ShutdownTimeout: time.Duration(
			runtime.Config.Controller.Schedule.ShutdownTimeoutSeconds,
		) * time.Second,
		Metrics: &controller.Metrics{}, Logger: logger,
	}
	listener, err := net.Listen("tcp", runtime.Config.Controller.Listen)
	if err != nil {
		return startupError(stderr, "listener", err)
	}
	if err := service.Run(ctx, listener); err != nil {
		return startupError(stderr, "service", err)
	}
	return 0
}

func openState(ctx context.Context, path string) (*state.Store, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return state.Open(ctx, path)
	case os.IsNotExist(err):
		return state.Initialize(ctx, path)
	default:
		return nil, err
	}
}

func startupError(writer io.Writer, phase string, err error) int {
	_, _ = fmt.Fprintf(writer, "gds-controller: %s startup failed (%T)\n", phase, err)
	return 1
}
