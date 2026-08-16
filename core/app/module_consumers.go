package app

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
)

type ModuleConsumerPlanOptions struct {
	ProjectionOperationOptions
	ModulePath      string
	InventoryRoot   string
	MaxDepth        int
	MaxRepositories int
	Concurrency     int
	ConsumerIDs     []string
}

type ModuleConsumerSubplan struct {
	ConsumerID string           `json:"consumer_id"`
	Mode       string           `json:"mode"`
	Path       string           `json:"path,omitempty"`
	Status     string           `json:"status"`
	PlanID     string           `json:"plan_id,omitempty"`
	ExitClass  domain.ExitClass `json:"exit_class"`
	Findings   []domain.Finding `json:"findings"`
}

type ModuleConsumerPlanData struct {
	ModuleID string                  `json:"module_id"`
	Selected []string                `json:"selected"`
	Subplans []ModuleConsumerSubplan `json:"subplans"`
	Planned  int                     `json:"planned"`
	Blocked  int                     `json:"blocked"`
}

func (services *Services) PlanModuleConsumerUpdates(
	ctx context.Context,
	options ModuleConsumerPlanOptions,
) domain.Envelope {
	const command = "gds module update-consumers plan"
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	selected, finding := validateSelectedConsumers(options.ConsumerIDs)
	if finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	moduleInfo, err := services.Git.RepositoryInfo(ctx, options.ModulePath)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitNotProven, nil, moduleConsumerFinding(
			"GDS_MODULE_CONSUMER_SOURCE_NOT_PROVEN", err.Error(), "",
		))
	}
	_, moduleAnchor, findings := services.policyInputs(ctx, moduleInfo.WorktreeRoot)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	if !hasRole(moduleAnchor.Repository.Roles, "module") || moduleAnchor.Module == nil {
		return domain.NewEnvelope(command, domain.ExitPolicy, nil, moduleConsumerFinding(
			"GDS_MODULE_CONSUMER_ROLE_REQUIRED", "Selected source is not a declared module repository.", "",
		))
	}
	index, indexFindings := services.completeRelationshipIndex(ctx, DiscoveryOptions{
		Root: options.InventoryRoot, MaxDepth: options.MaxDepth,
		MaxRepositories: options.MaxRepositories, Concurrency: options.Concurrency,
	})
	if len(indexFindings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(indexFindings), nil, indexFindings...)
	}
	paths := map[string]string{}
	moduleIndexed := false
	for _, repository := range index.Repositories {
		paths[repository.ID] = repository.Path
		if repository.ID == moduleAnchor.Repository.ID {
			physical, resolveErr := filepath.EvalSymlinks(repository.Path)
			moduleIndexed = resolveErr == nil && filepath.Clean(physical) == moduleInfo.WorktreeRoot
		}
	}
	if !moduleIndexed {
		return domain.NewEnvelope(command, domain.ExitNotProven, nil, moduleConsumerFinding(
			"GDS_MODULE_CONSUMER_INDEX_MISMATCH",
			"Selected module boundary does not match the complete relationship index.", "",
		))
	}
	edgesByConsumer := map[string][]string{}
	for _, edge := range index.Consumers {
		if edge.Dependency == moduleAnchor.Repository.ID {
			edgesByConsumer[edge.Consumer] = append(edgesByConsumer[edge.Consumer], edge.Mode)
		}
	}
	results := []ModuleConsumerSubplan{}
	aggregateFindings := []domain.Finding{}
	planned := 0
	blocked := 0
	for _, consumerID := range selected {
		modes := append([]string(nil), edgesByConsumer[consumerID]...)
		sort.Strings(modes)
		if len(modes) == 0 {
			finding := moduleConsumerFinding(
				"GDS_MODULE_CONSUMER_NOT_SELECTED_BY_GRAPH",
				"Selected repository is not a typed consumer of this module.", consumerID,
			)
			results = append(results, ModuleConsumerSubplan{
				ConsumerID: consumerID, Status: "blocked", ExitClass: domain.ExitNotProven,
				Findings: []domain.Finding{finding},
			})
			aggregateFindings = append(aggregateFindings, finding)
			blocked++
			continue
		}
		for _, mode := range modes {
			path := paths[consumerID]
			if mode != "git-submodule" {
				finding := moduleConsumerFinding(
					"GDS_MODULE_CONSUMER_PACKAGE_PROVIDER_REQUIRED",
					"Package consumers require a verified registry and dependency-manifest provider.", consumerID,
				)
				results = append(results, ModuleConsumerSubplan{
					ConsumerID: consumerID, Mode: mode, Path: path, Status: "blocked",
					ExitClass: domain.ExitUnsupported, Findings: []domain.Finding{finding},
				})
				aggregateFindings = append(aggregateFindings, finding)
				blocked++
				continue
			}
			name := ""
			for _, relationship := range index.Relationships {
				if relationship.Source == consumerID && relationship.Target == moduleAnchor.Repository.ID &&
					relationship.Type == "git-submodule-consumer" {
					name = relationship.GitmodulesName
					break
				}
			}
			if path == "" || name == "" {
				finding := moduleConsumerFinding(
					"GDS_MODULE_CONSUMER_GRAPH_INCOMPLETE",
					"Consumer path or git-submodule relationship metadata is missing.", consumerID,
				)
				results = append(results, ModuleConsumerSubplan{
					ConsumerID: consumerID, Mode: mode, Path: path, Status: "blocked",
					ExitClass: domain.ExitNotProven, Findings: []domain.Finding{finding},
				})
				aggregateFindings = append(aggregateFindings, finding)
				blocked++
				continue
			}
			envelope := services.PlanModuleUpdatePin(ctx, path, ModulePinOptions{
				ProjectionOperationOptions: options.ProjectionOperationOptions,
				ModulePath:                 moduleInfo.WorktreeRoot, GitmodulesName: name,
			})
			result := ModuleConsumerSubplan{
				ConsumerID: consumerID, Mode: mode, Path: path,
				ExitClass: envelope.ExitClass, Findings: envelope.Findings,
			}
			if data, ok := envelope.Data.(ModulePinPlanData); ok && envelope.ExitClass == domain.ExitSuccess {
				result.Status = "planned"
				result.PlanID = data.Plan.PlanID
				planned++
			} else {
				result.Status = "blocked"
				blocked++
				for _, item := range envelope.Findings {
					item.Evidence = copyEvidence(item.Evidence)
					item.Evidence["consumer_id"] = consumerID
					aggregateFindings = append(aggregateFindings, item)
				}
			}
			results = append(results, result)
		}
	}
	class := domain.ExitSuccess
	if blocked != 0 {
		class = domain.ExitPartial
	}
	data := ModuleConsumerPlanData{
		ModuleID: moduleAnchor.Repository.ID, Selected: selected,
		Subplans: results, Planned: planned, Blocked: blocked,
	}
	envelope := domain.NewEnvelope(command, class, data, aggregateFindings...)
	envelope.Scope["repository_id"] = moduleAnchor.Repository.ID
	envelope.Scope["repositories"] = selected
	return envelope
}

func validateSelectedConsumers(values []string) ([]string, *domain.Finding) {
	if len(values) == 0 || len(values) > 2000 {
		finding := moduleConsumerFinding(
			"GDS_MODULE_CONSUMER_SELECTION_REQUIRED",
			"Select between 1 and 2000 exact consumer repository IDs.", "",
		)
		return nil, &finding
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if !identity.Valid("repo", value) || (index > 0 && result[index-1] == value) {
			finding := moduleConsumerFinding(
				"GDS_MODULE_CONSUMER_SELECTION_INVALID",
				"Consumer selection contains an invalid or duplicate stable repository ID.", value,
			)
			return nil, &finding
		}
	}
	return result, nil
}

func moduleConsumerFinding(code string, message string, consumerID string) domain.Finding {
	evidence := map[string]any{}
	if strings.TrimSpace(consumerID) != "" {
		evidence["consumer_id"] = consumerID
	}
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence}
}

func copyEvidence(source map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range source {
		result[key] = value
	}
	return result
}
