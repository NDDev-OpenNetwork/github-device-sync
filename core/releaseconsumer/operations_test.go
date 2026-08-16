package releaseconsumer

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestReleaseHandlersBindMaterializationActivationRollbackAndRemoval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(ctx, filepath.Join(t.TempDir(), "state", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	installRoot := filepath.Join(t.TempDir(), "gds")

	firstVerified, firstRequest := verifiedInstallationFixture(
		t, schemas, "1.2.3", 7, bundle.AcceptanceState{}, now,
	)
	first, findings := BuildInstallCandidate(firstVerified, firstRequest, installRoot, schemas)
	if len(findings) != 0 {
		t.Fatalf("first candidate findings: %+v", findings)
	}
	materialize := MaterializeHandler{Candidate: first}
	materializeStep := releaseTestStep(
		MaterializeReleaseAction, Parameters(first, "install", firstRequest, "", nil),
	)
	if _, err := materialize.Apply(ctx, materializeStep); err != nil {
		t.Fatal(err)
	}
	if err := materialize.Verify(ctx, materializeStep, nil); err != nil {
		t.Fatal(err)
	}
	activate := ActivationHandler{
		Candidate: first, Operation: "install", Store: store, Now: func() time.Time { return now },
	}
	activateStep := releaseTestStep(
		ActivateReleaseAction, Parameters(first, "install", firstRequest, "", nil),
	)
	if _, err := activate.Apply(ctx, activateStep); err != nil {
		t.Fatal(err)
	}
	if err := activate.Verify(ctx, activateStep, nil); err != nil {
		t.Fatal(err)
	}
	acceptance, err := store.BundleAcceptanceState(ctx, first.Record.TrustDomain)
	if err != nil || acceptance.HighestSequence != 7 ||
		acceptance.AcceptedDigests[7] != first.Record.ArtifactDigest {
		t.Fatalf("first acceptance=%+v err=%v", acceptance, err)
	}

	secondVerified, secondRequest := verifiedInstallationFixture(
		t, schemas, "1.3.0", 8, acceptance, now.Add(time.Minute),
	)
	second, findings := BuildInstallCandidate(secondVerified, secondRequest, installRoot, schemas)
	if len(findings) != 0 {
		t.Fatalf("second candidate findings: %+v", findings)
	}
	secondMaterialize := MaterializeHandler{Candidate: second}
	secondMaterializeStep := releaseTestStep(
		MaterializeReleaseAction, Parameters(second, "upgrade", secondRequest, activeTarget(first), nil),
	)
	if _, err := secondMaterialize.Apply(ctx, secondMaterializeStep); err != nil {
		t.Fatal(err)
	}
	upgrade := ActivationHandler{
		Candidate: second, ExpectedCurrent: activeTarget(first), Operation: "upgrade",
		Store: store, Now: func() time.Time { return now.Add(time.Minute) },
	}
	upgradeStep := releaseTestStep(
		ActivateReleaseAction, Parameters(second, "upgrade", secondRequest, activeTarget(first), nil),
	)
	if _, err := upgrade.Apply(ctx, upgradeStep); err != nil {
		t.Fatal(err)
	}
	if err := upgrade.Verify(ctx, upgradeStep, nil); err != nil {
		t.Fatal(err)
	}

	scopeDigest, err := InstallScopeDigest(installRoot, first.Record.TrustDomain)
	if err != nil {
		t.Fatal(err)
	}
	authorization := &bundle.RollbackAuthorization{
		RolloutID:   "rollout_01KX83C1G6DFH809HC9VTW5Y6B",
		ScopeDigest: scopeDigest, TargetSequence: first.Record.ReleaseSequence,
		TargetDigest: first.Record.ArtifactDigest, Reason: "verified fixture rollback",
		ApprovalRef: "approval:owner:release-rollback", ExpiresAt: now.Add(time.Hour),
	}
	if _, lifecycleFindings := ValidateLifecycle("rollback", first, authorization, now.Add(2*time.Minute)); len(lifecycleFindings) != 0 {
		t.Fatalf("rollback lifecycle findings: %+v", lifecycleFindings)
	}
	rollback := ActivationHandler{
		Candidate: first, ExpectedCurrent: activeTarget(second), Operation: "rollback",
		Store: store, Authorization: authorization, Now: func() time.Time { return now.Add(2 * time.Minute) },
	}
	rollbackStep := releaseTestStep(
		RollbackReleaseAction,
		Parameters(first, "rollback", firstRequest, activeTarget(second), authorization),
	)
	rollbackPlan, err := operations.NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGW", now, now.Add(15*time.Minute),
		operations.PlanInput{
			Operation: "release-rollback",
			Actor:     operations.Actor{Type: "agent-session", SessionID: "release-rollback-test"},
			Preconditions: []operations.Precondition{{
				RepositoryID:   rollbackStep.RepositoryID,
				HeadOID:        "0123456789abcdef0123456789abcdef01234567",
				ManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
				PolicyDigest:   "sha256:2222222222222222222222222222222222222222222222222222222222222222",
			}},
			Steps: []operations.Step{rollbackStep}, ApprovalClass: "local-release-installation",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if planFindings := rollbackPlan.Validate(schemas); len(planFindings) != 0 {
		t.Fatalf("rollback plan findings: %+v", planFindings)
	}
	if _, err := rollback.Apply(ctx, rollbackStep); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Verify(ctx, rollbackStep, nil); err != nil {
		t.Fatal(err)
	}

	remove := RemoveHandler{Candidate: first, ExpectedCurrent: activeTarget(first)}
	removeStep := releaseTestStep(
		RemoveReleaseAction, Parameters(first, "remove", firstRequest, activeTarget(first), nil),
	)
	if _, err := remove.Apply(ctx, removeStep); err != nil {
		t.Fatal(err)
	}
	if err := remove.Verify(ctx, removeStep, nil); err != nil {
		t.Fatal(err)
	}
	if err := second.VerifyRelease(); err != nil {
		t.Fatalf("inactive upgraded release changed during removal: %v", err)
	}
}

func TestReleaseActivationRejectsChangedActiveTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(ctx, filepath.Join(t.TempDir(), "state", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	verified, request := verifiedInstallationFixture(
		t, schemas, "1.2.3", 7, bundle.AcceptanceState{}, time.Now().UTC(),
	)
	candidate, findings := BuildInstallCandidate(
		verified, request, filepath.Join(t.TempDir(), "gds"), schemas,
	)
	if len(findings) != 0 {
		t.Fatalf("candidate findings: %+v", findings)
	}
	if err := candidate.WriteReleaseNew(); err != nil {
		t.Fatal(err)
	}
	handler := ActivationHandler{
		Candidate: candidate, ExpectedCurrent: "releases/unexpected", Operation: "install",
		Store: store, Now: time.Now,
	}
	step := releaseTestStep(
		ActivateReleaseAction,
		Parameters(candidate, "install", request, "releases/unexpected", nil),
	)
	if _, err := handler.Apply(ctx, step); err == nil {
		t.Fatal("activation accepted a changed current target")
	}
}

func TestConcurrentUpgradesAcrossStateStoresCannotOverwriteNewerActivation(t *testing.T) {
	t.Parallel()
	setupCtx := context.Background()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	installRoot := filepath.Join(t.TempDir(), "gds")
	first := materializedReleaseCandidate(
		t, schemas, installRoot, "1.0.0", 7, bundle.AcceptanceState{}, now,
	)
	if err := Activate(first, ""); err != nil {
		t.Fatal(err)
	}
	accepted := bundle.AcceptanceState{
		HighestSequence: 7,
		AcceptedDigests: map[int]string{7: first.Record.ArtifactDigest},
	}
	lower := materializedReleaseCandidate(
		t, schemas, installRoot, "1.1.0", 8, accepted, now.Add(time.Minute),
	)
	higher := materializedReleaseCandidate(
		t, schemas, installRoot, "1.2.0", 9, accepted, now.Add(2*time.Minute),
	)
	lowerStore := newReleaseAcceptanceStore(t, setupCtx)
	higherStore := newReleaseAcceptanceStore(t, setupCtx)
	putAcceptedCandidate(t, setupCtx, lowerStore, first, now)
	putAcceptedCandidate(t, setupCtx, higherStore, first, now)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocking := &blockingAcceptanceStore{
		AcceptanceStore: higherStore,
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	firstTarget := activeTarget(first)
	higherHandler := ActivationHandler{
		Candidate: higher, ExpectedCurrent: firstTarget, Operation: "upgrade",
		Store: blocking, Now: func() time.Time { return now.Add(2 * time.Minute) },
	}
	lowerHandler := ActivationHandler{
		Candidate: lower, ExpectedCurrent: firstTarget, Operation: "upgrade",
		Store: lowerStore, Now: func() time.Time { return now.Add(time.Minute) },
	}
	higherStep := releaseTestStep(
		ActivateReleaseAction, Parameters(higher, "upgrade", Request{}, firstTarget, nil),
	)
	lowerStep := releaseTestStep(
		ActivateReleaseAction, Parameters(lower, "upgrade", Request{}, firstTarget, nil),
	)
	higherResult := make(chan error, 1)
	go func() {
		_, applyErr := higherHandler.Apply(ctx, higherStep)
		higherResult <- applyErr
	}()
	select {
	case <-blocking.entered:
	case <-ctx.Done():
		t.Fatal("higher activation did not reach acceptance barrier")
	}
	lowerStarted := make(chan struct{})
	lowerResult := make(chan error, 1)
	go func() {
		close(lowerStarted)
		_, applyErr := lowerHandler.Apply(ctx, lowerStep)
		lowerResult <- applyErr
	}()
	<-lowerStarted
	select {
	case err := <-lowerResult:
		t.Fatalf("lower activation returned before the higher acceptance completed: %v", err)
	case <-time.After(5 * installScopeLockPollInterval):
	}
	close(blocking.release)
	if err := <-higherResult; err != nil {
		t.Fatalf("higher activation failed: %v", err)
	}
	if err := <-lowerResult; !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("lower activation error=%v want %v", err, ErrActivationConflict)
	}
	active, err := InspectActive(installRoot, schemas)
	if err != nil || active.CurrentTarget != activeTarget(higher) {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	higherAcceptance, err := higherStore.BundleAcceptanceState(ctx, higher.Record.TrustDomain)
	if err != nil || higherAcceptance.AcceptedDigests[9] != higher.Record.ArtifactDigest {
		t.Fatalf("higher acceptance=%+v err=%v", higherAcceptance, err)
	}
	lowerAcceptance, err := lowerStore.BundleAcceptanceState(ctx, lower.Record.TrustDomain)
	if err != nil || lowerAcceptance.AcceptedDigests[8] != "" {
		t.Fatalf("lower acceptance=%+v err=%v", lowerAcceptance, err)
	}
}

func TestActivationAcceptanceFailureRestoresAndCanBeReplanned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 13, 0, 0, 0, time.UTC)
	installRoot := filepath.Join(t.TempDir(), "gds")
	first := materializedReleaseCandidate(
		t, schemas, installRoot, "1.0.0", 7, bundle.AcceptanceState{}, now,
	)
	if err := Activate(first, ""); err != nil {
		t.Fatal(err)
	}
	store := newReleaseAcceptanceStore(t, ctx)
	putAcceptedCandidate(t, ctx, store, first, now)
	accepted, err := store.BundleAcceptanceState(ctx, first.Record.TrustDomain)
	if err != nil {
		t.Fatal(err)
	}
	second := materializedReleaseCandidate(
		t, schemas, installRoot, "1.1.0", 8, accepted, now.Add(time.Minute),
	)
	rejecting := &rejectingAcceptanceStore{
		AcceptanceStore: store,
		err:             errors.New("injected acceptance write failure"),
		reject:          true,
	}
	firstTarget := activeTarget(first)
	handler := ActivationHandler{
		Candidate: second, ExpectedCurrent: firstTarget, Operation: "upgrade",
		Store: rejecting, Now: func() time.Time { return now.Add(time.Minute) },
	}
	step := releaseTestStep(
		ActivateReleaseAction, Parameters(second, "upgrade", Request{}, firstTarget, nil),
	)
	if _, err := handler.Apply(ctx, step); !errors.Is(err, ErrActivationAcceptance) {
		t.Fatalf("activation error=%v want %v", err, ErrActivationAcceptance)
	}
	active, err := InspectActive(installRoot, schemas)
	if err != nil || active.CurrentTarget != firstTarget {
		t.Fatalf("active after rejection=%+v err=%v", active, err)
	}
	if _, findings := ValidateLifecycle("upgrade", second, nil, now.Add(2*time.Minute)); len(findings) != 0 {
		t.Fatalf("exact materialized retry findings: %+v", findings)
	}
	if err := second.WriteReleaseNew(); err != nil {
		t.Fatalf("materialization replay failed: %v", err)
	}
	rejecting.reject = false
	if _, err := handler.Apply(ctx, step); err != nil {
		t.Fatalf("activation retry failed: %v", err)
	}
	if _, err := handler.Apply(ctx, step); err != nil {
		t.Fatalf("idempotent activation replay failed: %v", err)
	}
	active, err = InspectActive(installRoot, schemas)
	if err != nil || active.CurrentTarget != activeTarget(second) {
		t.Fatalf("active after retry=%+v err=%v", active, err)
	}
}

func TestActivationReconcilesLedgerAfterPostRenameCrashWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 14, 0, 0, 0, time.UTC)
	installRoot := filepath.Join(t.TempDir(), "gds")
	first := materializedReleaseCandidate(
		t, schemas, installRoot, "1.0.0", 7, bundle.AcceptanceState{}, now,
	)
	if err := Activate(first, ""); err != nil {
		t.Fatal(err)
	}
	store := newReleaseAcceptanceStore(t, ctx)
	putAcceptedCandidate(t, ctx, store, first, now)
	accepted, err := store.BundleAcceptanceState(ctx, first.Record.TrustDomain)
	if err != nil {
		t.Fatal(err)
	}
	second := materializedReleaseCandidate(
		t, schemas, installRoot, "1.1.0", 8, accepted, now.Add(time.Minute),
	)
	firstTarget := activeTarget(first)
	if err := Activate(second, firstTarget); err != nil {
		t.Fatal(err)
	}
	if _, findings := ValidateLifecycle("upgrade", second, nil, now.Add(2*time.Minute)); len(findings) != 0 {
		t.Fatalf("crash-window reconciliation findings: %+v", findings)
	}
	handler := ActivationHandler{
		Candidate: second, ExpectedCurrent: firstTarget, Operation: "upgrade",
		Store: store, Now: func() time.Time { return now.Add(2 * time.Minute) },
	}
	step := releaseTestStep(
		ActivateReleaseAction, Parameters(second, "upgrade", Request{}, firstTarget, nil),
	)
	if _, err := handler.Apply(ctx, step); err != nil {
		t.Fatalf("ledger reconciliation failed: %v", err)
	}
	reconciled, err := store.BundleAcceptanceState(ctx, second.Record.TrustDomain)
	if err != nil || reconciled.AcceptedDigests[8] != second.Record.ArtifactDigest {
		t.Fatalf("reconciled acceptance=%+v err=%v", reconciled, err)
	}
}

