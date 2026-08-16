package controller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type serviceWorker struct{ calls atomic.Int32 }

func (worker *serviceWorker) RunOnce(context.Context) (WorkerResult, error) {
	worker.calls.Add(1)
	return WorkerResult{}, nil
}

type sequenceWorker struct {
	errors []error
	calls  int
}

type outcomeWorker struct {
	results []WorkerResult
	calls   int
}

func (worker *outcomeWorker) RunOnce(context.Context) (WorkerResult, error) {
	if worker.calls >= len(worker.results) {
		return WorkerResult{}, nil
	}
	result := worker.results[worker.calls]
	worker.calls++
	return result, nil
}

func (worker *sequenceWorker) RunOnce(context.Context) (WorkerResult, error) {
	index := worker.calls
	worker.calls++
	if index < len(worker.errors) && worker.errors[index] != nil {
		return WorkerResult{}, worker.errors[index]
	}
	return WorkerResult{}, nil
}

type serviceReconciler struct{ calls atomic.Int32 }

func (reconciler *serviceReconciler) Run(context.Context) (ReconciliationRunResult, error) {
	reconciler.calls.Add(1)
	return ReconciliationRunResult{ReconciliationID: "reconciliation-test", Status: "succeeded"}, nil
}

type serviceBackup struct{ calls atomic.Int32 }

func (backup *serviceBackup) Run(context.Context) (state.BackupInfo, error) {
	backup.calls.Add(1)
	return state.BackupInfo{Size: 1, Digest: "sha256:test"}, nil
}

// blockingBackup models a background dependency that ignores cancellation and
// stays stuck until the test releases it. It never observes ctx, so the only
// way Service.Run can return is via the bounded shutdown budget.
type blockingBackup struct {
	started  chan struct{}
	release  chan struct{}
	announce sync.Once
}

func (backup *blockingBackup) Run(context.Context) (state.BackupInfo, error) {
	backup.announce.Do(func() { close(backup.started) })
	<-backup.release
	return state.BackupInfo{}, nil
}

