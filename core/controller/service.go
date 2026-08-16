package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

// errShutdownTimeout signals that the controller's background loops did not
// finish draining within ShutdownTimeout during shutdown. The standalone
// controller process (core/cmd/gds-controller) turns any Run error into a
// non-zero exit so the operator can observe that the previous owner could not
// prove a clean release of its SQLite lifetime authority; an embedded Service
// caller can match this with errors.Is.
var errShutdownTimeout = errors.New("controller background shutdown exceeded shutdown timeout")

type WebhookWorker interface {
	RunOnce(context.Context) (WorkerResult, error)
}

type ScheduledReconciler interface {
	Run(context.Context) (ReconciliationRunResult, error)
}

type ScheduledBackup interface {
	Run(context.Context) (state.BackupInfo, error)
}

type TelemetryFlusher interface{ Flush(context.Context) error }

type Service struct {
	Store             *state.Store
	Webhook           http.Handler
	Worker            WebhookWorker
	Reconciler        ScheduledReconciler
	Backup            ScheduledBackup
	Telemetry         TelemetryFlusher
	WebhookPath       string
	WebhookPoll       time.Duration
	ReconcileInterval time.Duration
	BackupInterval    time.Duration
	ShutdownTimeout   time.Duration
	Metrics           *Metrics
	Logger            *slog.Logger
	ready             atomic.Bool
	workerHealthy     atomic.Bool
	reconcilerHealthy atomic.Bool
	backupHealthy     atomic.Bool
}

func (service *Service) Validate() error {
	if service == nil || service.Store == nil || service.Webhook == nil ||
		service.Worker == nil || service.Reconciler == nil || service.Backup == nil ||
		service.WebhookPath != "/github/webhook" || service.WebhookPoll <= 0 ||
		service.ReconcileInterval <= 0 || service.BackupInterval <= 0 ||
		service.ShutdownTimeout <= 0 {
		return fmt.Errorf("controller service dependencies or intervals are invalid")
	}
	if service.Metrics == nil {
		service.Metrics = &Metrics{}
	}
	if service.Logger == nil {
		service.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return nil
}

func (service *Service) Handler() (http.Handler, error) {
	if err := service.Validate(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle(service.WebhookPath, service.instrumentWebhook(service.Webhook))
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSONStatus(writer, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := http.StatusServiceUnavailable
		stateValue := "not-ready"
		_, storeErr := service.Store.Summary(request.Context())
		if service.ready.Load() && service.workerHealthy.Load() &&
			service.reconcilerHealthy.Load() && service.backupHealthy.Load() &&
			storeErr == nil {
			status, stateValue = http.StatusOK, "ready"
		}
		writeJSONStatus(writer, status, map[string]any{"status": stateValue})
	})
	mux.HandleFunc("/metrics", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		queue, queueErr := service.Store.WebhookQueueSummary(request.Context())
		summary, summaryErr := service.Store.Summary(request.Context())
		if queueErr != nil || summaryErr != nil {
			http.Error(writer, "metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
		writer.Header().Set("Cache-Control", "no-store")
		_, _ = writer.Write(service.Metrics.PrometheusWithState(queue, summary))
	})
	return mux, nil
}

func (service *Service) Run(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return fmt.Errorf("controller listener is required")
	}
	handler, err := service.Handler()
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 << 10, ErrorLog: log.New(io.Discard, "", 0),
	}
	service.ready.Store(true)
	service.Logger.Info("controller-started", "listen", listener.Addr().String())
	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var background sync.WaitGroup
	background.Add(3)
	go func() {
		defer background.Done()
		service.workerLoop(runContext)
	}()
	if service.Telemetry != nil {
		background.Add(1)
		go func() {
			defer background.Done()
			service.telemetryLoop(runContext)
		}()
	}
	go func() {
		defer background.Done()
		service.reconciliationLoop(runContext)
	}()
	go func() {
		defer background.Done()
		service.backupLoop(runContext)
	}()
	serveError := make(chan error, 1)
	go func() {
		serveError <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		service.ready.Store(false)
		cancelRun()
		deadline := time.Now().Add(service.ShutdownTimeout)
		shutdownContext, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownContext)
		drainErr := service.awaitBackground(&background, deadline)
		if shutdownErr != nil {
			return fmt.Errorf("shutdown controller HTTP service: %w", errors.Join(shutdownErr, drainErr))
		}
		if drainErr != nil {
			return fmt.Errorf("drain controller background workers: %w", drainErr)
		}
		service.Logger.Info("controller-stopped")
		return nil
	case err := <-serveError:
		service.ready.Store(false)
		cancelRun()
		drainErr := service.awaitBackground(&background, time.Now().Add(service.ShutdownTimeout))
		if err == http.ErrServerClosed {
			if drainErr != nil {
				return fmt.Errorf("drain controller background workers: %w", drainErr)
			}
			return nil
		}
		return fmt.Errorf("serve controller HTTP: %w", errors.Join(err, drainErr))
	}
}

