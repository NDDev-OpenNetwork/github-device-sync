// Package webhooks verifies and durably enqueues GitHub webhook deliveries.
package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

const DefaultMaxPayloadBytes = int64(10 << 20)

var deliveryIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

type SecretSource interface {
	WebhookSecret(context.Context) ([]byte, error)
}

type EventRule struct {
	Actions map[string]struct{}
}

type Receiver struct {
	Store      *state.Store
	Secrets    SecretSource
	Now        func() time.Time
	MaxPayload int64
	Events     map[string]EventRule
}

func DefaultEventRules() map[string]EventRule {
	return map[string]EventRule{
		"ping":   {},
		"push":   {},
		"create": {},
		"delete": {},
		"installation": actions(
			"created", "deleted", "suspend", "unsuspend", "new_permissions_accepted",
		),
		"installation_repositories": actions("added", "removed"),
		"repository": actions(
			"created", "deleted", "archived", "unarchived", "renamed", "transferred",
			"edited", "publicized", "privatized",
		),
		"repository_ruleset":              actions("created", "deleted", "edited"),
		"security_and_analysis":           {},
		"branch_protection_configuration": actions("enabled", "disabled"),
		"branch_protection_rule":          actions("created", "deleted", "edited"),
		"pull_request": actions(
			"opened", "closed", "reopened", "synchronize", "edited",
			"ready_for_review", "converted_to_draft",
		),
		"check_run":    actions("created", "completed"),
		"check_suite":  actions("completed"),
		"workflow_run": actions("requested", "in_progress", "completed"),
		"release": actions(
			"published", "unpublished", "created", "edited", "deleted", "prereleased", "released",
		),
	}
}

func (receiver *Receiver) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if receiver.Store == nil || receiver.Secrets == nil {
		http.Error(writer, "webhook receiver unavailable", http.StatusServiceUnavailable)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if mediaTypeErr != nil || !strings.EqualFold(mediaType, "application/json") {
		http.Error(writer, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	deliveryID := request.Header.Get("X-GitHub-Delivery")
	eventType := request.Header.Get("X-GitHub-Event")
	if !deliveryIDPattern.MatchString(deliveryID) || eventType == "" ||
		strings.ContainsAny(eventType, "\x00\r\n") {
		http.Error(writer, "invalid webhook identity", http.StatusBadRequest)
		return
	}
	rules := receiver.Events
	if rules == nil {
		rules = DefaultEventRules()
	}
	rule, allowed := rules[eventType]
	if !allowed {
		http.Error(writer, "unsupported webhook event", http.StatusBadRequest)
		return
	}
	limit := receiver.MaxPayload
	if limit == 0 {
		limit = DefaultMaxPayloadBytes
	}
	if limit < 1024 || limit > 25<<20 {
		http.Error(writer, "webhook receiver misconfigured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil {
		http.Error(writer, "cannot read webhook payload", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > limit {
		http.Error(writer, "webhook payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	secret, err := receiver.Secrets.WebhookSecret(request.Context())
	if err != nil || len(secret) < 16 {
		clear(secret)
		http.Error(writer, "webhook authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	secretCopy := append([]byte(nil), secret...)
	clear(secret)
	defer clear(secretCopy)
	if !verifySignature(body, request.Header.Get("X-Hub-Signature-256"), secretCopy) {
		http.Error(writer, "webhook signature invalid", http.StatusUnauthorized)
		return
	}
	if !json.Valid(body) {
		http.Error(writer, "webhook payload is not valid JSON", http.StatusBadRequest)
		return
	}
	if err := validateAction(body, rule); err != nil {
		http.Error(writer, "unsupported webhook action", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if receiver.Now != nil {
		now = receiver.Now().UTC()
	}
	inserted, err := receiver.Store.EnqueueWebhook(request.Context(), state.WebhookDelivery{
		DeliveryID: deliveryID, EventType: eventType, Payload: body, ReceivedAt: now,
	})
	if errors.Is(err, state.ErrWebhookConflict) {
		http.Error(writer, "webhook delivery conflict", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(writer, "webhook queue unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(writer, `{"accepted":true,"duplicate":%t}`, !inserted)
}

func verifySignature(payload []byte, header string, secret []byte) bool {
	if !strings.HasPrefix(header, "sha256=") || len(header) != len("sha256=")+sha256.Size*2 {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return hmac.Equal(provided, mac.Sum(nil))
}

func validateAction(payload []byte, rule EventRule) error {
	var envelope struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if rule.Actions == nil {
		if envelope.Action != "" {
			return fmt.Errorf("event does not accept an action")
		}
		return nil
	}
	if _, found := rule.Actions[envelope.Action]; !found {
		return fmt.Errorf("action %q is not allowed", envelope.Action)
	}
	return nil
}

func actions(values ...string) EventRule {
	rule := EventRule{Actions: map[string]struct{}{}}
	for _, value := range values {
		rule.Actions[value] = struct{}{}
	}
	return rule
}