func TestServiceHandlerExposesHealthReadinessMetricsAndInstrumentedWebhook(t *testing.T) {
	service := serviceFixture(t)
	handler, err := service.Handler()
	if err != nil {
		t.Fatal(err)
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"status":"ok"`) {
		t.Fatalf("health=%d %s", health.Code, health.Body.String())
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready=%d %s", ready.Code, ready.Body.String())
	}
	webhook := httptest.NewRecorder()
	handler.ServeHTTP(webhook, httptest.NewRequest(http.MethodPost, "/github/webhook", nil))
	if webhook.Code != http.StatusAccepted || service.Metrics.WebhookAccepted.Load() != 1 {
		t.Fatalf("webhook=%d metrics=%s", webhook.Code, service.Metrics.Prometheus())
	}
	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK ||
		!strings.Contains(metrics.Body.String(), "gds_webhook_accepted_total 1") {
		t.Fatalf("metrics=%d %s", metrics.Code, metrics.Body.String())
	}
}

func TestServiceRunStartsSchedulesAndStopsCleanly(t *testing.T) {
	service := serviceFixture(t)
	worker := service.Worker.(*serviceWorker)
	reconciler := service.Reconciler.(*serviceReconciler)
	backup := service.Backup.(*serviceBackup)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.Run(ctx, listener) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if service.ready.Load() && worker.calls.Load() > 0 &&
			reconciler.calls.Load() > 0 && backup.calls.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !service.ready.Load() || worker.calls.Load() == 0 ||
		reconciler.calls.Load() == 0 || backup.calls.Load() == 0 {
		t.Fatalf(
			"ready=%t worker=%d reconcile=%d backup=%d",
			service.ready.Load(), worker.calls.Load(), reconciler.calls.Load(), backup.calls.Load(),
		)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller service did not stop")
	}
	if service.ready.Load() {
		t.Fatal("service remained ready after shutdown")
	}
}

func TestServiceRunBoundsBackgroundShutdownOnCancellation(t *testing.T) {
	service := serviceFixture(t)
	backup := &blockingBackup{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(backup.release) })
	service.Backup = backup
	service.ShutdownTimeout = 50 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.Run(ctx, listener) }()
	select {
	case <-backup.started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup dependency did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, errShutdownTimeout) {
			t.Fatalf("expected bounded shutdown timeout, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller run did not return within the shutdown budget")
	}
	if service.ready.Load() {
		t.Fatal("service remained ready after shutdown")
	}
}

func TestServiceRunBoundsBackgroundShutdownOnServeError(t *testing.T) {
	service := serviceFixture(t)
	backup := &blockingBackup{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(backup.release) })
	service.Backup = backup
	service.ShutdownTimeout = 50 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- service.Run(t.Context(), listener) }()
	select {
	case <-backup.started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup dependency did not start")
	}
	// Closing the listener makes server.Serve return a non-ErrServerClosed
	// error, exercising the serveError shutdown branch while ctx stays live.
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, errShutdownTimeout) {
			t.Fatalf("expected bounded shutdown timeout on serve error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller run did not return within the shutdown budget")
	}
}

func TestServiceQueueFailureDegradesReadinessAndRecovers(t *testing.T) {
	service := serviceFixture(t)
	service.Worker = &sequenceWorker{errors: []error{errors.New("claim queue")}}
	service.ready.Store(true)
	service.reconcilerHealthy.Store(true)
	service.backupHealthy.Store(true)
	handler, err := service.Handler()
	if err != nil {
		t.Fatal(err)
	}

	service.runWorkerBatch(context.Background())
	if service.Metrics.WorkerFailed.Load() != 1 {
		t.Fatalf("worker failures=%d", service.Metrics.WorkerFailed.Load())
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready after queue failure=%d %s", ready.Code, ready.Body.String())
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health after queue failure=%d %s", health.Code, health.Body.String())
	}

	service.runWorkerBatch(context.Background())
	ready = httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready after queue recovery=%d %s", ready.Code, ready.Body.String())
	}
}

func TestServiceCountsDurableWorkerFailuresWithoutDegradingReadiness(t *testing.T) {
	service := serviceFixture(t)
	service.Worker = &outcomeWorker{results: []WorkerResult{
		{Processed: true, DeliveryID: "retry", Status: "failed", Attempt: 1},
		{Processed: true, DeliveryID: "permanent", Status: "dead-letter", Attempt: 1},
	}}
	service.ready.Store(true)
	service.reconcilerHealthy.Store(true)
	service.backupHealthy.Store(true)
	handler, err := service.Handler()
	if err != nil {
		t.Fatal(err)
	}

	service.runWorkerBatch(context.Background())
	if !service.workerHealthy.Load() || service.Metrics.WorkerFailed.Load() != 1 ||
		service.Metrics.WorkerDeadLetter.Load() != 1 {
		t.Fatalf(
			"healthy=%t failed=%d dead_letter=%d",
			service.workerHealthy.Load(), service.Metrics.WorkerFailed.Load(),
			service.Metrics.WorkerDeadLetter.Load(),
		)
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready after durable outcomes=%d %s", ready.Code, ready.Body.String())
	}
}

func TestBackupManagerCreatesAndPrunesVerifiedSnapshots(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	manager := BackupManager{
		Store: store, Directory: filepath.Join(root, "backups"), Retain: 2,
		Retention: state.RetentionPolicy{
			TerminalWebhookAge: 14 * 24 * time.Hour,
			ReconciliationAge:  400 * 24 * time.Hour,
		},
		Now: func() time.Time { return now },
	}
	for range 3 {
		if _, err := manager.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	entries, err := os.ReadDir(manager.Directory)
	if err != nil || len(entries) != 2 ||
		entries[0].Name() != "state-20260711T000001Z.db" ||
		entries[1].Name() != "state-20260711T000002Z.db" {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func serviceFixture(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Service{
		Store: store,
		Webhook: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusAccepted)
		}),
		Worker: &serviceWorker{}, Reconciler: &serviceReconciler{}, Backup: &serviceBackup{},
		WebhookPath: "/github/webhook", WebhookPoll: 10 * time.Millisecond,
		ReconcileInterval: time.Hour, BackupInterval: time.Hour,
		ShutdownTimeout: time.Second, Metrics: &Metrics{},
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
}
