package app

import (
	"context"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/discovery"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
)

func (services *Services) completeRelationshipIndex(
	ctx context.Context,
	options DiscoveryOptions,
) (estate.IdentityIndex, []domain.Finding) {
	if finding := validateDiscoveryOptions(options); finding != nil {
		return estate.IdentityIndex{}, []domain.Finding{*finding}
	}
	discovered, err := services.Discovery.Discover(ctx, options.Root, discovery.Options{
		MaxDepth: options.MaxDepth, MaxRepositories: options.MaxRepositories,
		Concurrency: options.Concurrency,
	})
	if err != nil {
		return estate.IdentityIndex{}, []domain.Finding{{
			Code: "GDS_IDENTITY_INDEX_DISCOVERY_FAILED", Severity: domain.SeverityHigh,
			Message: err.Error(), Evidence: map[string]any{"root": options.Root},
		}}
	}
	findings := append([]domain.Finding(nil), discovered.Findings...)
	indexed := make([]estate.IndexedRepository, 0, len(discovered.Boundaries))
	loader := manifest.NewLoader(services.Schemas)
	for _, boundary := range discovered.Boundaries {
		if boundary.AnchorState != "valid" {
			findings = append(findings, domain.Finding{
				Code: "GDS_IDENTITY_INDEX_ANCHOR_REQUIRED", Severity: domain.SeverityHigh,
				Message:  "Complete relationship analysis requires every discovered Git boundary to have a valid anchor.",
				Evidence: map[string]any{"path": boundary.Path, "anchor_state": boundary.AnchorState},
			})
			continue
		}
		anchorValue, anchorFindings := loader.LoadRepository(boundary.Path)
		findings = append(findings, anchorFindings...)
		if len(anchorFindings) == 0 {
			indexed = append(indexed, estate.IndexedRepository{Path: boundary.Path, Anchor: anchorValue})
		}
	}
	index, indexFindings := estate.BuildIdentityIndex(indexed, true)
	findings = append(findings, indexFindings...)
	return index, findings
}
