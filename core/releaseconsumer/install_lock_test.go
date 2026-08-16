//go:build darwin || linux

package releaseconsumer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestInstallScopeLockRejectsSymlink(t *testing.T) {
	t.Parallel()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	verified, request := verifiedInstallationFixture(
		t, schemas, "1.2.3", 7, bundle.AcceptanceState{}, testNow(),
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
	external := filepath.Join(t.TempDir(), "external-lock")
	if err := os.WriteFile(external, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(candidate.InstallRoot, installScopeLockName)); err != nil {
		t.Fatal(err)
	}
	if err := Activate(candidate, ""); !errors.Is(err, ErrInstallScopeLock) {
		t.Fatalf("activation error=%v want %v", err, ErrInstallScopeLock)
	}
	raw, err := os.ReadFile(external)
	if err != nil || string(raw) != "unchanged\n" {
		t.Fatalf("external lock target changed: %q err=%v", raw, err)
	}
	active, err := InspectActive(candidate.InstallRoot, schemas)
	if err != nil || active.CurrentTarget != "" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}

func TestInstallScopeLockReleasedAfterProcessExit(t *testing.T) {
	if root := os.Getenv("GDS_TEST_CRASH_LOCK_ROOT"); root != "" {
		candidate := InstallCandidate{
			InstallRoot: root,
			Record:      InstallRecord{TrustDomain: "gds-release"},
		}
		_ = withInstallScopeLock(context.Background(), candidate, func() error {
			os.Exit(23)
			return nil
		})
		os.Exit(24)
	}
	root, err := canonicalInstallRoot(filepath.Join(t.TempDir(), "gds"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestInstallScopeLockReleasedAfterProcessExit$")
	command.Env = append(os.Environ(), "GDS_TEST_CRASH_LOCK_ROOT="+root)
	err = command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("crash helper err=%v", err)
	}
	candidate := InstallCandidate{
		InstallRoot: root,
		Record:      InstallRecord{TrustDomain: "gds-release"},
	}
	acquired := false
	if err := withInstallScopeLock(context.Background(), candidate, func() error {
		acquired = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("lock was not acquired after holder process exited")
	}
}

func testNow() time.Time {
	return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
}
