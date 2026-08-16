package releasebuilder

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestCanonicalVerifierTrustCoversEveryReleaseTarget(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	trust, err := bundle.LoadTrust(root, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateVerifierTargetCoverage(trust); err != nil {
		t.Fatal(err)
	}
}

func TestVerifierTrustRejectsMissingAndDuplicateTargets(t *testing.T) {
	t.Parallel()
	trust := completeVerifierTrustFixture()
	trust.Verification.Verifier.Executables = trust.Verification.Verifier.Executables[:3]
	if err := validateVerifierTargetCoverage(trust); err == nil || !strings.Contains(err.Error(), "covers 3") {
		t.Fatalf("missing target error = %v", err)
	}

	trust = completeVerifierTrustFixture()
	trust.Verification.Verifier.Executables[3] = trust.Verification.Verifier.Executables[0]
	if err := validateVerifierTargetCoverage(trust); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate target error = %v", err)
	}
}

func completeVerifierTrustFixture() bundle.TrustPolicy {
	trust := bundle.TrustPolicy{}
	for _, target := range defaultTargets() {
		trust.Verification.Verifier.Executables = append(
			trust.Verification.Verifier.Executables,
			bundle.TrustVerifierExecutable{
				OS: target.OS, Arch: target.Arch, Digest: "sha256:" + strings.Repeat("f", 64),
			},
		)
	}
	return trust
}
