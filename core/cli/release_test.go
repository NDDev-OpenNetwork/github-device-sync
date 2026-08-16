package cli

import "testing"

func TestReleaseLifecycleRequiresOneExactMode(t *testing.T) {
	t.Parallel()
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "release", "install", "--plan", "--apply", "plan_01KX7BV07RHD6KRA4Z4J0KCHGV",
	)
	if exitCode != 4 || stderr != "" ||
		!containsFinding(envelope.Findings, "GDS_RELEASE_LIFECYCLE_MODE_REQUIRED") {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestReleaseStoredVerificationRejectsIdentityOverrides(t *testing.T) {
	t.Parallel()
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "release", "install",
		"--verify", "op_01KX7BV07RHD6KRA4Z4J0KCHGV",
		"--install-root", t.TempDir(),
	)
	if exitCode != 4 || stderr != "" ||
		!containsFinding(envelope.Findings, "GDS_RELEASE_VERIFY_INPUT_CONFLICT") {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestReleaseEvidenceVerificationRequiresExactInputs(t *testing.T) {
	t.Parallel()
	exitCode, envelope, stderr := executeJSON(t, "--json", "release", "verify")
	if exitCode != 4 || stderr != "" ||
		!containsFinding(envelope.Findings, "GDS_RELEASE_EVIDENCE_INPUT_REQUIRED") {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestReleaseScopeRequiresIndependentTrustAndInstallRoot(t *testing.T) {
	t.Parallel()
	exitCode, envelope, stderr := executeJSON(t, "--json", "release", "scope")
	if exitCode != 4 || stderr != "" ||
		!containsFinding(envelope.Findings, "GDS_RELEASE_SCOPE_INPUT_REQUIRED") {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	assertEnvelopeSchema(t, envelope)
}
