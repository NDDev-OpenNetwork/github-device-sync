package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func gitFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, arguments := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "GDS Test"},
		{"config", "user.email", "gds@example.invalid"},
	} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "fixture.txt"}, {"commit", "-qm", "fixture"}} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(string(output))
}

type fixtureChecker struct {
	observation operations.Observation
}

func (checker fixtureChecker) Observe(
	context.Context,
	string,
) (operations.Observation, error) {
	return checker.observation, nil
}

func TestRecoveryRefHandlerRequiresOperationPlanApprovalAndVerify(t *testing.T) {
	root, head := gitFixture(t)
	runner, err := gitprovider.NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewRecoveryRefHandler(root, runner)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(stateRoot, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	repositoryID := "repo_01JEXAMPZ0000000000000000C"
	observation := operations.Observation{
		RepositoryID: repositoryID, HeadOID: head,
		ManifestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PolicyDigest:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	plan, err := operations.NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGZ", now, now.Add(time.Minute),
		operations.PlanInput{
			Operation: "materialize-recovery-ref",
			Actor:     operations.Actor{Type: "agent-session", SessionID: "fixture-session"},
			Preconditions: []operations.Precondition{{
				RepositoryID: repositoryID, HeadOID: head,
				ManifestDigest: observation.ManifestDigest, PolicyDigest: observation.PolicyDigest,
			}},
			Steps: []operations.Step{{
				StepID: "materialize-recovery-ref", RepositoryID: repositoryID,
				Action: MaterializeRecoveryRefAction, RequiresApproval: true,
				Compensation: operations.Compensation{Mode: "manual"},
				Parameters: map[string]any{"recovery_ref": map[string]any{
					"reference": "refs/gds/recovery/operation-1",
					"new_oid":   head, "expected_old_oid": strings.Repeat("0", 40),
				}},
			}},
			ApprovalClass: "local-recovery-ref-write",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := operations.NewDefaultEngine(
		store, schemas, fixtureChecker{observation: observation},
		map[string]operations.ActionHandler{MaterializeRecoveryRefAction: handler},
		"device:fixture", "fixture-session",
	)
	engine.Now = func() time.Time { return now.Add(10 * time.Second) }
	// This handler-focused unit test exercises the legacy in-memory engine
	// adapter; signed approval/enablement is covered by core/operations.
	engine.RequireSignedApprovals = false
	sequence := 0
	engine.NewID = func(prefix string, _ time.Time) (string, error) {
		sequence++
		return prefix + "_recovery_ref_" + string(rune('0'+sequence)), nil
	}
	if err := engine.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Apply(context.Background(), plan.PlanID, ""); err == nil {
		t.Fatal("unapproved operation reached the Git handler")
	}
	missing, err := runner.ObserveRecoveryRef(
		context.Background(), root, "refs/gds/recovery/operation-1",
	)
	if err != nil || missing != strings.Repeat("0", 40) {
		t.Fatalf("unapproved ref=%q err=%v", missing, err)
	}
	result, err := engine.Apply(context.Background(), plan.PlanID, "approval:owner:recovery-ref")
	if err != nil || !result.MutationCompleted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := engine.Verify(context.Background(), result.OperationID); err != nil {
		t.Fatal(err)
	}
	observed, err := runner.ObserveRecoveryRef(
		context.Background(), root, "refs/gds/recovery/operation-1",
	)
	if err != nil || observed != head {
		t.Fatalf("recovery ref=%q err=%v", observed, err)
	}
}
