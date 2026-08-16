package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	moduleworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/module"
)

type ModuleRelationshipOptions struct {
	ProjectionOperationOptions
	ModuleAnchorPath string
	ModuleID         string
	GitmodulesName   string
}

func (services *Services) PlanModuleAdd(
	ctx context.Context,
	path string,
	options ModuleRelationshipOptions,
) domain.Envelope {
	_, consumer, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module add plan", classifyFindings(findings), nil, findings...)
	}
	moduleCandidate, findings := services.loadAnchorCandidate(options.ModuleAnchorPath)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module add plan", classifyFindings(findings), nil, findings...)
	}
	topology, err := services.Git.InspectTopology(ctx, path)
	if err != nil {
		return envelopeForError("gds module add plan", path, err)
	}
	updated, findings := moduleworkflow.AddGitSubmoduleRelationship(
		consumer, moduleCandidate.Anchor, options.GitmodulesName, topology,
	)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module add plan", classifyFindings(findings), nil, findings...)
	}
	raw, findings := services.readAnchorSource(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module add plan", classifyFindings(findings), nil, findings...)
	}
	candidate, findings := anchor.SpliceRelationships(raw, updated, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module add plan", classifyFindings(findings), nil, findings...)
	}
	return services.planAnchorChange(
		ctx, "gds module add plan", "add-module-relationship",
		"onboard-module-relationship", path, candidate, options.ProjectionOperationOptions,
	)
}

func (services *Services) ApplyModuleAdd(
	ctx context.Context,
	planID string,
	options ModuleRelationshipOptions,
) domain.Envelope {
	return services.applyAnchorChange(
		ctx, "gds module add apply", "add-module-relationship",
		planID, options.ProjectionOperationOptions,
	)
}

func (services *Services) VerifyModuleAdd(
	ctx context.Context,
	operationID string,
	options ModuleRelationshipOptions,
) domain.Envelope {
	return services.verifyAnchorChange(
		ctx, "gds module add verify", "add-module-relationship",
		operationID, options.ProjectionOperationOptions,
	)
}

func (services *Services) PlanModuleRemove(
	ctx context.Context,
	path string,
	options ModuleRelationshipOptions,
) domain.Envelope {
	if strings.TrimSpace(options.ModuleID) == "" || strings.TrimSpace(options.GitmodulesName) == "" {
		return domain.NewEnvelope("gds module remove plan", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_MODULE_RELATIONSHIP_IDENTITY_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--module-id and --name must identify the exact relationship to retire.",
		})
	}
	_, consumer, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module remove plan", classifyFindings(findings), nil, findings...)
	}
	topology, err := services.Git.InspectTopology(ctx, path)
	if err != nil {
		return envelopeForError("gds module remove plan", path, err)
	}
	updated, findings := moduleworkflow.RemoveGitSubmoduleRelationship(
		consumer, options.ModuleID, options.GitmodulesName, topology,
	)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module remove plan", classifyFindings(findings), nil, findings...)
	}
	raw, findings := services.readAnchorSource(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module remove plan", classifyFindings(findings), nil, findings...)
	}
	candidate, findings := anchor.SpliceRelationships(raw, updated, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope("gds module remove plan", classifyFindings(findings), nil, findings...)
	}
	return services.planAnchorChange(
		ctx, "gds module remove plan", "remove-module-relationship",
		"retire-module-relationship", path, candidate, options.ProjectionOperationOptions,
	)
}

func (services *Services) ApplyModuleRemove(
	ctx context.Context,
	planID string,
	options ModuleRelationshipOptions,
) domain.Envelope {
	return services.applyAnchorChange(
		ctx, "gds module remove apply", "remove-module-relationship",
		planID, options.ProjectionOperationOptions,
	)
}

func (services *Services) VerifyModuleRemove(
	ctx context.Context,
	operationID string,
	options ModuleRelationshipOptions,
) domain.Envelope {
	return services.verifyAnchorChange(
		ctx, "gds module remove verify", "remove-module-relationship",
		operationID, options.ProjectionOperationOptions,
	)
}

// readAnchorSource returns the anchor exactly as authored, which the caller
// needs in order to change one block of it without rewriting the rest.
func (services *Services) readAnchorSource(
	ctx context.Context,
	path string,
) ([]byte, []domain.Finding) {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return nil, []domain.Finding{{
			Code: "GDS_ANCHOR_SOURCE_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "The repository boundary holding the anchor could not be resolved.",
		}}
	}
	source := filepath.Join(info.WorktreeRoot, filepath.FromSlash(anchor.Path))
	raw, err := os.ReadFile(source)
	if err != nil {
		return nil, []domain.Finding{{
			Code: "GDS_ANCHOR_SOURCE_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  "The repository anchor could not be read as authored.",
			Evidence: map[string]any{"path": source},
		}}
	}
	return raw, nil
}
