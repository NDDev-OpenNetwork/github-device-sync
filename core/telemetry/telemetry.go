// Package telemetry provides bounded offline-first OTLP/HTTP export.
// The operation journal remains the recovery authority; this outbox is a sink.
package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

var secretValue = regexp.MustCompile(`(?i)(gh[pousr]_[a-z0-9]{20,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|password\s*=|bearer\s+[a-z0-9._-]+)`)

type Config struct {
	Endpoint        string
	Headers         map[string]string
	MaxPending      int
	MaxBytes        int64
	MaximumAttempts int
	BatchSize       int
}

type Event struct {
	EventID    string         `json:"event_id"`
	SignalType string         `json:"signal_type"`
	Name       string         `json:"name"`
	OccurredAt time.Time      `json:"occurred_at"`
	Attributes map[string]any `json:"attributes"`
}

type Exporter struct {
	Store  *state.Store
	Client *http.Client
	Config Config
	Now    func() time.Time
}

// secretBearingHeaders name request headers whose value authenticates GDS to the
// collector. Any of them turns the request into a credential the transport must
// protect, so their presence forces HTTPS and forbids following a redirect.
var secretBearingHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"x-api-key":           {},
	"api-key":             {},
	"cookie":              {},
}

// errRedirectRefused fails an export closed rather than replaying a credential to
// a destination the operator never approved. A redirect can move the request to a
// different origin, so following one would silently widen the trust boundary.
var errRedirectRefused = errors.New("OTLP export refused to follow a redirect")

func carriesSecret(headers map[string]string) bool {
	for key := range headers {
		if _, secret := secretBearingHeaders[strings.ToLower(strings.TrimSpace(key))]; secret {
			return true
		}
	}
	return false
}

func isLoopbackHost(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	address := net.ParseIP(strings.Trim(hostname, "[]"))
	return address != nil && address.IsLoopback()
}

// validateTransport is the single owner of the OTLP transport policy. It is
// enforced both when the exporter is built from the environment and on every
// flush, so constructing Config directly cannot route a credential over plaintext.
// Messages never echo the endpoint or a header value: the endpoint may embed
// userinfo and the headers are credentials.
func validateTransport(config Config) error {
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Host == "" {
		return errors.New("GDS_OTLP_ENDPOINT must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil {
		return errors.New("GDS_OTLP_ENDPOINT must not embed userinfo")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if carriesSecret(config.Headers) {
			return errors.New("GDS_OTLP_ENDPOINT must use HTTPS while a credential header is configured")
		}
		if !isLoopbackHost(parsed.Hostname()) {
			return errors.New("GDS_OTLP_ENDPOINT must use HTTPS unless it addresses loopback")
		}
		return nil
	default:
		return errors.New("GDS_OTLP_ENDPOINT must use http or https")
	}
}

func FromEnvironment(store *state.Store) (*Exporter, error) {
	endpoint := strings.TrimSpace(os.Getenv("GDS_OTLP_ENDPOINT"))
	if endpoint == "" {
		return nil, nil
	}
	headers := map[string]string{}
	if authorization := os.Getenv("GDS_OTLP_AUTHORIZATION"); authorization != "" {
		if strings.ContainsAny(authorization, "\x00\r\n") || len(authorization) > 4096 {
			return nil, errors.New("GDS_OTLP_AUTHORIZATION is invalid")
		}
		headers["Authorization"] = authorization
	}
	config := Config{Endpoint: endpoint, Headers: headers, MaxPending: 10_000, MaxBytes: 64 << 20,
		MaximumAttempts: 10, BatchSize: 100}
	if err := validateTransport(config); err != nil {
		return nil, err
	}
	return &Exporter{Store: store, Client: &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirectRefused }},
		Now: time.Now, Config: config}, nil
}

func (exporter Exporter) Emit(ctx context.Context, event Event) error {
	if exporter.Store == nil || event.EventID == "" || event.Name == "" || event.OccurredAt.IsZero() ||
		(event.SignalType != "log" && event.SignalType != "metric" && event.SignalType != "trace") {
		return errors.New("telemetry event is invalid")
	}
	if err := validateAttributes(event.Attributes); err != nil {
		return err
	}
	body, err := encodeOTLP(event)
	if err != nil {
		return err
	}
	return exporter.Store.EnqueueTelemetry(ctx, state.TelemetryEvent{EventID: event.EventID, SignalType: event.SignalType,
		Body: body, Status: "pending", NextAttemptAt: event.OccurredAt, CreatedAt: event.OccurredAt}, exporter.Config.MaxPending, exporter.Config.MaxBytes)
}

