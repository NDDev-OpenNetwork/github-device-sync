package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDeviceEvidencePersistsLatestSignedArtifact(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for index, observed := range []time.Time{now.Add(-time.Hour), now} {
		value := DeviceEvidenceRecord{EvidenceID: "evidence-" + string(rune('a'+index)), DeviceID: "device:test",
			ObservedAt: observed, ExpiresAt: observed.Add(6 * time.Hour), EvidenceDigest: "sha256:digest-" + string(rune('a'+index)),
			Body: json.RawMessage(`{"payload":{"device_id":"device:test"},"signature":{"value":"detached"}}`), InsertedAt: now}
		if err := store.PutDeviceEvidence(context.Background(), value); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := store.LatestDeviceEvidence(context.Background(), "device:test")
	if err != nil || !latest.ObservedAt.Equal(now) || latest.EvidenceID != "evidence-b" {
		t.Fatalf("latest=%#v err=%v", latest, err)
	}
}
