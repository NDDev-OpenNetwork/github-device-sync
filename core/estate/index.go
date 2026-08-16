package estate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
)

type IndexedRepository struct {
	Path   string                  `json:"path"`
	Anchor domain.RepositoryAnchor `json:"anchor"`
}

type IdentityIndex struct {
	Repositories  []IdentityRepository   `json:"repositories"`
	Relationships []IdentityRelationship `json:"relationships"`
	Consumers     []ConsumerEdge         `json:"consumers"`
}

type IdentityRepository struct {
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	ProviderID int64    `json:"provider_id"`
	Owner      string   `json:"owner"`
	Name       string   `json:"name"`
	Roles      []string `json:"roles"`
	Portfolios []string `json:"portfolios"`
	Lifecycle  string   `json:"lifecycle"`
}

type IdentityRelationship struct {
	Source          string `json:"source"`
	Type            string `json:"type"`
	Target          string `json:"target"`
	GitmodulesName  string `json:"gitmodules_name,omitempty"`
	Materialization string `json:"materialization,omitempty"`
}

type ConsumerEdge struct {
	Dependency string `json:"dependency"`
	Consumer   string `json:"consumer"`
	Mode       string `json:"mode"`
}

func BuildIdentityIndex(
	input []IndexedRepository,
	requireRelationshipTargets bool,
) (IdentityIndex, []domain.Finding) {
	index := IdentityIndex{
		Repositories: []IdentityRepository{}, Relationships: []IdentityRelationship{},
		Consumers: []ConsumerEdge{},
	}
	if len(input) > 2000 {
		return index, []domain.Finding{indexFinding(
			"GDS_IDENTITY_INDEX_LIMIT_EXCEEDED",
			"Identity index exceeds the 2000-repository contract bound.",
			map[string]any{"count": len(input), "limit": 2000},
		)}
	}
	findings := []domain.Finding{}
	byID := map[string]IndexedRepository{}
	byProviderID := map[int64]string{}
	byLocator := map[string]string{}
	byPath := map[string]string{}
	for _, item := range input {
		anchor := item.Anchor
		if previous, duplicate := byID[anchor.Repository.ID]; duplicate {
			findings = append(findings, indexFinding(
				"GDS_IDENTITY_INDEX_ID_CONFLICT", "Stable repository identity is claimed more than once.",
				map[string]any{"repository_id": anchor.Repository.ID, "first_path": previous.Path, "second_path": item.Path},
			))
			continue
		}
		byID[anchor.Repository.ID] = item
		if previous, duplicate := byProviderID[anchor.Provider.RepositoryID]; duplicate {
			findings = append(findings, indexFinding(
				"GDS_IDENTITY_INDEX_PROVIDER_CONFLICT", "Provider repository identity is claimed by multiple GDS identities.",
				map[string]any{"provider_id": anchor.Provider.RepositoryID, "first": previous, "second": anchor.Repository.ID},
			))
		} else {
			byProviderID[anchor.Provider.RepositoryID] = anchor.Repository.ID
		}
		locator := strings.ToLower(anchor.Provider.Owner + "/" + anchor.Provider.Name)
		if previous, duplicate := byLocator[locator]; duplicate {
			findings = append(findings, indexFinding(
				"GDS_IDENTITY_INDEX_LOCATOR_CONFLICT", "Current provider locator resolves to multiple GDS identities.",
				map[string]any{"locator": locator, "first": previous, "second": anchor.Repository.ID},
			))
		} else {
			byLocator[locator] = anchor.Repository.ID
		}
		if previous, duplicate := byPath[item.Path]; duplicate {
			findings = append(findings, indexFinding(
				"GDS_IDENTITY_INDEX_PATH_CONFLICT", "Local checkout path resolves to multiple GDS identities.",
				map[string]any{"path": item.Path, "first": previous, "second": anchor.Repository.ID},
			))
		} else {
			byPath[item.Path] = anchor.Repository.ID
		}
		roles := append([]string(nil), anchor.Repository.Roles...)
		portfolios := append([]string(nil), anchor.Classification.Portfolios...)
		sort.Strings(roles)
		sort.Strings(portfolios)
		index.Repositories = append(index.Repositories, IdentityRepository{
			ID: anchor.Repository.ID, Path: item.Path, ProviderID: anchor.Provider.RepositoryID,
			Owner: anchor.Provider.Owner, Name: anchor.Provider.Name, Roles: roles,
			Portfolios: portfolios, Lifecycle: anchor.Repository.Lifecycle,
		})
	}
	for _, item := range byID {
		for _, relationship := range item.Anchor.Relationships {
			edge := IdentityRelationship{
				Source: item.Anchor.Repository.ID, Type: relationship.Type, Target: relationship.Target,
				GitmodulesName: relationship.GitmodulesName, Materialization: relationship.Materialization,
			}
			index.Relationships = append(index.Relationships, edge)
			if identity.Valid("repo", relationship.Target) {
				if _, found := byID[relationship.Target]; !found && requireRelationshipTargets {
					findings = append(findings, indexFinding(
						"GDS_IDENTITY_INDEX_TARGET_MISSING", "Typed repository relationship target is absent from the complete index.",
						map[string]any{"source": edge.Source, "type": edge.Type, "target": edge.Target},
					))
				}
			}
			switch relationship.Type {
			case "git-submodule-consumer":
				index.Consumers = append(index.Consumers, ConsumerEdge{
					Dependency: relationship.Target, Consumer: edge.Source, Mode: "git-submodule",
				})
				// The module-owned contract must enumerate the mechanism its
				// consumers actually use; otherwise a planner reading
				// module.consumption gets a different dependency model from
				// .gitmodules and the typed relationship.
				findings = append(findings, consumptionContractFindings(
					byID, edge, "git-submodule",
				)...)
			case "package-consumer":
				index.Consumers = append(index.Consumers, ConsumerEdge{
					Dependency: relationship.Target, Consumer: edge.Source, Mode: "package",
				})
				// Same obligation as the other two consumer types: the module
				// contract must name the mechanism, or a planner reading
				// module.consumption cannot see this consumer at all.
				findings = append(findings, consumptionContractFindings(
					byID, edge, "package",
				)...)
			case "workflow-module-consumer":
				index.Consumers = append(index.Consumers, ConsumerEdge{
					Dependency: relationship.Target, Consumer: edge.Source, Mode: "workflow-module",
				})
				// A reusable workflow is executed by GitHub from the module
				// repository rather than vendored into the consumer, so
				// `runtime-service` is its module-side consumption value.
				findings = append(findings, consumptionContractFindings(
					byID, edge, "runtime-service",
				)...)
				// The typed relationship must not silently disagree with the flat
				// ci.workflow_ref that actually drives the generated workflow: when
				// the module target resolves and the consumer declares a
				// workflow_ref, the ref's repository must equal the module identity.
				if module, found := byID[relationship.Target]; found &&
					item.Anchor.CI != nil && item.Anchor.CI.WorkflowRef != "" {
					wantRepo := strings.ToLower(module.Anchor.Provider.Owner + "/" + module.Anchor.Provider.Name)
					gotRepo := workflowRefRepository(item.Anchor.CI.WorkflowRef)
					if gotRepo != wantRepo {
						findings = append(findings, indexFinding(
							"GDS_IDENTITY_WORKFLOW_REF_MODULE_MISMATCH",
							"ci.workflow_ref repository disagrees with the workflow-module-consumer target identity.",
							map[string]any{
								"source": edge.Source, "target": edge.Target,
								"workflow_ref_repository": gotRepo, "module_repository": wantRepo,
							},
						))
					}
				}
			}
		}
	}
	sort.Slice(index.Repositories, func(left, right int) bool {
		return index.Repositories[left].ID < index.Repositories[right].ID
	})
	sort.Slice(index.Relationships, func(left, right int) bool {
		leftKey := fmt.Sprintf("%s\x00%s\x00%s", index.Relationships[left].Source, index.Relationships[left].Type, index.Relationships[left].Target)
		rightKey := fmt.Sprintf("%s\x00%s\x00%s", index.Relationships[right].Source, index.Relationships[right].Type, index.Relationships[right].Target)
		return leftKey < rightKey
	})
	sort.Slice(index.Consumers, func(left, right int) bool {
		leftKey := index.Consumers[left].Dependency + "\x00" + index.Consumers[left].Consumer + "\x00" + index.Consumers[left].Mode
		rightKey := index.Consumers[right].Dependency + "\x00" + index.Consumers[right].Consumer + "\x00" + index.Consumers[right].Mode
		return leftKey < rightKey
	})
	sortFindings(findings)
	return index, findings
}

