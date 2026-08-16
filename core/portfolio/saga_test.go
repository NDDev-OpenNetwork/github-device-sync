package portfolio

import (
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

func TestCompileSagaOrdersProducerBeforeConsumer(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	pre := func(id string) operations.Precondition {
		return operations.Precondition{RepositoryID: id,
			HeadOID:        "0123456789abcdef0123456789abcdef01234567",
			ManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			PolicyDigest:   "sha256:2222222222222222222222222222222222222222222222222222222222222222"}
	}
	step := func(id, repo string) operations.Step {
		return operations.Step{StepID: id, RepositoryID: repo, Action: "fixture-action",
			RequiresApproval: true, WriteSet: []string{"repository"}, Compensation: operations.Compensation{Mode: "manual"}}
	}
	plan, err := CompileSaga(SagaInput{PlanID: "plan_01KX7BV07RHD6KRA4Z4J0KCHGX", Operation: "portfolio-change", TaskID: "task-1",
		ApprovalClass: "repository-write", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Actor: operations.Actor{Type: "agent", SessionID: "session:test"},
		Nodes: []SagaNode{{RepositoryID: "repo-consumer", DependsOn: []string{"repo-producer"}, Precondition: pre("repo-consumer"), Steps: []operations.Step{step("step-consumer", "repo-consumer")}},
			{RepositoryID: "repo-producer", Precondition: pre("repo-producer"), Steps: []operations.Step{step("step-producer", "repo-producer")}}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0].RepositoryID != "repo-producer" || plan.Steps[1].RepositoryID != "repo-consumer" {
		t.Fatalf("dependency order not preserved: %+v", plan.Steps)
	}
}
