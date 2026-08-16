package app

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
)

func (services *Services) RenderHarnessAdapter(
	ctx context.Context,
	path string,
	harnessID string,
	request harness.RenderRequest,
) domain.Envelope {
	adapter, envelope := services.resolveHarnessAdapter(ctx, path, harnessID, "gds harness render")
	if envelope != nil {
		return *envelope
	}
	candidate, findings := adapter.Render(request)
	return domain.NewEnvelope(
		"gds harness render", classifyFindings(findings), candidate, findings...,
	)
}

func (services *Services) InspectHarnessAdapter(
	ctx context.Context,
	path string,
	harnessID string,
	targetRoot string,
	request harness.RenderRequest,
) domain.Envelope {
	if strings.TrimSpace(targetRoot) == "" {
		return harnessTargetRequired("gds harness inspect")
	}
	adapter, envelope := services.resolveHarnessAdapter(ctx, path, harnessID, "gds harness inspect")
	if envelope != nil {
		return *envelope
	}
	report, findings := adapter.Inspect(targetRoot, request)
	return domain.NewEnvelope(
		"gds harness inspect", classifyFindings(findings), report, findings...,
	)
}

func (services *Services) PlanHarnessAdapter(
	ctx context.Context,
	path string,
	harnessID string,
	targetRoot string,
	request harness.RenderRequest,
	operation string,
) domain.Envelope {
	command := "gds harness plan-" + operation
	if strings.TrimSpace(targetRoot) == "" {
		return harnessTargetRequired(command)
	}
	adapter, envelope := services.resolveHarnessAdapter(ctx, path, harnessID, command)
	if envelope != nil {
		return *envelope
	}
	var plan harness.AdapterPlan
	var findings []domain.Finding
	switch operation {
	case "install":
		plan, findings = adapter.PlanInstall(targetRoot, request)
	case "update":
		plan, findings = adapter.PlanUpdate(targetRoot, request)
	case "remove":
		plan, findings = adapter.PlanRemove(targetRoot, request)
	default:
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_HARNESS_OPERATION_INVALID", Severity: domain.SeverityHigh,
			Message: "Harness lifecycle operation must be install, update, or remove.",
		})
	}
	return domain.NewEnvelope(command, classifyFindings(findings), plan, findings...)
}

func (services *Services) DoctorHarnessAdapter(
	ctx context.Context,
	path string,
	harnessID string,
	targetRoot string,
	request harness.RenderRequest,
) domain.Envelope {
	if strings.TrimSpace(targetRoot) == "" {
		return harnessTargetRequired("gds harness doctor")
	}
	adapter, envelope := services.resolveHarnessAdapter(ctx, path, harnessID, "gds harness doctor")
	if envelope != nil {
		return *envelope
	}
	report, findings := adapter.Doctor(targetRoot, request)
	return domain.NewEnvelope(
		"gds harness doctor", classifyFindings(findings), report, findings...,
	)
}

func (services *Services) EvaluateHarnessAdapter(
	ctx context.Context,
	path string,
	harnessID string,
	options harness.EvalOptions,
) domain.Envelope {
	command := "gds harness eval"
	if strings.TrimSpace(harnessID) == "" || harnessID == "all" ||
		strings.TrimSpace(options.SkillProfile) == "" ||
		strings.TrimSpace(options.ModelLabel) == "" ||
		strings.TrimSpace(options.ExecutionProfile) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_HARNESS_EVAL_INPUT_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Evaluation requires one harness, skill profile, model label, and execution profile.",
		})
	}
	if options.RuntimeEvidence != "" && options.RuntimeDriver != "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_HARNESS_EVAL_MODE_CONFLICT", Severity: domain.SeverityHigh,
			Message: "Use at most one of --runtime-evidence or --runtime-driver.",
		})
	}
	if options.RuntimeDriver != "" && strings.TrimSpace(options.EvidenceDirectory) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_HARNESS_EVAL_DIRECTORY_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Native runtime driver execution requires one empty --evidence-directory.",
		})
	}
	if (options.RuntimeEvidence != "" || options.RuntimeDriver != "") &&
		options.ModelLabel == "not-proven" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_HARNESS_EVAL_MODEL_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Native runtime evidence requires the exact tested model label.",
		})
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError(command, path, err)
	}
	if options.RuntimeEvidence != "" {
		if !filepath.IsAbs(options.RuntimeEvidence) {
			options.RuntimeEvidence = filepath.Join(info.WorktreeRoot, options.RuntimeEvidence)
		}
		options.RuntimeEvidence = filepath.Clean(options.RuntimeEvidence)
	}
	normalizePath := func(value string) string {
		if value == "" {
			return ""
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(info.WorktreeRoot, value)
		}
		return filepath.Clean(value)
	}
	options.RuntimeDriver = normalizePath(options.RuntimeDriver)
	options.EvidenceDirectory = normalizePath(options.EvidenceDirectory)
	run, findings := harness.Evaluate(
		ctx, info.WorktreeRoot, harnessID, options, services.Schemas, services.Now(), nil,
	)
	exitClass := domain.ExitSuccess
	if run.Result == "not-proven" {
		exitClass = domain.ExitNotProven
	} else if run.Result == "fail" || len(findings) != 0 {
		exitClass = domain.ExitValidation
	}
	envelope := domain.NewEnvelope(command, exitClass, run, findings...)
	if options.RuntimeDriver != "" {
		envelope.Mutation.Attempted = true
		envelope.Mutation.Completed = len(run.Transcripts) != 0
	}
	envelope.Scope["harness"] = harnessID
	envelope.Scope["evaluation_id"] = run.EvaluationID
	return envelope
}

func (services *Services) resolveHarnessAdapter(
	ctx context.Context,
	path string,
	harnessID string,
	command string,
) (harness.Adapter, *domain.Envelope) {
	if strings.TrimSpace(harnessID) == "" || harnessID == "all" {
		envelope := domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_HARNESS_EXACT_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Adapter lifecycle commands require one exact canonical harness ID.",
		})
		return nil, &envelope
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		envelope := envelopeForError(command, path, err)
		return nil, &envelope
	}
	adapter, findings := harness.NewAdapter(info.WorktreeRoot, harnessID, services.Schemas)
	if len(findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return nil, &envelope
	}
	return adapter, nil
}

func harnessTargetRequired(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
		Code: "GDS_HARNESS_TARGET_REQUIRED", Severity: domain.SeverityHigh,
		Message: "--target-root must identify an existing isolated or managed adapter root.",
	})
}
