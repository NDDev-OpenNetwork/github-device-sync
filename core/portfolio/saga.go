package portfolio

import (
	"fmt"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

// SagaNode is the executable counterpart of an observational Subplan. Nodes
// compile into one immutable operations plan, so the normal exact approval,
// complete write-set locking, ordered journal and compensation contracts apply
// across repository boundaries.
type SagaNode struct {
	RepositoryID string
	DependsOn    []string
	Precondition operations.Precondition
	Steps        []operations.Step
}

type SagaInput struct {
	PlanID, Operation, TaskID, ApprovalClass string
	CreatedAt, ExpiresAt                     time.Time
	Actor                                    operations.Actor
	Nodes                                    []SagaNode
}

func CompileSaga(input SagaInput) (operations.Plan, error) {
	subplans := make([]Subplan, 0, len(input.Nodes))
	byID := make(map[string]SagaNode, len(input.Nodes))
	for _, node := range input.Nodes {
		if node.RepositoryID == "" || node.Precondition.RepositoryID != node.RepositoryID || len(node.Steps) == 0 {
			return operations.Plan{}, fmt.Errorf("saga node requires matching repository precondition and at least one step")
		}
		byID[node.RepositoryID] = node
		subplans = append(subplans, Subplan{RepositoryID: node.RepositoryID, DependsOn: node.DependsOn})
	}
	ordered, err := dependencyOrder(subplans)
	if err != nil {
		return operations.Plan{}, err
	}
	preconditions := make([]operations.Precondition, 0, len(ordered))
	steps := []operations.Step{}
	for _, item := range ordered {
		node := byID[item.RepositoryID]
		preconditions = append(preconditions, node.Precondition)
		for _, step := range node.Steps {
			if step.RepositoryID != node.RepositoryID {
				return operations.Plan{}, fmt.Errorf("saga step repository does not match its node")
			}
			steps = append(steps, step)
		}
	}
	return operations.NewPlan(input.PlanID, input.CreatedAt, input.ExpiresAt, operations.PlanInput{
		Operation: input.Operation, Actor: input.Actor, TaskID: input.TaskID,
		Preconditions: preconditions, Steps: steps, ApprovalClass: input.ApprovalClass,
	})
}