func indexFinding(code string, message string, evidence map[string]any) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence}
}

// workflowRefRepository returns the lowercased "owner/name" prefix of a
// ci.workflow_ref value ("owner/name/.github/workflows/<file>.yml@<sha>"), or ""
// when the ref does not carry at least an owner and name segment.
func workflowRefRepository(ref string) string {
	parts := strings.SplitN(ref, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return strings.ToLower(parts[0] + "/" + parts[1])
}

// consumptionContractFindings reports `GDS_IDENTITY_CONSUMPTION_UNDECLARED`
// when a resolved dependency's own module contract omits the consumption mode
// its consumer actually uses. It stays silent for an unresolved target: that is
// already covered by `GDS_IDENTITY_INDEX_TARGET_MISSING`.
func consumptionContractFindings(
	byID map[string]IndexedRepository,
	edge IdentityRelationship,
	required string,
) []domain.Finding {
	dependency, found := byID[edge.Target]
	if !found || dependency.Anchor.Module == nil {
		return nil
	}
	for _, declared := range dependency.Anchor.Module.Consumption {
		if declared == required {
			return nil
		}
	}
	return []domain.Finding{indexFinding(
		"GDS_IDENTITY_CONSUMPTION_UNDECLARED",
		"Dependency module contract omits the consumption mode its consumer uses.",
		map[string]any{
			"consumer": edge.Source, "dependency": edge.Target,
			"relationship": edge.Type, "required_consumption": required,
			"declared_consumption": dependency.Anchor.Module.Consumption,
		},
	)}
}
