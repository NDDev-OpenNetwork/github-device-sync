package cli

import (
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func TestGitHubInventoryRequiresRuntimeEvidenceWithoutAttemptingMutation(t *testing.T) {
	root := repositoryRoot(t)
	missing := filepath.Join(t.TempDir(), "github-runtime.yaml")
	exitCode, envelope, stderr := executeJSON(
		t,
		"--json", "--cwd", root,
		"github", "inventory",
		"--installation", "installation:github-personal",
		"--runtime-config", missing,
	)
	if exitCode != 3 || envelope.ExitClass != domain.ExitNotProven || stderr != "" {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	if !containsFinding(envelope.Findings, "GDS_GITHUB_RUNTIME_NOT_PROVEN") ||
		envelope.Mutation.Attempted || envelope.Mutation.Completed {
		t.Fatalf("envelope=%#v", envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestGitHubGovernanceRequiresExactRepositoryScopeBeforeRuntimeAccess(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root,
		"github", "governance",
		"--installation", "installation:github-personal",
	)
	if exitCode != 4 || envelope.ExitClass != domain.ExitInput || stderr != "" {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	if !containsFinding(envelope.Findings, "GDS_GITHUB_GOVERNANCE_SCOPE_REQUIRED") ||
		envelope.Mutation.Attempted || envelope.Mutation.Completed {
		t.Fatalf("envelope=%#v", envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestGitHubGovernanceRejectsConflictingOperationModesBeforeRuntimeAccess(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root,
		"github", "governance", "--plan", "--apply", "plan_01KX7PNHB7DFRJ36HK7G12E6PF",
	)
	if exitCode != 4 || envelope.ExitClass != domain.ExitInput || stderr != "" ||
		!containsFinding(envelope.Findings, "GDS_GITHUB_GOVERNANCE_MODE_CONFLICT") ||
		envelope.Mutation.Attempted {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestGitHubGovernancePlanRequiresCanonicalActorBeforeRuntimeAccess(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root,
		"github", "governance", "--plan",
		"--installation", "installation:github-personal",
		"--owner", "example-user", "--repository", "github-device-sync",
	)
	if exitCode != 4 || envelope.ExitClass != domain.ExitInput || stderr != "" ||
		!containsFinding(envelope.Findings, "GDS_DEVICE_ID_INVALID") || envelope.Mutation.Attempted {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestGitHubProjectionPRRequiresOneExplicitOperationMode(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "github", "projection-pr",
	)
	if exitCode != 4 || envelope.ExitClass != domain.ExitInput || stderr != "" ||
		!containsFinding(envelope.Findings, "GDS_GITHUB_PROJECTION_MODE_REQUIRED") ||
		envelope.Mutation.Attempted {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestGitHubProjectionPRRejectsMutationRuntimeOutsideApply(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "github", "projection-pr", "--plan",
		"--mutation-runtime-config", filepath.Join(t.TempDir(), "mutation-runtime.yaml"),
	)
	if exitCode != 4 || envelope.ExitClass != domain.ExitInput || stderr != "" ||
		!containsFinding(envelope.Findings, "GDS_GITHUB_PROJECTION_INPUT_CONFLICT") ||
		envelope.Mutation.Attempted {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestReconcileRequiresExplicitReadOnlyPlanMode(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t, "--json", "--cwd", root, "reconcile",
	)
	if exitCode != 4 || envelope.ExitClass != domain.ExitInput || stderr != "" {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	if !containsFinding(envelope.Findings, "GDS_RECONCILE_PLAN_REQUIRED") ||
		envelope.Mutation.Attempted || envelope.Mutation.Completed {
		t.Fatalf("envelope=%#v", envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestReconcilePlanReportsMissingRuntimeAsNotProven(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t,
		"--json", "--cwd", root,
		"reconcile", "--plan",
		"--runtime-config", filepath.Join(t.TempDir(), "missing.yaml"),
	)
	if exitCode != 3 || envelope.ExitClass != domain.ExitNotProven || stderr != "" {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	if !containsFinding(envelope.Findings, "GDS_GITHUB_RUNTIME_NOT_PROVEN") ||
		envelope.Mutation.Attempted || envelope.Mutation.Completed {
		t.Fatalf("envelope=%#v", envelope)
	}
	assertEnvelopeSchema(t, envelope)
}

func TestEstateSummaryReportRequiresRuntimeEvidence(t *testing.T) {
	root := repositoryRoot(t)
	exitCode, envelope, stderr := executeJSON(
		t,
		"--json", "--cwd", root,
		"report", "estate-summary",
		"--runtime-config", filepath.Join(t.TempDir(), "missing.yaml"),
	)
	if exitCode != 3 || envelope.ExitClass != domain.ExitNotProven || stderr != "" {
		t.Fatalf("exit=%d stderr=%q envelope=%#v", exitCode, stderr, envelope)
	}
	if envelope.Command != "gds report estate-summary" ||
		!containsFinding(envelope.Findings, "GDS_GITHUB_RUNTIME_NOT_PROVEN") ||
		envelope.Mutation.Attempted || envelope.Mutation.Completed {
		t.Fatalf("envelope=%#v", envelope)
	}
	assertEnvelopeSchema(t, envelope)
}