func (exporter Exporter) Flush(ctx context.Context) error {
	if exporter.Store == nil || exporter.Client == nil || exporter.Now == nil || exporter.Config.Endpoint == "" ||
		exporter.Config.BatchSize < 1 || exporter.Config.MaximumAttempts < 1 {
		return errors.New("telemetry exporter configuration is invalid")
	}
	if err := validateTransport(exporter.Config); err != nil {
		return err
	}
	// The redirect policy belongs to the exporter, not to whoever supplied the
	// client, so it is reapplied to a copy that keeps the caller's transport and
	// timeout. A caller cannot opt back into following redirects.
	client := *exporter.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirectRefused }
	events, err := exporter.Store.PendingTelemetry(ctx, exporter.Now(), exporter.Config.BatchSize)
	if err != nil {
		return err
	}
	var failures []error
	for _, event := range events {
		endpoint := strings.TrimRight(exporter.Config.Endpoint, "/")
		if !strings.HasSuffix(endpoint, "/v1/logs") && !strings.HasSuffix(endpoint, "/v1/metrics") && !strings.HasSuffix(endpoint, "/v1/traces") {
			endpoint += "/v1/" + signalEndpoint(event.SignalType)
		}
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(event.Body))
		if requestErr == nil {
			request.Header.Set("Content-Type", "application/json")
			for key, value := range exporter.Config.Headers {
				request.Header.Set(key, value)
			}
			var response *http.Response
			response, requestErr = client.Do(request)
			if response != nil {
				response.Body.Close()
			}
			if requestErr == nil && (response.StatusCode < 200 || response.StatusCode >= 300) {
				requestErr = fmt.Errorf("OTLP HTTP status class %dxx", response.StatusCode/100)
			}
		}
		if requestErr == nil {
			if err := exporter.Store.CompleteTelemetry(ctx, event.EventID); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		backoff := time.Second << min(event.Attempts, 10)
		if err := exporter.Store.RetryTelemetry(ctx, event.EventID, "export-failed", exporter.Now().Add(backoff), exporter.Config.MaximumAttempts); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func signalEndpoint(signalType string) string {
	switch signalType {
	case "metric":
		return "metrics"
	case "trace":
		return "traces"
	default:
		return "logs"
	}
}

func encodeOTLP(event Event) ([]byte, error) {
	attributes, err := otlpAttributes(event.EventID, event.Attributes)
	if err != nil {
		return nil, err
	}
	timestamp := fmt.Sprintf("%d", event.OccurredAt.UnixNano())
	resource := map[string]any{"attributes": []any{
		map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "github-device-sync"}},
	}}
	scope := map[string]any{"name": "github-device-sync", "version": "0.4.0"}
	var envelope map[string]any
	switch event.SignalType {
	case "log":
		envelope = map[string]any{"resourceLogs": []any{map[string]any{
			"resource": resource, "scopeLogs": []any{map[string]any{"scope": scope, "logRecords": []any{map[string]any{
				"timeUnixNano": timestamp, "severityText": "INFO", "body": map[string]any{"stringValue": event.Name}, "attributes": attributes,
			}}}},
		}}}
	case "metric":
		envelope = map[string]any{"resourceMetrics": []any{map[string]any{
			"resource": resource, "scopeMetrics": []any{map[string]any{"scope": scope, "metrics": []any{map[string]any{
				"name": event.Name, "gauge": map[string]any{"dataPoints": []any{map[string]any{
					"timeUnixNano": timestamp, "asInt": "1", "attributes": attributes,
				}}},
			}}}},
		}}}
	case "trace":
		digest := sha256.Sum256([]byte(event.EventID))
		envelope = map[string]any{"resourceSpans": []any{map[string]any{
			"resource": resource, "scopeSpans": []any{map[string]any{"scope": scope, "spans": []any{map[string]any{
				"traceId": fmt.Sprintf("%x", digest[:16]), "spanId": fmt.Sprintf("%x", digest[16:24]),
				"name": event.Name, "kind": 1, "startTimeUnixNano": timestamp, "endTimeUnixNano": timestamp,
				"attributes": attributes, "status": map[string]any{"code": 1},
			}}}},
		}}}
	default:
		return nil, errors.New("unsupported telemetry signal")
	}
	return json.Marshal(envelope)
}

func otlpAttributes(eventID string, input map[string]any) ([]any, error) {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]any, 0, len(keys)+1)
	for _, key := range keys {
		value, err := otlpValue(input[key])
		if err != nil {
			return nil, fmt.Errorf("OTLP attribute %s: %w", key, err)
		}
		result = append(result, map[string]any{"key": key, "value": value})
	}
	return append(result, map[string]any{"key": "gds.event_id", "value": map[string]any{"stringValue": eventID}}), nil
}

func otlpValue(value any) (map[string]any, error) {
	switch typed := value.(type) {
	case string:
		return map[string]any{"stringValue": typed}, nil
	case bool:
		return map[string]any{"boolValue": typed}, nil
	case int:
		return map[string]any{"intValue": fmt.Sprint(typed)}, nil
	case int64:
		return map[string]any{"intValue": fmt.Sprint(typed)}, nil
	case float64:
		return map[string]any{"doubleValue": typed}, nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"stringValue": string(raw)}, nil
	}
}

func validateAttributes(attributes map[string]any) error {
	if len(attributes) > 64 {
		return errors.New("telemetry attribute cardinality exceeds bound")
	}
	for key, value := range attributes {
		lower := strings.ToLower(key)
		if key == "" || strings.ContainsAny(key, "\x00\r\n") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "private_key") {
			return errors.New("telemetry attribute key crosses the secret boundary")
		}
		raw, err := json.Marshal(value)
		if err != nil || len(raw) > 4096 || secretValue.Match(raw) {
			return errors.New("telemetry attribute value crosses the secret boundary")
		}
	}
	return nil
}
