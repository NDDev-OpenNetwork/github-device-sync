package state

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	rolloutdomain "github.com/NDDev-OpenNetwork/github-device-sync/core/rollout"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestAcceptedBundleLedgerPreservesAntiRollbackFloor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	second := acceptedBundle(2, 'b')
	if err := store.PutAcceptedBundle(ctx, second, nil, testTime); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAcceptedBundle(ctx, second, nil, testTime); err != nil {
		t.Fatalf("idempotent acceptance failed: %v", err)
	}

	first := acceptedBundle(1, 'a')
	if err := store.PutAcceptedBundle(ctx, first, nil, testTime); !errors.Is(err, ErrRollbackBlocked) {
		t.Fatalf("downgrade error = %v, want ErrRollbackBlocked", err)
	}
	authorization := &bundle.RollbackAuthorization{
		RolloutID:      "rollout_01J00000000000000000000000",
		TargetSequence: first.ReleaseSequence, TargetDigest: first.ArtifactDigest,
		ScopeDigest: digestWith('d'), Reason: "test rollback", ApprovalRef: "approval:test-rollback",
		ExpiresAt: testTime.Add(time.Hour),
	}
	if err := store.PutAcceptedBundle(ctx, first, authorization, testTime); err != nil {
		t.Fatalf("authorized rollback evidence was not recorded: %v", err)
	}
	state, err := store.BundleAcceptanceState(ctx, "gds-release")
	if err != nil {
		t.Fatal(err)
	}
	if state.HighestSequence != 2 || len(state.AcceptedDigests) != 2 {
		t.Fatalf("acceptance floor was lowered or incomplete: %#v", state)
	}
	if state.AcceptedVersions[2] != second.BundleVersion {
		t.Fatalf("accepted versions = %#v", state.AcceptedVersions)
	}
	regression := acceptedBundle(3, 'e')
	regression.BundleVersion = "1.1.9"
	if err := store.PutAcceptedBundle(ctx, regression, nil, testTime); !errors.Is(err, ErrVersionRegression) {
		t.Fatalf("version regression error = %v, want ErrVersionRegression", err)
	}

	conflict := second
	conflict.ArtifactDigest = digestWith('c')
	if err := store.PutAcceptedBundle(ctx, conflict, nil, testTime); !errors.Is(err, ErrBundleConflict) {
		t.Fatalf("sequence conflict error = %v, want ErrBundleConflict", err)
	}
	if _, err := store.db.ExecContext(
		ctx, `UPDATE accepted_bundles SET bundle_version = '9.9.9' WHERE release_sequence = 2`,
	); err == nil {
		t.Fatal("append-only bundle ledger allowed update")
	}
	if _, err := store.db.ExecContext(
		ctx, `DELETE FROM accepted_bundles WHERE release_sequence = 2`,
	); err == nil {
		t.Fatal("append-only bundle ledger allowed delete")
	}
}

