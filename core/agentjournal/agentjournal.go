// Package agentjournal records a handoff as an agent-runtime goal.
//
// gds handoff checkpoints unfinished work for a next actor, and that is
// exactly the contract agent-runtime's Goal journal and handoff lifecycle
// events were written for: a durable, revisioned, vendor-neutral record of a
// goal another agent is expected to pick up. The journal lives beside the
// operation state, the lifecycle events append to a JSONL stream, and both
// use agent-runtime's own validation -- GDS adds identity and evidence, never
// a private dialect.
package agentjournal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/agent-runtime/goal"
	"github.com/NDDev-OpenNetwork/agent-runtime/observability"
)

const (
	// CheckpointItem is the acceptance item the apply completes: the
	// checkpoint commit exists and is published.
	CheckpointItem = "checkpoint-published"
	// VerifiedItem is the acceptance item the verify completes: the handoff
	// operation re-proved the checkpoint.
	VerifiedItem = "handoff-verified"
)

var invalidIDRunes = regexp.MustCompile(`[^a-z0-9._-]+`)

// Recorder writes goal journals and lifecycle events under one directory.
type Recorder struct {
	Directory string
	Now       func() time.Time
}

func (r Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

// GoalID derives a valid agent-runtime goal id from an operation id.
func GoalID(operationID string) string {
	lowered := strings.ToLower(strings.TrimSpace(operationID))
	lowered = strings.ReplaceAll(lowered, "_", "-")
	lowered = invalidIDRunes.ReplaceAllString(lowered, "-")
	lowered = strings.Trim(lowered, "._-")
	if lowered == "" {
		lowered = "operation"
	}
	return "handoff." + lowered
}

// JournalPath is where an operation's goal journal lives.
func (r Recorder) JournalPath(operationID string) string {
	return filepath.Join(r.Directory, GoalID(operationID)+".json")
}

func (r Recorder) eventsPath() string {
	return filepath.Join(r.Directory, "handoff-events.jsonl")
}

// RecordCheckpoint creates the goal journal for an applied handoff and marks
// the checkpoint item complete with its commit evidence, then appends the
// dispatched handoff event.
func (r Recorder) RecordCheckpoint(
	ctx context.Context,
	operationID, repositoryID, intent string,
	files []string,
	commitReference string,
	sessionID string,
) error {
	if err := os.MkdirAll(r.Directory, 0o700); err != nil {
		return fmt.Errorf("create agent journal directory: %w", err)
	}
	now := r.now()
	journal, err := goal.New(GoalID(operationID), intent, []goal.ChecklistItem{
		{ID: CheckpointItem, Acceptance: "the checkpoint commit exists on the published branch"},
		{ID: VerifiedItem, Acceptance: "gds handoff --verify re-proved the checkpoint"},
	}, []string{"integration", "cleanup"}, now)
	if err != nil {
		return fmt.Errorf("draft handoff goal: %w", err)
	}
	evidence := []goal.Evidence{{Type: goal.EvidenceCommit, Reference: commitReference, Result: "checkpoint published for " + repositoryID}}
	for _, file := range files {
		evidence = append(evidence, goal.Evidence{Type: goal.EvidenceFile, Reference: file, Result: "carried by the checkpoint"})
	}
	store := goal.Store{Path: r.JournalPath(operationID)}
	// The store insists on a genesis create followed by a CAS update -- the
	// same discipline every other consumer gets, so GDS takes it too.
	if err := store.Create(journal); err != nil {
		return fmt.Errorf("store handoff goal: %w", err)
	}
	if _, err := store.Update(journal.Revision, func(stored *goal.Journal) error {
		return stored.CompleteItem(CheckpointItem, evidence, now)
	}); err != nil {
		return fmt.Errorf("complete checkpoint item: %w", err)
	}
	return r.emit(ctx, operationID, sessionID, observability.HandoffStageDispatched)
}

// RecordVerified marks the verification item complete on the stored journal
// and appends the completed handoff event.
func (r Recorder) RecordVerified(
	ctx context.Context,
	operationID string,
	verificationReference string,
	sessionID string,
) error {
	store := goal.Store{Path: r.JournalPath(operationID)}
	current, err := store.Load()
	if err != nil {
		return fmt.Errorf("load handoff goal: %w", err)
	}
	// A re-verify is idempotent: the journal already says it, and the event
	// stream already carries it, so neither is repeated.
	for _, item := range current.Goal.Acceptance {
		if item.ID == VerifiedItem && item.Status == goal.ItemComplete {
			return nil
		}
	}
	now := r.now()
	if _, err := store.Update(current.Revision, func(journal *goal.Journal) error {
		return journal.CompleteItem(VerifiedItem, []goal.Evidence{{
			Type: goal.EvidenceCommand, Reference: verificationReference,
			Result: "handoff verify succeeded",
		}}, now)
	}); err != nil {
		return fmt.Errorf("complete verification item: %w", err)
	}
	return r.emit(ctx, operationID, sessionID, observability.HandoffStageCompleted)
}

func (r Recorder) emit(ctx context.Context, operationID, sessionID string, stage observability.HandoffStage) error {
	sink, err := observability.OpenJSONLSink(r.eventsPath(), observability.JSONLOptions{Name: "gds-handoff"})
	if err != nil {
		return fmt.Errorf("open handoff event sink: %w", err)
	}
	defer sink.Close(ctx)
	emitter, err := observability.NewEmitter(
		observability.Runtime{ID: "gds", Version: "handoff-v1"},
		[]observability.Sink{sink},
		observability.Options{Clock: r.now},
	)
	if err != nil {
		return fmt.Errorf("build handoff event emitter: %w", err)
	}
	draft, err := observability.HandoffDraft(
		GoalID(operationID),
		observability.ActorWorker, observability.ActorWorker, stage, nil, nil,
		observability.Context{
			CorrelationID: GoalID(operationID),
			Actor:         observability.Actor{Kind: observability.ActorWorker, ID: sessionID},
			Attempt:       observability.AttemptInitial,
		},
	)
	if err != nil {
		return fmt.Errorf("draft handoff event: %w", err)
	}
	if _, _, err := emitter.Emit(ctx, draft); err != nil {
		return fmt.Errorf("emit handoff event: %w", err)
	}
	return nil
}
