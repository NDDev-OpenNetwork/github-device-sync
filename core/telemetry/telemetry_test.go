package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

func TestExporterOutageLeavesDurableBoundedOutboxAndSecretsFailClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "offline", http.StatusServiceUnavailable) }))
	defer server.Close()
	exporter := Exporter{Store: store, Client: server.Client(), Now: func() time.Time { return now }, Config: Config{Endpoint: server.URL, MaxPending: 2, MaxBytes: 4096, MaximumAttempts: 3, BatchSize: 10}}
	event := Event{EventID: "event-1", SignalType: "log", Name: "operation.completed", OccurredAt: now, Attributes: map[string]any{"repository": "example-org/repo", "device_id": "device:test", "operation_id": "op:test"}}
	if err := exporter.Emit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingTelemetry(context.Background(), now.Add(2*time.Second), 10)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 1 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	event.EventID = "event-secret"
	secretKey := "authorization" + "_token"
	secretValue := "gh" + "p_abcdefghijklmnopqrstuvwxyz"
	event.Attributes = map[string]any{secretKey: secretValue}
	if err := exporter.Emit(context.Background(), event); err == nil {
		t.Fatal("secret telemetry was accepted")
	}
}

func TestEncodeOTLPUsesCanonicalSignalEnvelopes(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for signal, root := range map[string]string{"log": "resourceLogs", "metric": "resourceMetrics", "trace": "resourceSpans"} {
		raw, err := encodeOTLP(Event{EventID: "event-" + signal, SignalType: signal,
			Name: "gds.test", OccurredAt: now, Attributes: map[string]any{"device_id": "device:test"}})
		if err != nil {
			t.Fatalf("%s: %v", signal, err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope[root] == nil {
			t.Fatalf("%s envelope=%s err=%v", signal, raw, err)
		}
	}
}

func TestEnvironmentConfiguresBoundedOpenObserveOTLPExporter(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Setenv("GDS_OTLP_ENDPOINT", "https://openobserve.example/api/default")
	t.Setenv("GDS_OTLP_AUTHORIZATION", "Basic fixture-credential")
	exporter, err := FromEnvironment(store)
	if err != nil || exporter == nil || exporter.Config.MaxPending != 10_000 || exporter.Config.Headers["Authorization"] == "" {
		t.Fatalf("exporter=%#v err=%v", exporter, err)
	}
	t.Setenv("GDS_OTLP_ENDPOINT", "https://user:password@openobserve.example")
	if _, err := FromEnvironment(store); err == nil {
		t.Fatal("OTLP endpoint userinfo was accepted")
	}
}

func TestTransportPolicyKeepsCredentialsOffPlaintextAndRedirects(t *testing.T) {
	credential := "Basic " + "fixture-credential"
	for _, testCase := range []struct {
		name     string
		endpoint string
		headers  map[string]string
		accepted bool
	}{
		{"remote plaintext with credential", "http://openobserve.example/api/default", map[string]string{"Authorization": credential}, false},
		{"remote plaintext with lowercase credential", "http://openobserve.example", map[string]string{"authorization": credential}, false},
		{"remote plaintext with api key", "http://openobserve.example", map[string]string{"X-Api-Key": credential}, false},
		{"remote plaintext without credential", "http://openobserve.example", nil, false},
		{"loopback plaintext with credential", "http://127.0.0.1:4318", map[string]string{"Authorization": credential}, false},
		{"loopback plaintext without credential", "http://127.0.0.1:4318", nil, true},
		{"localhost plaintext without credential", "http://localhost:4318", nil, true},
		{"ipv6 loopback plaintext without credential", "http://[::1]:4318", nil, true},
		{"https with credential", "https://openobserve.example/api/default", map[string]string{"Authorization": credential}, true},
		{"userinfo", "https://user:password@openobserve.example", nil, false},
		{"unsupported scheme", "ftp://openobserve.example", nil, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateTransport(Config{Endpoint: testCase.endpoint, Headers: testCase.headers})
			if testCase.accepted != (err == nil) {
				t.Fatalf("accepted=%v err=%v", testCase.accepted, err)
			}
			if err != nil && (strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), "password")) {
				t.Fatalf("transport error disclosed a credential: %v", err)
			}
		})
	}
}

func TestFlushRejectsPlaintextCredentialWithoutSendingIt(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var received int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	// A directly constructed exporter must not be able to route a credential over
	// plaintext by skipping FromEnvironment.
	exporter := Exporter{Store: store, Client: server.Client(), Now: func() time.Time { return now },
		Config: Config{Endpoint: "http://openobserve.example", Headers: map[string]string{"Authorization": "Basic fixture"},
			MaxPending: 8, MaxBytes: 4096, MaximumAttempts: 3, BatchSize: 10}}
	event := Event{EventID: "event-1", SignalType: "log", Name: "operation.completed", OccurredAt: now,
		Attributes: map[string]any{"repository": "example-org/repo"}}
	if err := exporter.Emit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := exporter.Flush(context.Background()); err == nil {
		t.Fatal("flush accepted a plaintext credential endpoint")
	}
	if received != 0 {
		t.Fatalf("flush contacted the collector %d times before enforcing transport policy", received)
	}
	// The outbox stays durable: the event is still pending and was not consumed.
	pending, err := store.PendingTelemetry(context.Background(), now, 10)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 0 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
}

func TestFlushRefusesRedirectsAndKeepsTheEventPending(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var destinationHits int
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationHits++
		if r.Header.Get("Authorization") != "" {
			t.Error("credential followed a redirect to another origin")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/v1/logs", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	// Loopback plaintext without a credential is the one accepted plaintext case,
	// so this isolates redirect behaviour from the scheme policy.
	exporter := Exporter{Store: store, Client: redirector.Client(), Now: func() time.Time { return now },
		Config: Config{Endpoint: redirector.URL, MaxPending: 8, MaxBytes: 4096, MaximumAttempts: 3, BatchSize: 10}}
	event := Event{EventID: "event-1", SignalType: "log", Name: "operation.completed", OccurredAt: now,
		Attributes: map[string]any{"repository": "example-org/repo"}}
	if err := exporter.Emit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := exporter.Flush(context.Background()); err != nil {
		t.Fatalf("flush should record a retry, not fail: %v", err)
	}
	if destinationHits != 0 {
		t.Fatalf("redirect was followed %d times", destinationHits)
	}
	pending, err := store.PendingTelemetry(context.Background(), now.Add(2*time.Second), 10)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 1 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
}

func TestOutboxCapacityDropsOldestWithDurableAccounting(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	exporter := Exporter{Store: store, Config: Config{MaxPending: 2, MaxBytes: 32 << 10}}
	for index := 0; index < 3; index++ {
		if err := exporter.Emit(context.Background(), Event{EventID: fmt.Sprintf("event-%d", index), SignalType: "log",
			Name: "capacity.test", OccurredAt: now.Add(time.Duration(index) * time.Second), Attributes: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := store.Summary(context.Background())
	if err != nil || summary.TelemetryPending != 2 || summary.TelemetryDropped != 1 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
}
