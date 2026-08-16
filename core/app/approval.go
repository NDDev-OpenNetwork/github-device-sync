package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	approvalcontract "github.com/NDDev-OpenNetwork/github-device-sync/core/approval"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type ApprovalIssueOptions struct {
	StatePath         string
	PrivateKeyPath    string
	OutputPath        string
	ActorID           string
	ActorType         string
	KeyID             string
	ExternalReference string
	TTL               time.Duration
}

type ApprovalIssueData struct {
	ApprovalID string    `json:"approval_id"`
	PlanID     string    `json:"plan_id"`
	ExpiresAt  time.Time `json:"expires_at"`
	OutputPath string    `json:"output_path"`
}

type PlanEnableOptions struct {
	StatePath    string
	ApprovalPath string
	DeviceID     string
	SessionID    string
}

type StateApprovalIssueOptions struct {
	PlanFile          string
	PrivateKeyPath    string
	OutputPath        string
	ActorID           string
	ActorType         string
	KeyID             string
	ExternalReference string
	TTL               time.Duration
}

func (services *Services) IssueStateLifecycleApproval(_ context.Context, options StateApprovalIssueOptions) domain.Envelope {
	const command = "gds state approve"
	if options.PlanFile == "" || options.PrivateKeyPath == "" || options.OutputPath == "" ||
		options.ActorID == "" || options.KeyID == "" ||
		(options.ActorType != "owner" && options.ActorType != "delegate" && options.ActorType != "automation") {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{Code: "GDS_APPROVAL_ISSUE_INPUT_INVALID", Severity: domain.SeverityHigh, Message: "Plan file, output, actor, key identity, and Ed25519 private key are required."})
	}
	raw, err := os.ReadFile(options.PlanFile)
	if err != nil || len(raw) < 2 || len(raw) > 64<<10 {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{Code: "GDS_STATE_PLAN_FILE_INVALID", Severity: domain.SeverityHigh, Message: "State lifecycle plan file is unavailable or unbounded."})
	}
	var plan StateLifecyclePlan
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{Code: "GDS_STATE_PLAN_FILE_INVALID", Severity: domain.SeverityHigh, Message: err.Error()})
	}
	expected, err := digestStateLifecyclePlan(plan)
	if err != nil || expected.PlanID != plan.PlanID || expected.PlanDigest != plan.PlanDigest || !plan.Required {
		return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{Code: "GDS_STATE_PLAN_FILE_INVALID", Severity: domain.SeverityHigh, Message: "State lifecycle plan identity or digest is invalid."})
	}
	if options.TTL == 0 {
		options.TTL = 15 * time.Minute
	}
	scopeDigest, err := stateLifecycleScopeDigest(plan)
	if err != nil {
		return domain.InternalError(command, err)
	}
	record, err := approvalcontract.NewRecord(plan.PlanID, plan.PlanDigest, "state-lifecycle", scopeDigest,
		options.ActorID, options.ActorType, options.ExternalReference, services.Now().UTC(), options.TTL)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{Code: "GDS_APPROVAL_VALIDITY_INVALID", Severity: domain.SeverityHigh, Message: err.Error()})
	}
	privateKey, err := approvalcontract.LoadPrivateKey(options.PrivateKeyPath)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitSecurity, nil, domain.Finding{Code: "GDS_APPROVAL_PRIVATE_KEY_INVALID", Severity: domain.SeverityHigh, Message: err.Error()})
	}
	record, err = approvalcontract.Sign(record, options.KeyID, privateKey)
	if err != nil {
		return domain.InternalError(command, err)
	}
	outputRaw, _ := json.MarshalIndent(record, "", "  ")
	output, err := filepath.Abs(options.OutputPath)
	if err == nil {
		var file *os.File
		file, err = os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, err = file.Write(append(outputRaw, '\n'))
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitConflict, nil, domain.Finding{Code: "GDS_APPROVAL_OUTPUT_WRITE_FAILED", Severity: domain.SeverityHigh, Message: err.Error()})
	}
	return domain.Success(command, ApprovalIssueData{ApprovalID: record.ApprovalID, PlanID: plan.PlanID, ExpiresAt: record.ExpiresAt, OutputPath: output})
}

