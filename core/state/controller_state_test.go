package state

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func repositoryObservation(at time.Time, body string) RepositoryObservation {
	return RepositoryObservation{
		InstallationID: "installation:test", ProviderRepositoryID: 123,
		Owner: "example", Name: "repository", AccessState: "available",
		ObservedAt: at, ETag: `"etag"`, Body: json.RawMessage(body), RequestID: "request-1",
	}
}

func TestRepositoryObservationIsFreshnessOrderedAndIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first := repositoryObservation(testTime, `{"id":123,"name":"repository"}`)
	if err := store.PutRepositoryObservation(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepositoryObservation(ctx, first); err != nil {
		t.Fatalf("idempotent observation: %v", err)
	}
	conflict := first
	conflict.Body = json.RawMessage(`{"id":123,"name":"changed"}`)
	if err := store.PutRepositoryObservation(ctx, conflict); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("same-time conflict error=%v", err)
	}
	older := first
	older.ObservedAt = testTime.Add(-time.Second)
	if err := store.PutRepositoryObservation(ctx, older); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale observation error=%v", err)
	}
	newer := repositoryObservation(testTime.Add(time.Second), `{"id":123,"name":"newer"}`)
	newer.RequestID = "request-2"
	if err := store.PutRepositoryObservation(ctx, newer); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetRepositoryObservation(ctx, first.InstallationID, first.ProviderRepositoryID)
	if err != nil || !loaded.ObservedAt.Equal(newer.ObservedAt) || loaded.RequestID != "request-2" ||
		loaded.BodyDigest == "" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestUnavailableObservationCannotPersistProviderBody(t *testing.T) {
	store := newTestStore(t)
	observation := repositoryObservation(testTime, `{"private":"payload"}`)
	observation.AccessState = "inaccessible"
	if err := store.PutRepositoryObservation(context.Background(), observation); err == nil {
		t.Fatal("unavailable observation accepted provider body")
	}
	observation.Body = nil
	if err := store.PutRepositoryObservation(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
}

func TestReconciliationJournalRequiresStableErrorCode(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	record := ReconciliationRecord{
		ReconciliationID: "reconciliation-test", Scope: "installation",
		ScopeID: "installation:test", Status: "running", StartedAt: testTime,
		Cursor: json.RawMessage(`{"page":1}`),
	}
	if err := store.StartReconciliation(ctx, record); err != nil {
		t.Fatal(err)
	}
	started, err := store.GetReconciliation(ctx, record.ReconciliationID)
	if err != nil || started.CursorSequence != 0 || started.CursorDigest == "" {
		t.Fatalf("initial cursor is not durable: %#v %v", started, err)
	}
	advanced, err := store.UpdateReconciliationCursor(
		ctx, record.ReconciliationID, 0, map[string]any{"page": 2},
	)
	if err != nil || advanced.CursorSequence != 1 || advanced.CursorDigest == started.CursorDigest ||
		string(advanced.Cursor) != `{"page":2}` {
		t.Fatalf("cursor did not advance durably: %#v %v", advanced, err)
	}
	if _, err := store.UpdateReconciliationCursor(
		ctx, record.ReconciliationID, 0, map[string]any{"page": 3},
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale cursor update error=%v", err)
	}
	if err := store.FinishReconciliation(
		ctx, record.ReconciliationID, "failed", testTime.Add(time.Second),
		map[string]any{"observed": 10}, "private error detail",
	); err == nil {
		t.Fatal("unsafe reconciliation error text accepted")
	}
	if err := store.FinishReconciliation(
		ctx, record.ReconciliationID, "partial", testTime.Add(time.Second),
		map[string]any{"observed": 10}, "GDS_PROVIDER_TRANSIENT",
	); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetReconciliation(ctx, record.ReconciliationID)
	if err != nil || loaded.Status != "partial" || loaded.FinishedAt == nil ||
		loaded.LastError != "GDS_PROVIDER_TRANSIENT" || loaded.CursorSequence != 1 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestStateSummaryIncludesControllerTables(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, webhookFixture("delivery-summary")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepositoryObservation(
		ctx, repositoryObservation(testTime, `{"id":123}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.StartReconciliation(ctx, ReconciliationRecord{
		ReconciliationID: "reconciliation-summary", Scope: "estate", ScopeID: "estate:test",
		Status: "running", StartedAt: testTime,
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := store.Summary(ctx)
	if err != nil || summary.Webhooks != 1 || summary.Observations != 1 || summary.Reconciliations != 1 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
}
