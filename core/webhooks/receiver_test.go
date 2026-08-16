package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type staticSecret []byte

func (secret staticSecret) WebhookSecret(context.Context) ([]byte, error) {
	return append([]byte(nil), secret...), nil
}

func testReceiver(t *testing.T) (*Receiver, *state.Store, []byte) {
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
	secret := []byte("0123456789abcdef0123456789abcdef")
	receiver := &Receiver{
		Store: store, Secrets: staticSecret(secret),
		Now:        func() time.Time { return time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC) },
		MaxPayload: 4096,
	}
	return receiver, store, secret
}

func TestReceiverVerifiesAndEnqueuesBeforeAcceptedResponse(t *testing.T) {
	receiver, store, secret := testReceiver(t)
	payload := []byte(`{"ref":"refs/heads/main","repository":{"id":1}}`)
	response := deliver(receiver, "delivery-1", "push", payload, signature(payload, secret))
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"duplicate":false`) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	queued, err := store.GetWebhook(context.Background(), "delivery-1")
	if err != nil || queued.Status != "queued" || !bytes.Equal(queued.Payload, payload) {
		t.Fatalf("queued=%#v err=%v", queued, err)
	}
	duplicate := deliver(receiver, "delivery-1", "push", payload, signature(payload, secret))
	if duplicate.Code != http.StatusAccepted || !strings.Contains(duplicate.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate=%d %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestReceiverRejectsConflictingDeliveryIdentity(t *testing.T) {
	receiver, _, secret := testReceiver(t)
	first := []byte(`{"ref":"refs/heads/main"}`)
	second := []byte(`{"ref":"refs/heads/other"}`)
	if response := deliver(receiver, "delivery-2", "push", first, signature(first, secret)); response.Code != http.StatusAccepted {
		t.Fatalf("first response=%d", response.Code)
	}
	response := deliver(receiver, "delivery-2", "push", second, signature(second, secret))
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict response=%d %s", response.Code, response.Body.String())
	}
}

func TestReceiverRejectsInvalidSignatureWithoutQueueing(t *testing.T) {
	receiver, store, _ := testReceiver(t)
	payload := []byte(`{"ref":"refs/heads/main"}`)
	response := deliver(receiver, "delivery-3", "push", payload, "sha256="+strings.Repeat("0", 64))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	if _, err := store.GetWebhook(context.Background(), "delivery-3"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("invalid signature was queued: %v", err)
	}
}

func TestReceiverRejectsUnsupportedEventAndAction(t *testing.T) {
	receiver, _, secret := testReceiver(t)
	payload := []byte(`{"action":"opened"}`)
	response := deliver(receiver, "delivery-4", "unknown", payload, signature(payload, secret))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown event response=%d", response.Code)
	}
	payload = []byte(`{"action":"future-action"}`)
	response = deliver(receiver, "delivery-5", "pull_request", payload, signature(payload, secret))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown action response=%d", response.Code)
	}
}

func TestReceiverAcceptsExactGovernanceEventContracts(t *testing.T) {
	receiver, _, secret := testReceiver(t)
	cases := []struct {
		id      string
		event   string
		payload []byte
	}{
		{"delivery-ruleset", "repository_ruleset", []byte(`{"action":"edited"}`)},
		{"delivery-security", "security_and_analysis", []byte(`{"changes":{}}`)},
		{"delivery-protection", "branch_protection_rule", []byte(`{"action":"created"}`)},
	}
	for _, testCase := range cases {
		response := deliver(
			receiver, testCase.id, testCase.event, testCase.payload,
			signature(testCase.payload, secret),
		)
		if response.Code != http.StatusAccepted {
			t.Fatalf("event=%s response=%d %s", testCase.event, response.Code, response.Body.String())
		}
	}
}

func TestReceiverEnforcesMethodContentTypeAndPayloadBound(t *testing.T) {
	receiver, _, secret := testReceiver(t)
	request := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	response := httptest.NewRecorder()
	receiver.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET response=%d", response.Code)
	}

	payload := []byte(`{"ref":"refs/heads/main"}`)
	request = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "text/plain")
	response = httptest.NewRecorder()
	receiver.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content-type response=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json-malformed")
	response = httptest.NewRecorder()
	receiver.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("malformed content-type response=%d", response.Code)
	}

	receiver.MaxPayload = 1024
	large := []byte(`{"payload":"` + strings.Repeat("x", 1100) + `"}`)
	response = deliver(receiver, "delivery-6", "push", large, signature(large, secret))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large response=%d", response.Code)
	}
}

func deliver(
	receiver *Receiver,
	deliveryID string,
	eventType string,
	payload []byte,
	signatureHeader string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Delivery", deliveryID)
	request.Header.Set("X-GitHub-Event", eventType)
	request.Header.Set("X-Hub-Signature-256", signatureHeader)
	response := httptest.NewRecorder()
	receiver.ServeHTTP(response, request)
	return response
}

func signature(payload, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