func (services *Services) EnablePlan(ctx context.Context, planID string, options PlanEnableOptions) domain.Envelope {
	const command = "gds operation enable"
	if planID == "" || options.StatePath == "" || options.ApprovalPath == "" || options.DeviceID == "" || options.SessionID == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{Code: "GDS_PLAN_ENABLE_INPUT_INVALID", Severity: domain.SeverityHigh, Message: "Plan, state, signed approval, device, and session are required."})
	}
	store, err := state.Open(ctx, options.StatePath)
	if err != nil {
		return envelopeForError(command, options.StatePath, err)
	}
	defer store.Close()
	record, err := approvalcontract.LoadRecord(options.ApprovalPath)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitApproval, nil, domain.Finding{Code: "GDS_SIGNED_APPROVAL_READ_FAILED", Severity: domain.SeverityHigh, Message: err.Error()})
	}
	engine := operations.NewDefaultEngine(store, services.Schemas, nil, nil, options.DeviceID, options.SessionID)
	engine.Now = services.Now
	value, err := engine.EnableSigned(ctx, planID, record)
	if err != nil {
		return operationFailureEnvelope(command, err)
	}
	return domain.Success(command, value)
}

func (services *Services) IssuePlanApproval(ctx context.Context, planID string, options ApprovalIssueOptions) domain.Envelope {
	const command = "gds operation approve"
	if strings.TrimSpace(planID) == "" || options.StatePath == "" || options.PrivateKeyPath == "" ||
		options.OutputPath == "" || options.ActorID == "" || options.KeyID == "" ||
		(options.ActorType != "owner" && options.ActorType != "delegate" && options.ActorType != "automation") {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{Code: "GDS_APPROVAL_ISSUE_INPUT_INVALID", Severity: domain.SeverityHigh, Message: "Plan, state, output, actor, key identity, and Ed25519 private key are required."})
	}
	if options.TTL == 0 {
		options.TTL = 15 * time.Minute
	}
	store, err := state.OpenReadOnly(ctx, options.StatePath)
	if err != nil {
		return envelopeForError(command, options.StatePath, err)
	}
	defer store.Close()
	stored, err := store.GetPlan(ctx, planID)
	if err != nil || stored.Status != "planned" {
		return domain.NewEnvelope(command, domain.ExitStale, nil, domain.Finding{Code: "GDS_APPROVAL_PLAN_UNAVAILABLE", Severity: domain.SeverityHigh, Message: "Only an exact stored plan in planned state can be approved."})
	}
	var plan operations.Plan
	if err := json.Unmarshal(stored.Body, &plan); err != nil || plan.PlanDigest != stored.PlanDigest {
		return domain.InternalError(command, errors.New("stored plan body and digest disagree"))
	}
	scopeDigest, err := operations.ApprovalScopeDigest(plan)
	if err != nil {
		return domain.InternalError(command, err)
	}
	record, err := approvalcontract.NewRecord(plan.PlanID, plan.PlanDigest, plan.ApprovalClass, scopeDigest,
		options.ActorID, options.ActorType, options.ExternalReference, services.Now().UTC(), options.TTL)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{Code: "GDS_APPROVAL_VALIDITY_INVALID", Severity: domain.SeverityHigh, Message: err.Error()})
	}
	privateKey, err := approvalcontract.LoadPrivateKey(options.PrivateKeyPath)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitSecurity, nil, domain.Finding{Code: "GDS_APPROVAL_PRIVATE_KEY_INVALID", Severity: domain.SeverityHigh, Message: err.Error()})
	}
	record, err = approvalcontract.Sign(record, options.KeyID, privateKey)
	if err != nil {
		return domain.InternalError(command, err)
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return domain.InternalError(command, err)
	}
	output, err := filepath.Abs(options.OutputPath)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{Code: "GDS_APPROVAL_OUTPUT_INVALID", Severity: domain.SeverityHigh, Message: err.Error()})
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = file.Write(append(raw, '\n'))
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitConflict, nil, domain.Finding{Code: "GDS_APPROVAL_OUTPUT_WRITE_FAILED", Severity: domain.SeverityHigh, Message: "Approval output must be a new private file: " + err.Error()})
	}
	return domain.Success(command, ApprovalIssueData{ApprovalID: record.ApprovalID, PlanID: plan.PlanID, ExpiresAt: record.ExpiresAt, OutputPath: output})
}
