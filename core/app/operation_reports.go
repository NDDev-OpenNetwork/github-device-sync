package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type OperationInspectionData struct {
	StatePath        string                  `json:"state_path"`
	Plan             operations.Plan         `json:"plan"`
	Operation        state.OperationRecord   `json:"operation"`
	Steps            []state.StepRecord      `json:"steps"`
	Events           []state.EventRecord     `json:"events"`
	Locks            []state.Lock            `json:"locks"`
	KillSwitches     operations.KillSwitches `json:"kill_switches"`
	JournalIntegrity string                  `json:"journal_integrity"`
	Recovery         RecoveryAssessment      `json:"recovery"`
}

type RecoveryAssessment struct {
	Classification  string   `json:"classification"`
	SafeNextActions []string `json:"safe_next_actions"`
	BlockedActions  []string `json:"blocked_actions"`
}

func (services *Services) InspectOperation(
	ctx context.Context,
	operationID string,
	requestedStatePath string,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return domain.NewEnvelope("gds operation inspect", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_OPERATION_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "An exact operation id is required.",
		})
	}
	statePath, err := resolveStatePath(requestedStatePath)
	if err != nil {
		return envelopeForError("gds operation inspect", requestedStatePath, err)
	}
	store, err := state.OpenReadOnly(ctx, statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") {
			return domain.NewEnvelope(
				"gds operation inspect", domain.ExitNotProven,
				map[string]any{"state_path": statePath}, domain.Finding{
					Code: "GDS_STATE_NOT_INITIALIZED", Severity: domain.SeverityInfo,
					Message: "The local GDS state database does not exist.",
				},
			)
		}
		return envelopeForError("gds operation inspect", statePath, err)
	}
	defer store.Close()
	operation, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return operationStateError("gds operation inspect", operationID, err)
	}
	planRecord, err := store.GetPlan(ctx, operation.PlanID)
	if err != nil {
		return operationStateError("gds operation inspect", operationID, err)
	}
	plan, err := operations.DecodePlan(planRecord.Body)
	if err != nil {
		return operationStateError("gds operation inspect", operationID, err)
	}
	steps, err := store.ListSteps(ctx, operationID)
	if err != nil {
		return operationStateError("gds operation inspect", operationID, err)
	}
	events, err := store.ListEvents(ctx, operationID)
	if err != nil {
		return operationStateError("gds operation inspect", operationID, err)
	}
	locks, err := store.ListLocksByOperation(ctx, operationID)
	if err != nil {
		return operationStateError("gds operation inspect", operationID, err)
	}
	killSwitches, killSwitchError := operations.LoadKillSwitches(os.LookupEnv)
	findings := inspectOperationIntegrity(
		planRecord, plan, operation, steps, events, services,
	)
	if killSwitchError != nil {
		findings = append(findings, domain.Finding{
			Code: "GDS_KILL_SWITCH_INVALID", Severity: domain.SeverityCritical,
			Message:  "A kill-switch value is invalid; mutation commands fail closed.",
			Evidence: map[string]any{"error": killSwitchError.Error()},
		})
	}
	data := OperationInspectionData{
		StatePath: statePath, Plan: plan, Operation: operation, Steps: steps,
		Events: events, Locks: locks, KillSwitches: killSwitches,
		JournalIntegrity: "pass", Recovery: assessRecovery(operation, steps, locks),
	}
	if len(findings) != 0 {
		data.JournalIntegrity = "failed"
	}
	envelope := domain.NewEnvelope(
		"gds operation inspect", classifyFindings(findings), data, findings...,
	)
	envelope.OperationID = operationID
	envelope.Scope["plan_id"] = operation.PlanID
	return envelope
}