func TestRolloutStateIsImmutableResumableAndWaveGated(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	plan := stateRolloutPlan(t, 6)
	if err := store.PutRollout(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRollout(ctx, plan); err != nil {
		t.Fatalf("idempotent rollout insert failed: %v", err)
	}
	conflict := plan
	conflict.PlanDigest = digestWith('f')
	if err := store.PutRollout(ctx, conflict); !errors.Is(err, ErrRolloutConflict) {
		t.Fatalf("rollout conflict error = %v, want ErrRolloutConflict", err)
	}

	if err := store.TransitionRollout(ctx, plan.RolloutID, "planned", "active", 0, testTime); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionRollout(
		ctx, plan.RolloutID, "active", "active", 1, testTime.Add(time.Second),
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("unfinished wave advance error = %v, want ErrStateConflict", err)
	}
	completeWave(t, store, plan, 0, testTime.Add(time.Minute))
	if err := store.TransitionRollout(
		ctx, plan.RolloutID, "active", "active", 1, testTime.Add(2*time.Minute),
	); err != nil {
		t.Fatalf("completed canary did not advance: %v", err)
	}
	if err := store.TransitionRollout(
		ctx, plan.RolloutID, "active", "completed", 1, testTime.Add(3*time.Minute),
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("unfinished final wave completion error = %v, want ErrStateConflict", err)
	}
	completeWave(t, store, plan, 1, testTime.Add(4*time.Minute))
	if err := store.TransitionRollout(
		ctx, plan.RolloutID, "active", "completed", 1, testTime.Add(5*time.Minute),
	); err != nil {
		t.Fatalf("finished rollout did not complete: %v", err)
	}

	snapshot, err := store.GetRollout(ctx, plan.RolloutID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "completed" || snapshot.CurrentWave != 1 || snapshot.PlanDigest != plan.PlanDigest {
		t.Fatalf("unexpected rollout snapshot: %#v", snapshot)
	}
	summary, err := store.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Rollouts != 1 || summary.RolloutTargets != 6 || summary.RolloutEvents != 16 {
		t.Fatalf("unexpected rollout state counts: %#v", summary)
	}
	if _, err := store.db.ExecContext(
		ctx, `UPDATE rollouts SET plan_digest = ? WHERE rollout_id = ?`, digestWith('e'), plan.RolloutID,
	); err == nil {
		t.Fatal("immutable rollout trigger allowed plan update")
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM rollout_events WHERE rollout_id = ?`, plan.RolloutID); err == nil {
		t.Fatal("append-only rollout journal allowed delete")
	}
}

func completeWave(
	t *testing.T,
	store *Store,
	plan rolloutdomain.Plan,
	ordinal int,
	startedAt time.Time,
) {
	t.Helper()
	ctx := context.Background()
	for index, repositoryID := range plan.Waves[ordinal].RepositoryIDs {
		now := startedAt.Add(time.Duration(index) * time.Second)
		if err := store.TransitionRolloutTarget(
			ctx, plan.RolloutID, repositoryID, "pending", "active", now, map[string]any{"stage": "active"},
		); err != nil {
			t.Fatal(err)
		}
		if err := store.TransitionRolloutTarget(
			ctx, plan.RolloutID, repositoryID, "active", "succeeded", now.Add(time.Millisecond),
			map[string]any{"checks": "passed"},
		); err != nil {
			t.Fatal(err)
		}
	}
}

func acceptedBundle(sequence int, marker byte) AcceptedBundle {
	return AcceptedBundle{
		TrustDomain: "gds-release", ReleaseSequence: sequence,
		BundleVersion:  fmt.Sprintf("1.%d.0", sequence),
		ArtifactDigest: digestWith(marker), ManifestDigest: digestWith(marker + 1),
		AttestationIdentityDigest: digestWith(marker + 2),
		AcceptedAt:                testTime.Add(time.Duration(sequence) * time.Minute),
	}
}

func stateRolloutPlan(t *testing.T, count int) rolloutdomain.Plan {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	targets := make([]string, count)
	for index := range targets {
		targets[index] = fmt.Sprintf("repo_01J%023d", index+1)
	}
	plan, findings := rolloutdomain.Build(rolloutdomain.BuildInput{
		RolloutID: "rollout_01J00000000000000000000000", CreatedAt: testTime,
		Envelope: bundle.ReleaseEnvelope{
			SchemaVersion: 1, BundleVersion: "1.0.0", ReleaseSequence: 1,
			Channel: "canary", SourceCommit: "0123456789abcdef0123456789abcdef01234567",
			ManifestDigest: digestWith('a'), ArtifactDigest: digestWith('b'),
		},
		RepositoryIDs: targets,
		Rings: []rolloutdomain.RingSpec{
			{ID: "canary", MaxRepositories: 2},
			{ID: "general", CumulativePercent: 100},
		},
		MaxFailureRate: 0,
	}, schemas)
	if len(findings) != 0 {
		t.Fatalf("rollout build findings: %#v", findings)
	}
	return plan
}

func digestWith(value byte) string {
	return "sha256:" + string(makeBytes(value, 64))
}

func makeBytes(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}