func TestReleaseInstallRunsThroughDurablePlanApplyVerifyEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(ctx, filepath.Join(t.TempDir(), "state", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	verified, request := verifiedInstallationFixture(
		t, schemas, "1.2.3", 7, bundle.AcceptanceState{}, now,
	)
	candidate, findings := BuildInstallCandidate(
		verified, request, filepath.Join(t.TempDir(), "gds"), schemas,
	)
	if len(findings) != 0 {
		t.Fatalf("candidate findings: %+v", findings)
	}
	repositoryID := "repo_01JEXAMPZ0000000000000000C"
	precondition := operations.Precondition{
		RepositoryID:   repositoryID,
		HeadOID:        "0123456789abcdef0123456789abcdef01234567",
		ManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		PolicyDigest:   "sha256:2222222222222222222222222222222222222222222222222222222222222222",
	}
	parameters := Parameters(candidate, "install", request, "", nil)
	plan, err := operations.NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGV", now, now.Add(15*time.Minute),
		operations.PlanInput{
			Operation:     "release-install",
			Actor:         operations.Actor{Type: "agent-session", SessionID: "release-engine-test"},
			Preconditions: []operations.Precondition{precondition},
			Steps: []operations.Step{
				{
					StepID: "materialize-release", RepositoryID: repositoryID,
					Action: MaterializeReleaseAction, RequiresApproval: true,
					Compensation: operations.Compensation{Mode: "manual"}, Parameters: parameters,
				},
				{
					StepID: "activate-release", RepositoryID: repositoryID,
					Action: ActivateReleaseAction, RequiresApproval: true,
					Compensation: operations.Compensation{Mode: "manual"}, Parameters: parameters,
				},
			},
			ApprovalClass: "local-release-installation",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	checker := releaseStaticChecker{observation: operations.Observation{
		RepositoryID: repositoryID, HeadOID: precondition.HeadOID,
		ManifestDigest: precondition.ManifestDigest, PolicyDigest: precondition.PolicyDigest,
	}}
	engine := operations.NewDefaultEngine(
		store, schemas, checker,
		map[string]operations.ActionHandler{
			MaterializeReleaseAction: MaterializeHandler{Candidate: candidate},
			ActivateReleaseAction: ActivationHandler{
				Candidate: candidate, Operation: "install", Store: store,
				Now: func() time.Time { return now.Add(time.Minute) },
			},
		},
		"device:test", "release-engine-test",
	)
	engine.Now = func() time.Time { return now.Add(time.Minute) }
	// This test owns release materialization/activation behavior; the normative
	// signed approval and separate enablement path is covered in core/operations.
	engine.RequireSignedApprovals = false
	if err := engine.PutPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Apply(ctx, plan.PlanID, "approval:owner:release-install")
	if err != nil || !result.MutationCompleted || result.OperationID == "" {
		t.Fatalf("apply result=%+v err=%v", result, err)
	}
	verifiedResult, err := engine.Verify(ctx, result.OperationID)
	if err != nil || verifiedResult.Status != "verified" || verifiedResult.Steps != 2 {
		t.Fatalf("verify result=%+v err=%v", verifiedResult, err)
	}
}

type releaseStaticChecker struct {
	observation operations.Observation
}

func (checker releaseStaticChecker) Observe(context.Context, string) (operations.Observation, error) {
	return checker.observation, nil
}

func releaseTestStep(action string, parameters map[string]any) operations.Step {
	return operations.Step{
		StepID: "release-step", RepositoryID: "repo_01JEXAMPZ0000000000000000C",
		Action: action, RequiresApproval: true,
		Compensation: operations.Compensation{Mode: "manual"}, Parameters: parameters,
	}
}

func activeTarget(candidate InstallCandidate) string {
	return filepath.ToSlash(filepath.Join(releasesName, candidate.Record.ReleaseKey))
}

func materializedReleaseCandidate(
	t *testing.T,
	schemas *validation.Set,
	installRoot string,
	version string,
	sequence int,
	acceptance bundle.AcceptanceState,
	now time.Time,
) InstallCandidate {
	t.Helper()
	verified, request := verifiedInstallationFixture(t, schemas, version, sequence, acceptance, now)
	candidate, findings := BuildInstallCandidate(verified, request, installRoot, schemas)
	if len(findings) != 0 {
		t.Fatalf("candidate findings: %+v", findings)
	}
	if err := candidate.WriteReleaseNew(); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func newReleaseAcceptanceStore(t *testing.T, ctx context.Context) *state.Store {
	t.Helper()
	store, err := state.Initialize(ctx, filepath.Join(t.TempDir(), "state", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func putAcceptedCandidate(
	t *testing.T,
	ctx context.Context,
	store *state.Store,
	candidate InstallCandidate,
	now time.Time,
) {
	t.Helper()
	if err := store.PutAcceptedBundle(ctx, state.AcceptedBundle{
		TrustDomain:               candidate.Record.TrustDomain,
		ReleaseSequence:           candidate.Record.ReleaseSequence,
		BundleVersion:             candidate.Record.BundleVersion,
		ArtifactDigest:            candidate.Record.ArtifactDigest,
		ManifestDigest:            candidate.Record.ManifestDigest,
		AttestationIdentityDigest: candidate.Record.AttestationIdentityDigest,
		AcceptedAt:                now,
	}, nil, now); err != nil {
		t.Fatal(err)
	}
}

type blockingAcceptanceStore struct {
	AcceptanceStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (store *blockingAcceptanceStore) PutAcceptedBundle(
	ctx context.Context,
	accepted state.AcceptedBundle,
	authorization *bundle.RollbackAuthorization,
	now time.Time,
) error {
	store.once.Do(func() { close(store.entered) })
	select {
	case <-store.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return store.AcceptanceStore.PutAcceptedBundle(ctx, accepted, authorization, now)
}

type rejectingAcceptanceStore struct {
	AcceptanceStore
	err    error
	reject bool
}

func (store *rejectingAcceptanceStore) PutAcceptedBundle(
	ctx context.Context,
	accepted state.AcceptedBundle,
	authorization *bundle.RollbackAuthorization,
	now time.Time,
) error {
	if store.reject {
		return store.err
	}
	return store.AcceptanceStore.PutAcceptedBundle(ctx, accepted, authorization, now)
}
