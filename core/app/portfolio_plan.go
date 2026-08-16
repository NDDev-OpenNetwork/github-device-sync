package app

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/portfolio"
)

type PortfolioPlanOptions struct {
	Portfolio       string
	Operation       string
	Intent          string
	InventoryRoot   string
	MaxDepth        int
	MaxRepositories int
	Concurrency     int
}

func (services *Services) PlanPortfolioChange(
	ctx context.Context,
	options PortfolioPlanOptions,
) domain.Envelope {
	const command = "gds portfolio plan"
	if finding := validatePortfolioPlanOptions(options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	index, findings := services.completeRelationshipIndex(ctx, DiscoveryOptions{
		Root: options.InventoryRoot, MaxDepth: options.MaxDepth,
		MaxRepositories: options.MaxRepositories, Concurrency: options.Concurrency,
	})
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	estateRoot, estateFindings := services.lifecycleEstateRoot(ctx)
	if len(estateFindings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(estateFindings), nil, estateFindings...)
	}
	loader := manifest.NewLoader(services.Schemas)
	subplans := []portfolio.Subplan{}
	aggregateFindings := []domain.Finding{}
	for _, repository := range index.Repositories {
		if !stringSliceContains(repository.Portfolios, options.Portfolio) {
			continue
		}
		subplan := portfolio.Subplan{
			RepositoryID: repository.ID, Path: repository.Path,
			Status: "blocked", FindingCodes: []string{},
		}
		anchorValue, anchorFindings := loader.LoadRepository(repository.Path)
		for _, finding := range anchorFindings {
			subplan.FindingCodes = append(subplan.FindingCodes, finding.Code)
			aggregateFindings = append(aggregateFindings, portfolioRepositoryFinding(finding, repository.ID))
		}
		status, statusErr := services.Git.InspectStatus(ctx, repository.Path)
		if statusErr != nil {
			finding := domain.Finding{
				Code: "GDS_PORTFOLIO_REPOSITORY_STATUS_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message: statusErr.Error(),
			}
			subplan.FindingCodes = append(subplan.FindingCodes, finding.Code)
			aggregateFindings = append(aggregateFindings, portfolioRepositoryFinding(finding, repository.ID))
		}
		manifestDigest, digestErr := fileDigest(filepath.Join(repository.Path, ".gds", "repository.yaml"))
		if digestErr != nil {
			finding := domain.Finding{
				Code: "GDS_PORTFOLIO_REPOSITORY_MANIFEST_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message: digestErr.Error(),
			}
			subplan.FindingCodes = append(subplan.FindingCodes, finding.Code)
			aggregateFindings = append(aggregateFindings, portfolioRepositoryFinding(finding, repository.ID))
		}
		compiled := services.Compiler.CompileDirectory(
			estateRoot, anchorValue, compiler.DevelopmentBundleVersion,
		)
		for _, finding := range compiled.Findings {
			subplan.FindingCodes = append(subplan.FindingCodes, finding.Code)
			aggregateFindings = append(aggregateFindings, portfolioRepositoryFinding(finding, repository.ID))
		}
		if len(anchorFindings) == 0 && statusErr == nil && digestErr == nil &&
			len(compiled.Findings) == 0 && anchorValue.Repository.Lifecycle == "active" &&
			status.Head.Mode == "branch" && status.Head.OID != "" &&
			status.Branch.Name == anchorValue.Git.DefaultBranch &&
			status.Branch.Upstream == "origin/"+anchorValue.Git.DefaultBranch &&
			status.Branch.Ahead == 0 && status.Branch.Behind == 0 && !status.Branch.Diverged &&
			checkoutStatusIsClean(status) {
			subplan.Status = "ready"
			subplan.HeadOID = status.Head.OID
			subplan.ManifestDigest = manifestDigest
			subplan.PolicyDigest = compiled.Document.CompiledPolicy.Digest
		} else if len(subplan.FindingCodes) == 0 {
			finding := domain.Finding{
				Code: "GDS_PORTFOLIO_REPOSITORY_NOT_READY", Severity: domain.SeverityHigh,
				Message: "Repository is not clean and current on its active default branch.",
			}
			subplan.FindingCodes = append(subplan.FindingCodes, finding.Code)
			aggregateFindings = append(aggregateFindings, portfolioRepositoryFinding(finding, repository.ID))
		}
		subplans = append(subplans, subplan)
	}
	if len(subplans) == 0 {
		return domain.NewEnvelope(command, domain.ExitNotProven, nil, domain.Finding{
			Code: "GDS_PORTFOLIO_TARGETS_EMPTY", Severity: domain.SeverityHigh,
			Message: "No indexed repository belongs to the selected portfolio.",
		})
	}
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError(command, err)
	}
	plan, planFindings := portfolio.Build(portfolio.BuildInput{
		PlanID: planID, CreatedAt: now, Portfolio: options.Portfolio,
		Operation: options.Operation, Intent: options.Intent, Subplans: subplans,
	}, services.Schemas)
	if len(planFindings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(planFindings), nil, planFindings...)
	}
	class := domain.ExitSuccess
	if plan.BlockedCount != 0 {
		class = domain.ExitPartial
	}
	envelope := domain.NewEnvelope(command, class, plan, aggregateFindings...)
	envelope.Scope["repositories"] = planTargetIDs(plan)
	return envelope
}

func validatePortfolioPlanOptions(options PortfolioPlanOptions) *domain.Finding {
	portfolioValid := strings.HasPrefix(options.Portfolio, "portfolio:") ||
		identity.Valid("portfolio", options.Portfolio)
	intent := strings.TrimSpace(options.Intent)
	operationValid := options.Operation == "repository-change" ||
		options.Operation == "projection-rollout" || options.Operation == "policy-rollout"
	if !portfolioValid || !operationValid || intent == "" || intent != options.Intent ||
		len(intent) > 512 || strings.ContainsAny(intent, "\x00\r\n") {
		finding := domain.Finding{
			Code: "GDS_PORTFOLIO_PLAN_INPUT_INVALID", Severity: domain.SeverityHigh,
			Message: "Portfolio, operation, and one bounded single-line intent are required.",
		}
		return &finding
	}
	return nil
}

func portfolioRepositoryFinding(finding domain.Finding, repositoryID string) domain.Finding {
	finding.Evidence = copyEvidence(finding.Evidence)
	finding.Evidence["repository_id"] = repositoryID
	return finding
}

func planTargetIDs(plan portfolio.Plan) []string {
	result := make([]string, 0, len(plan.Subplans))
	for _, subplan := range plan.Subplans {
		result = append(result, subplan.RepositoryID)
	}
	return result
}

func stringSliceContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
