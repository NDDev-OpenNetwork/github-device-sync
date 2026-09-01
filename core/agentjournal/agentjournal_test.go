package agentjournal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/agent-runtime/goal"
)

func fixedNow() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }

func TestGoalIDDerivesAValidRuntimeIdentity(t *testing.T) {
	id := GoalID("op_01M0G5WWVAHQGK33XJ9M183MJ5")
	if id != "handoff.op-01m0g5wwvahqgk33xj9m183mj5" {
		t.Fatalf("id=%q", id)
	}
	if _, err := goal.New(id, "x", []goal.ChecklistItem{{ID: "a", Acceptance: "b"}}, nil, fixedNow()); err != nil {
		t.Fatalf("derived id refused by agent-runtime: %v", err)
	}
}

func TestRecordCheckpointThenVerifiedCompletesTheGoalStory(t *testing.T) {
	recorder := Recorder{Directory: filepath.Join(t.TempDir(), "agent-journals"), Now: fixedNow}
	ctx := context.Background()
	err := recorder.RecordCheckpoint(ctx, "op_TEST123", "device:example/repo", "checkpoint the refactor", []string{"a.go", "b.go"}, "operation:op_TEST123", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	store := goal.Store{Path: recorder.JournalPath("op_TEST123")}
	journal, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Validate(); err != nil {
		t.Fatalf("stored journal does not validate under agent-runtime: %v", err)
	}
	byID := map[string]goal.ChecklistItem{}
	for _, item := range journal.Goal.Acceptance {
		byID[item.ID] = item
	}
	if byID[CheckpointItem].Status != goal.ItemComplete {
		t.Fatal("checkpoint item is not complete after apply")
	}
	if byID[VerifiedItem].Status != goal.ItemPending {
		t.Fatal("verification item must stay pending until verify")
	}
	// Files ride as evidence on the completed item.
	if len(byID[CheckpointItem].Evidence) != 3 {
		t.Fatalf("evidence=%+v", byID[CheckpointItem].Evidence)
	}

	if err := recorder.RecordVerified(ctx, "op_TEST123", "gds handoff --verify op_TEST123", "session-2"); err != nil {
		t.Fatal(err)
	}
	journal, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range journal.Goal.Acceptance {
		if item.ID == VerifiedItem && item.Status != goal.ItemComplete {
			t.Fatal("verification item is not complete after verify")
		}
	}
	// A second verify is idempotent, not a corruption.
	if err := recorder.RecordVerified(ctx, "op_TEST123", "gds handoff --verify op_TEST123", "session-2"); err != nil {
		t.Fatalf("re-verify must not fail: %v", err)
	}

	// The lifecycle stream carries the dispatched and completed handoff events.
	raw, err := os.ReadFile(filepath.Join(recorder.Directory, "handoff-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 {
		t.Fatalf("events=%d, want at least dispatched and completed", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("event stream is not JSONL: %v", err)
	}
}

func TestRecordVerifiedWithoutACheckpointRefuses(t *testing.T) {
	recorder := Recorder{Directory: t.TempDir(), Now: fixedNow}
	if err := recorder.RecordVerified(context.Background(), "op_NOPE", "ref", "s"); err == nil {
		t.Fatal("verify without a stored goal was accepted")
	}
}
