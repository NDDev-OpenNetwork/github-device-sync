package releaseconsumer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestInstallUpgradeRollbackAndRemoveLifecycle(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	firstVerified, firstRequest := verifiedInstallationFixture(
		t, schemas, "1.2.3", 7, bundle.AcceptanceState{}, now,
	)
	installRoot := filepath.Join(t.TempDir(), "gds")
	first, findings := BuildInstallCandidate(firstVerified, firstRequest, installRoot, schemas)
	if len(findings) != 0 {
		t.Fatalf("first candidate findings: %+v", findings)
	}
	if err := first.WriteReleaseNew(); err != nil {
		t.Fatal(err)
	}
	if err := Activate(first, ""); err != nil {
		t.Fatal(err)
	}
	firstTarget := filepath.ToSlash(filepath.Join(releasesName, first.Record.ReleaseKey))
	active, err := InspectActive(installRoot, schemas)
	if err != nil || active.Record == nil || active.CurrentTarget != firstTarget {
		t.Fatalf("active=%+v err=%v", active, err)
	}

	secondState := bundle.AcceptanceState{
		HighestSequence: 7,
		AcceptedDigests: map[int]string{7: first.Record.ArtifactDigest},
	}
	secondVerified, secondRequest := verifiedInstallationFixture(
		t, schemas, "1.3.0", 8, secondState, now.Add(time.Minute),
	)
	second, findings := BuildInstallCandidate(secondVerified, secondRequest, installRoot, schemas)
	if len(findings) != 0 {
		t.Fatalf("second candidate findings: %+v", findings)
	}
	if err := second.WriteReleaseNew(); err != nil {
		t.Fatal(err)
	}
	if err := Activate(second, firstTarget); err != nil {
		t.Fatal(err)
	}
	secondTarget := filepath.ToSlash(filepath.Join(releasesName, second.Record.ReleaseKey))
	active, err = InspectActive(installRoot, schemas)
	if err != nil || active.CurrentTarget != secondTarget {
		t.Fatalf("upgrade active=%+v err=%v", active, err)
	}

	if err := Activate(first, secondTarget); err != nil {
		t.Fatal(err)
	}
	active, err = InspectActive(installRoot, schemas)
	if err != nil || active.CurrentTarget != firstTarget {
		t.Fatalf("rollback active=%+v err=%v", active, err)
	}

	if err := RemoveActive(first, firstTarget); err != nil {
		t.Fatal(err)
	}
	active, err = InspectActive(installRoot, schemas)
	if err != nil || active.CurrentTarget != "" || active.Record != nil {
		t.Fatalf("remove active=%+v err=%v", active, err)
	}
	if _, err := os.Lstat(first.ReleasePath); !os.IsNotExist(err) {
		t.Fatalf("removed release still exists: %v", err)
	}
	if err := second.VerifyRelease(); err != nil {
		t.Fatalf("inactive prior release was damaged: %v", err)
	}
}

