package app

import (
	"context"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/discovery"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/workspace"
)

type WorkspaceAuditOptions struct {
	Roots           []string
	DevicePath      string
	MaxDepth        int
	MaxRepositories int
	Concurrency     int
}

type WorkspaceAuditData struct {
	Roots      []string                  `json:"roots"`
	Device     workspace.DeviceCandidate `json:"device_descriptor"`
	Discovered int                       `json:"discovered"`
	Anchored   int                       `json:"anchored"`
	Layout     workspace.LayoutReport    `json:"layout"`
	Identity   estate.IdentityIndex      `json:"identity_index"`
}

func (services *Services) AuditWorkspaceLayout(
	ctx context.Context,
	options WorkspaceAuditOptions,
) domain.Envelope {
	const command = "gds workspace audit"
	if finding := validateWorkspaceAuditOptions(options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	device, findings := workspace.LoadDeviceCandidate(options.DevicePath, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	environment, err := workspace.CurrentEnvironment()
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitNotProven, nil, domain.Finding{
			Code: "GDS_WORKSPACE_ENVIRONMENT_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: err.Error(),
		})
	}

	boundaries := map[string]discovery.Boundary{}
	roots := make([]string, 0, len(options.Roots))
	for _, root := range options.Roots {
		result, discoverErr := services.Discovery.Discover(ctx, root, discovery.Options{
			MaxDepth: options.MaxDepth, MaxRepositories: options.MaxRepositories,
			Concurrency: options.Concurrency,
		})
		if discoverErr != nil {
			return envelopeForError(command, root, discoverErr)
		}
		roots = append(roots, result.Root)
		findings = append(findings, result.Findings...)
		for _, boundary := range result.Boundaries {
			boundaries[boundary.Path] = boundary
		}
	}
	sort.Strings(roots)
	roots = uniqueStrings(roots)
	if len(boundaries) > options.MaxRepositories {
		findings = append(findings, domain.Finding{
			Code: "GDS_WORKSPACE_AUDIT_LIMIT_EXCEEDED", Severity: domain.SeverityHigh,
			Message:  "Workspace audit exceeds the configured total repository limit.",
			Evidence: map[string]any{"count": len(boundaries), "limit": options.MaxRepositories},
		})
	}

	paths := make([]string, 0, len(boundaries))
	for path := range boundaries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	loader := manifest.NewLoader(services.Schemas)
	layoutInput := make([]workspace.LayoutRepository, 0, len(paths))
	indexed := make([]estate.IndexedRepository, 0, len(paths))
	for _, path := range paths {
		boundary := boundaries[path]
		if boundary.AnchorState != "valid" {
			findings = append(findings, domain.Finding{
				Code: "GDS_WORKSPACE_ANCHOR_REQUIRED", Severity: domain.SeverityHigh,
				Message:  "Every audited Git boundary requires a valid repository anchor.",
				Evidence: map[string]any{"path": path, "anchor_state": boundary.AnchorState},
			})
			continue
		}
		anchorValue, anchorFindings := loader.LoadRepository(path)
		findings = append(findings, anchorFindings...)
		if len(anchorFindings) != 0 {
			continue
		}
		layoutInput = append(layoutInput, workspace.LayoutRepository{
			Path: path, SuperprojectRoot: boundary.SuperprojectRoot, Anchor: anchorValue,
		})
		indexed = append(indexed, estate.IndexedRepository{Path: path, Anchor: anchorValue})
	}

	identityIndex, identityFindings := estate.BuildIdentityIndex(indexed, false)
	findings = append(findings, identityFindings...)
	layoutReport, layoutFindings := workspace.AuditLayout(
		device.Descriptor, environment, layoutInput,
	)
	layoutReport.Invalid += len(boundaries) - len(indexed)
	findings = append(findings, layoutFindings...)
	return domain.NewEnvelope(command, classifyFindings(findings), WorkspaceAuditData{
		Roots: roots, Device: device, Discovered: len(boundaries), Anchored: len(indexed),
		Layout: layoutReport, Identity: identityIndex,
	}, findings...)
}

func validateWorkspaceAuditOptions(options WorkspaceAuditOptions) *domain.Finding {
	invalid := ""
	switch {
	case len(options.Roots) == 0:
		invalid = "at least one --root is required"
	case strings.TrimSpace(options.DevicePath) == "":
		invalid = "--device is required"
	case options.MaxDepth < 1:
		invalid = "--max-depth must be at least 1"
	case options.MaxRepositories < 1 || options.MaxRepositories > 2000:
		invalid = "--max-repositories must be between 1 and 2000"
	case options.Concurrency < 1 || options.Concurrency > 16:
		invalid = "--concurrency must be between 1 and 16"
	}
	if invalid == "" {
		for _, root := range options.Roots {
			if strings.TrimSpace(root) == "" {
				invalid = "--root values must be non-empty"
				break
			}
		}
	}
	if invalid == "" {
		return nil
	}
	return &domain.Finding{
		Code: "GDS_WORKSPACE_AUDIT_OPTIONS_INVALID", Severity: domain.SeverityHigh,
		Message: invalid + ".",
	}
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
