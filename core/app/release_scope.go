package app

import (
	"context"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/releaseconsumer"
)

type ReleaseScopeOptions struct {
	InstallRoot     string
	TrustPolicyPath string
}

type ReleaseScopeData struct {
	InstallRoot string                             `json:"install_root"`
	TrustDomain string                             `json:"trust_domain"`
	ScopeDigest string                             `json:"scope_digest"`
	Active      releaseconsumer.ActiveInstallation `json:"active"`
}

func (services *Services) InspectReleaseScope(
	_ context.Context,
	options ReleaseScopeOptions,
) domain.Envelope {
	command := "gds release scope"
	if strings.TrimSpace(options.InstallRoot) == "" || strings.TrimSpace(options.TrustPolicyPath) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_RELEASE_SCOPE_INPUT_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Exact installation root and independent local trust policy are required.",
		})
	}
	trust, err := bundle.LoadTrustFile(options.TrustPolicyPath, services.Schemas)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_RELEASE_TRUST_POLICY_INVALID", Severity: domain.SeverityHigh,
			Message: "Local consumer trust policy is invalid.",
		})
	}
	canonicalRoot, scope, err := releaseconsumer.ResolveInstallScope(options.InstallRoot, trust.TrustDomain)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_RELEASE_INSTALL_ROOT_INVALID", Severity: domain.SeverityHigh,
			Message: "Installation root cannot be resolved to one canonical scope.",
		})
	}
	active, err := releaseconsumer.InspectActive(options.InstallRoot, services.Schemas)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_RELEASE_ACTIVE_STATE_INVALID", Severity: domain.SeverityHigh,
			Message: "Current installation state is invalid.",
		})
	}
	return domain.Success(command, ReleaseScopeData{
		InstallRoot: canonicalRoot, TrustDomain: trust.TrustDomain,
		ScopeDigest: scope, Active: active,
	})
}