// awaitBackground waits for the controller's background loops to finish within
// the shutdown budget. The single ShutdownTimeout budget bounds the whole
// shutdown: in the cancellation path the HTTP drain and this wait share one
// deadline, so a background loop that ignores cancellation cannot hold the
// process — and its SQLite lifetime authority — open indefinitely. It returns
// errShutdownTimeout when the budget is exhausted before the loops return, and
// prefers a completed drain when both the deadline and completion race.
//
// On timeout, the waiter goroutine leaks until background.Wait completes. The
// caller's context cancellation forces the three loops to exit, which drains
// the WaitGroup and releases the goroutine. The process-level guarantee is
// that Run turns errShutdownTimeout into a non-zero exit, so the lifetime
// authority is force-released even if a loop ignores cancellation.
func (service *Service) awaitBackground(background *sync.WaitGroup, deadline time.Time) error {
	done := make(chan struct{})
	go func() {
		background.Wait()
		close(done)
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		select {
		case <-done:
			return nil
		default:
			return errShutdownTimeout
		}
	}
}

func (service *Service) workerLoop(ctx context.Context) {
	ticker := time.NewTicker(service.WebhookPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.runWorkerBatch(ctx)
		}
	}
}

func (service *Service) telemetryLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := service.Telemetry.Flush(ctx); err != nil {
				// Telemetry is a sink, never the recovery authority or readiness gate.
				service.Logger.Warn("telemetry-export-failed", "error_class", fmt.Sprintf("%T", err))
			}
		}
	}
}

func (service *Service) runWorkerBatch(ctx context.Context) {
	for range 16 {
		result, err := service.Worker.RunOnce(ctx)
		if err != nil {
			service.workerHealthy.Store(false)
			service.Metrics.WorkerFailed.Add(1)
			service.Logger.Warn("webhook-processing-failed", "error_type", fmt.Sprintf("%T", err))
			return
		}
		service.workerHealthy.Store(true)
		if !result.Processed {
			return
		}
		switch result.Status {
		case "succeeded":
			service.Metrics.WorkerSucceeded.Add(1)
		case "dead-letter":
			service.Metrics.WorkerDeadLetter.Add(1)
		default:
			service.Metrics.WorkerFailed.Add(1)
		}
		service.Logger.Info(
			"webhook-processed", "delivery_id", result.DeliveryID,
			"status", result.Status, "attempt", result.Attempt,
		)
	}
}

func (service *Service) reconciliationLoop(ctx context.Context) {
	service.runReconciliation(ctx)
	ticker := time.NewTicker(service.ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.runReconciliation(ctx)
		}
	}
}

func (service *Service) runReconciliation(ctx context.Context) {
	run, err := service.Reconciler.Run(ctx)
	if err != nil {
		service.reconcilerHealthy.Store(false)
		service.Metrics.ReconciliationFailed.Add(1)
		service.Logger.Warn("reconciliation-failed", "error_type", fmt.Sprintf("%T", err))
		return
	}
	service.reconcilerHealthy.Store(true)
	switch run.Status {
	case "succeeded":
		service.Metrics.ReconciliationSucceeded.Add(1)
	case "partial":
		service.Metrics.ReconciliationPartial.Add(1)
	default:
		service.Metrics.ReconciliationFailed.Add(1)
	}
	service.Metrics.LastReconciliationUnix.Store(time.Now().UTC().Unix())
	service.Logger.Info(
		"reconciliation-finished", "reconciliation_id", run.ReconciliationID,
		"status", run.Status,
	)
}

func (service *Service) backupLoop(ctx context.Context) {
	service.runBackup(ctx)
	ticker := time.NewTicker(service.BackupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.runBackup(ctx)
		}
	}
}

func (service *Service) runBackup(ctx context.Context) {
	backup, err := service.Backup.Run(ctx)
	if err != nil {
		service.backupHealthy.Store(false)
		service.Metrics.BackupFailed.Add(1)
		service.Logger.Warn("backup-failed", "error_type", fmt.Sprintf("%T", err))
		return
	}
	service.backupHealthy.Store(true)
	service.Metrics.BackupSucceeded.Add(1)
	service.Metrics.LastBackupUnix.Store(time.Now().UTC().Unix())
	service.Logger.Info(
		"backup-finished",
		"size", backup.Size,
		"digest", backup.Digest,
		"schema_version", backup.SchemaVersion,
		"logical_digest", backup.LogicalDigest,
		"integrity", backup.Integrity,
	)
}

func (service *Service) instrumentWebhook(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		service.Metrics.WebhookRequests.Add(1)
		statusWriter := &statusResponseWriter{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(statusWriter, request)
		if statusWriter.status == http.StatusAccepted {
			service.Metrics.WebhookAccepted.Add(1)
		} else {
			service.Metrics.WebhookRejected.Add(1)
		}
	})
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusResponseWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func writeJSONStatus(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