func inspectOperationIntegrity(
	record state.PlanRecord,
	plan operations.Plan,
	operation state.OperationRecord,
	steps []state.StepRecord,
	events []state.EventRecord,
	services *Services,
) []domain.Finding {
	findings := plan.Validate(services.Schemas)
	if record.PlanDigest != plan.PlanDigest || operation.PlanID != plan.PlanID ||
		operation.Operation != plan.Operation {
		findings = append(findings, domain.Finding{
			Code: "GDS_OPERATION_PLAN_INTEGRITY_FAILED", Severity: domain.SeverityCritical,
			Message: "Stored operation and immutable plan identities do not agree.",
		})
	}
	if len(steps) != len(plan.Steps) {
		findings = append(findings, domain.Finding{
			Code: "GDS_OPERATION_STEP_SET_INVALID", Severity: domain.SeverityCritical,
			Message: "Stored operation step count differs from the immutable plan.",
		})
	} else {
		for index, recorded := range steps {
			expected := plan.Steps[index]
			expectedKey, keyErr := operations.StepIdempotencyKey(plan, expected)
			if recorded.Sequence != index || recorded.StepID != expected.StepID ||
				recorded.RepositoryID != expected.RepositoryID || recorded.Action != expected.Action ||
				keyErr != nil ||
				(!strings.HasPrefix(recorded.IdempotencyKey, "legacy:") &&
					recorded.IdempotencyKey != expectedKey) {
				findings = append(findings, domain.Finding{
					Code: "GDS_OPERATION_STEP_SET_INVALID", Severity: domain.SeverityCritical,
					Message:  "Stored operation steps differ from the immutable plan.",
					Evidence: map[string]any{"sequence": index},
				})
			}
		}
	}
	for _, event := range events {
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(event.Payload))
		if event.OperationID != operation.OperationID || event.PlanID != plan.PlanID ||
			event.PayloadDigest != digest || !json.Valid(event.Payload) {
			findings = append(findings, domain.Finding{
				Code: "GDS_OPERATION_EVENT_INTEGRITY_FAILED", Severity: domain.SeverityCritical,
				Message:  "An append-only journal event failed identity or digest verification.",
				Evidence: map[string]any{"sequence": event.Sequence},
			})
		}
	}
	return findings
}

func assessRecovery(
	operation state.OperationRecord,
	steps []state.StepRecord,
	locks []state.Lock,
) RecoveryAssessment {
	assessment := RecoveryAssessment{SafeNextActions: []string{}, BlockedActions: []string{}}
	switch operation.Status {
	case "succeeded":
		assessment.Classification = "not-required"
		assessment.SafeNextActions = append(assessment.SafeNextActions, "verify")
	case "failed", "blocked":
		assessment.Classification = "replan-required"
		assessment.SafeNextActions = append(assessment.SafeNextActions, "inspect", "create-new-plan")
		assessment.BlockedActions = append(assessment.BlockedActions, "blind-retry")
	case "partial":
		assessment.Classification = "manual-review-required"
		assessment.SafeNextActions = append(assessment.SafeNextActions, "inspect", "plan-compensation")
		assessment.BlockedActions = append(assessment.BlockedActions, "blind-retry", "automatic-rollback")
	case "applying":
		assessment.Classification = "interrupted-or-active"
		assessment.SafeNextActions = append(assessment.SafeNextActions, "inspect-owner", "plan-recovery")
		assessment.BlockedActions = append(assessment.BlockedActions, "retry", "release-lock")
	default:
		assessment.Classification = "unknown"
		assessment.BlockedActions = append(assessment.BlockedActions, "all-mutations")
	}
	for _, step := range steps {
		if step.Status == "applying" {
			assessment.Classification = "unknown-side-effects"
			assessment.BlockedActions = append(assessment.BlockedActions, "automatic-compensation")
		}
	}
	if len(locks) != 0 {
		assessment.BlockedActions = append(assessment.BlockedActions, "mutation-before-lock-review")
	}
	return assessment
}

func resolveStatePath(requested string) (string, error) {
	path := requested
	var err error
	if strings.TrimSpace(path) == "" {
		path, err = state.DefaultPath()
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(path)
}

func operationStateError(command string, operationID string, err error) domain.Envelope {
	if errors.Is(err, state.ErrNotFound) {
		return domain.NewEnvelope(command, domain.ExitNotProven, nil, domain.Finding{
			Code: "GDS_OPERATION_NOT_FOUND", Severity: domain.SeverityHigh,
			Message:  "The requested operation does not exist in the selected state store.",
			Evidence: map[string]any{"operation_id": operationID},
		})
	}
	return domain.InternalError(command, err)
}