func TestInstalledReleaseTamperBlocksActivation(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(
		filepath.Join(candidate.ReleasePath, "consumer-trust.yaml"), []byte("tampered\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := Activate(candidate, ""); err == nil {
		t.Fatal("tampered release was activated")
	}
}

func TestActivationRestoresPreviousPointerWhenCandidateChangesAfterRename(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
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
	err = withInstallScopeLock(context.Background(), candidate, func() error {
		return activateWhileLockedWithPostRenameHook(candidate, "", func() {
			if writeErr := os.WriteFile(
				filepath.Join(candidate.ReleasePath, "consumer-trust.yaml"), []byte("tampered\n"), 0o644,
			); writeErr != nil {
				t.Errorf("tamper candidate: %v", writeErr)
			}
		})
	})
	if !errors.Is(err, ErrActivationIntegrity) {
		t.Fatalf("activation error = %v, want %v", err, ErrActivationIntegrity)
	}
	active, inspectErr := InspectActive(candidate.InstallRoot, schemas)
	if inspectErr != nil || active.CurrentTarget != "" || active.Record != nil {
		t.Fatalf("active=%+v err=%v", active, inspectErr)
	}
}

func TestMaterializationAndActivationAreIdempotentForExactCandidate(t *testing.T) {
	t.Parallel()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
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
	if err := candidate.WriteReleaseNew(); err != nil {
		t.Fatalf("exact materialization replay failed: %v", err)
	}
	if err := Activate(candidate, ""); err != nil {
		t.Fatal(err)
	}
	if err := Activate(candidate, ""); err != nil {
		t.Fatalf("exact activation replay failed: %v", err)
	}
	if _, findings := ValidateLifecycle("install", candidate, nil, time.Now().UTC()); len(findings) != 0 {
		t.Fatalf("exact install reconciliation findings: %+v", findings)
	}
	active, err := InspectActive(candidate.InstallRoot, schemas)
	if err != nil || active.Record == nil || active.Record.CandidateDigest != candidate.Record.CandidateDigest {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}

func TestInstalledReleaseRejectsSymlinkedRecord(t *testing.T) {
	t.Parallel()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
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
	recordPath := filepath.Join(candidate.ReleasePath, installRecordName)
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), installRecordName)
	if err := os.WriteFile(external, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, recordPath); err != nil {
		t.Fatal(err)
	}
	if err := candidate.VerifyRelease(); err == nil {
		t.Fatal("symlinked installation record was accepted")
	}
}

func TestInstallationRootCanonicalizesParentSymlink(t *testing.T) {
	t.Parallel()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	verified, request := verifiedInstallationFixture(
		t, schemas, "1.2.3", 7, bundle.AcceptanceState{}, time.Now().UTC(),
	)
	realParent := t.TempDir()
	aliasParent := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatal(err)
	}
	aliasedRoot := filepath.Join(aliasParent, "gds")
	candidate, findings := BuildInstallCandidate(verified, request, aliasedRoot, schemas)
	if len(findings) != 0 {
		t.Fatalf("candidate findings: %+v", findings)
	}
	resolvedParent, err := filepath.EvalSymlinks(realParent)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(resolvedParent, "gds")
	if candidate.InstallRoot != expected {
		t.Fatalf("install root=%q want %q", candidate.InstallRoot, expected)
	}
	aliasDigest, err := InstallScopeDigest(aliasedRoot, candidate.Record.TrustDomain)
	if err != nil {
		t.Fatal(err)
	}
	realDigest, err := InstallScopeDigest(expected, candidate.Record.TrustDomain)
	if err != nil || aliasDigest != realDigest {
		t.Fatalf("alias digest=%q real digest=%q err=%v", aliasDigest, realDigest, err)
	}
}

func TestInstallCandidateUsesVerifiedSnapshotAfterExternalEvidenceDrift(t *testing.T) {
	t.Parallel()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	verified, request := verifiedInstallationFixture(
		t, schemas, "1.2.3", 7, bundle.AcceptanceState{}, time.Now().UTC(),
	)
	if err := os.WriteFile(
		filepath.Join(request.EvidenceDirectory, ProvenanceBundleName), []byte("changed\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate, findings := BuildInstallCandidate(
		verified, request, filepath.Join(t.TempDir(), "gds"), schemas,
	)
	if len(findings) != 0 || len(candidate.Files) == 0 {
		t.Fatalf("candidate=%+v findings=%+v", candidate.Record, findings)
	}
}

func verifiedInstallationFixture(
	t *testing.T,
	schemas *validation.Set,
	version string,
	sequence int,
	state bundle.AcceptanceState,
	now time.Time,
) (VerifiedRelease, Request) {
	t.Helper()
	release, evidence, trust := writeVerificationFixtureVersion(t, schemas, version, sequence)
	request := Request{
		ReleaseDirectory: release, EvidenceDirectory: evidence, TrustPolicyPath: trust,
		ConsumerVersion: "1.0.0", TargetOS: "linux", TargetArch: "amd64",
	}
	verified, findings := (Verifier{
		Schemas: schemas, Attestations: staticAttestationVerifier{},
	}).Verify(context.Background(), request, state, nil, now)
	if len(findings) != 0 {
		t.Fatalf("verification findings: %+v", findings)
	}
	return verified, request
}
