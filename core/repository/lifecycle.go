// Package repository validates repository lifecycle transitions without performing provider writes.
package repository

import (
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

const (
	RenameOperation         = "rename"
	TransferOperation       = "transfer"
	ArchiveOperation        = "archive"
	DeleteOperation         = "delete"
	ProviderLifecycleAction = "github-repository-lifecycle"
	TransferApplyBlocker    = "GitHub repository transfer requires a dedicated user-access-token adapter and asynchronous acceptance verification; installation-token apply is unsupported."
)

type ProviderTransition struct {
	Operation              string `json:"operation"`
	RepositoryID           string `json:"repository_id"`
	ProviderRepositoryID   int64  `json:"provider_repository_id"`
	MutationCapabilityID   string `json:"mutation_capability_id,omitempty"`
	ExpectedProviderDigest string `json:"expected_provider_digest,omitempty"`
	CurrentInstallation    string `json:"current_installation"`
	CurrentOwner           string `json:"current_owner"`
	CurrentName            string `json:"current_name"`
	CurrentLifecycle       string `json:"current_lifecycle"`
	TargetInstallation     string `json:"target_installation"`
	TargetOwner            string `json:"target_owner"`
	TargetName             string `json:"target_name"`
	TargetLifecycle        string `json:"target_lifecycle"`
	AnalysisRoot           string `json:"analysis_root,omitempty"`
}

func ValidateDelete(current domain.RepositoryAnchor) (ProviderTransition, []domain.Finding) {
	transition := ProviderTransition{
		Operation: DeleteOperation, RepositoryID: current.Repository.ID,
		ProviderRepositoryID: current.Provider.RepositoryID,
		CurrentInstallation:  current.Provider.Installation,
		CurrentOwner:         current.Provider.Owner, CurrentName: current.Provider.Name,
		CurrentLifecycle:   current.Repository.Lifecycle,
		TargetInstallation: current.Provider.Installation,
		TargetOwner:        current.Provider.Owner, TargetName: current.Provider.Name,
		TargetLifecycle: "tombstoned",
	}
	if current.Repository.Lifecycle != "archived" {
		return transition, []domain.Finding{transitionFinding(
			"GDS_REPOSITORY_DELETE_ARCHIVE_REQUIRED",
			"Repository deletion requires an already archived repository.",
		)}
	}
	return transition, nil
}

func Parameters(transition ProviderTransition) map[string]any {
	parameters := map[string]any{
		"operation":                transition.Operation,
		"provider_repository_id":   strconv.FormatInt(transition.ProviderRepositoryID, 10),
		"mutation_capability_id":   transition.MutationCapabilityID,
		"expected_provider_digest": transition.ExpectedProviderDigest,
		"current_installation":     transition.CurrentInstallation,
		"current_owner":            transition.CurrentOwner,
		"current_name":             transition.CurrentName,
		"current_lifecycle":        transition.CurrentLifecycle,
		"target_installation":      transition.TargetInstallation,
		"target_owner":             transition.TargetOwner,
		"target_name":              transition.TargetName,
		"target_lifecycle":         transition.TargetLifecycle,
	}
	if transition.AnalysisRoot != "" {
		parameters["analysis_root"] = transition.AnalysisRoot
	}
	return map[string]any{"repository_provider": parameters}
}

func StepTransition(step operations.Step) (ProviderTransition, error) {
	if step.Action != ProviderLifecycleAction {
		return ProviderTransition{}, errors.New("unexpected repository provider action")
	}
	raw, ok := step.Parameters["repository_provider"].(map[string]any)
	if !ok {
		return ProviderTransition{}, errors.New("repository provider parameters are missing")
	}
	result := ProviderTransition{RepositoryID: step.RepositoryID}
	result.Operation, _ = raw["operation"].(string)
	providerID, _ := raw["provider_repository_id"].(string)
	result.ProviderRepositoryID, _ = strconv.ParseInt(providerID, 10, 64)
	result.MutationCapabilityID, _ = raw["mutation_capability_id"].(string)
	result.ExpectedProviderDigest, _ = raw["expected_provider_digest"].(string)
	result.CurrentInstallation, _ = raw["current_installation"].(string)
	result.CurrentOwner, _ = raw["current_owner"].(string)
	result.CurrentName, _ = raw["current_name"].(string)
	result.CurrentLifecycle, _ = raw["current_lifecycle"].(string)
	result.TargetInstallation, _ = raw["target_installation"].(string)
	result.TargetOwner, _ = raw["target_owner"].(string)
	result.TargetName, _ = raw["target_name"].(string)
	result.TargetLifecycle, _ = raw["target_lifecycle"].(string)
	result.AnalysisRoot, _ = raw["analysis_root"].(string)
	if (result.Operation != RenameOperation && result.Operation != TransferOperation &&
		result.Operation != ArchiveOperation && result.Operation != DeleteOperation) ||
		result.ProviderRepositoryID < 1 || result.CurrentInstallation == "" || result.CurrentOwner == "" ||
		result.CurrentName == "" || result.CurrentLifecycle == "" || result.TargetInstallation == "" ||
		result.TargetOwner == "" || result.TargetName == "" || result.TargetLifecycle == "" {
		return ProviderTransition{}, errors.New("repository provider parameters are invalid")
	}
	if result.Operation == DeleteOperation && result.AnalysisRoot == "" {
		return ProviderTransition{}, errors.New("repository delete parameters require a complete analysis root")
	}
	return result, nil
}

func ValidateTransition(
	operation string,
	current domain.RepositoryAnchor,
	candidate domain.RepositoryAnchor,
) (ProviderTransition, []domain.Finding) {
	transition := ProviderTransition{
		Operation: operation, RepositoryID: current.Repository.ID,
		ProviderRepositoryID: current.Provider.RepositoryID,
		CurrentInstallation:  current.Provider.Installation,
		CurrentOwner:         current.Provider.Owner, CurrentName: current.Provider.Name,
		CurrentLifecycle:   current.Repository.Lifecycle,
		TargetInstallation: candidate.Provider.Installation,
		TargetOwner:        candidate.Provider.Owner, TargetName: candidate.Provider.Name,
		TargetLifecycle: candidate.Repository.Lifecycle,
	}
	if current.Repository.ID != candidate.Repository.ID ||
		current.Provider.RepositoryID != candidate.Provider.RepositoryID ||
		current.Provider.Type != candidate.Provider.Type {
		return transition, []domain.Finding{transitionFinding(
			"GDS_REPOSITORY_TRANSITION_IDENTITY_CHANGED",
			"Repository lifecycle transitions must preserve GDS and provider identities.",
		)}
	}

	normalized := candidate
	switch operation {
	case RenameOperation:
		if !strings.EqualFold(current.Provider.Owner, candidate.Provider.Owner) ||
			current.Provider.Installation != candidate.Provider.Installation ||
			strings.EqualFold(current.Provider.Name, candidate.Provider.Name) {
			return transition, []domain.Finding{transitionFinding(
				"GDS_REPOSITORY_RENAME_DELTA_INVALID",
				"Rename must change only the provider repository name within the same owner and installation.",
			)}
		}
		if !aliasesPreserveCurrentLocator(current.Provider, candidate.Provider) {
			return transition, []domain.Finding{transitionFinding(
				"GDS_REPOSITORY_ALIAS_HISTORY_INVALID",
				"The candidate must preserve existing aliases and append the exact previous provider locator.",
			)}
		}
		normalized.Provider = current.Provider
	case TransferOperation:
		if strings.EqualFold(current.Provider.Owner, candidate.Provider.Owner) ||
			!strings.EqualFold(current.Provider.Name, candidate.Provider.Name) {
			return transition, []domain.Finding{transitionFinding(
				"GDS_REPOSITORY_TRANSFER_DELTA_INVALID",
				"Transfer must change the provider owner without combining a repository rename.",
			)}
		}
		if !aliasesPreserveCurrentLocator(current.Provider, candidate.Provider) {
			return transition, []domain.Finding{transitionFinding(
				"GDS_REPOSITORY_ALIAS_HISTORY_INVALID",
				"The candidate must preserve existing aliases and append the exact previous provider locator.",
			)}
		}
		normalized.Provider = current.Provider
		normalized.Classification = current.Classification
		normalized.Policy = current.Policy
	case ArchiveOperation:
		if current.Repository.Lifecycle == "archived" || candidate.Repository.Lifecycle != "archived" ||
			!reflect.DeepEqual(current.Provider, candidate.Provider) {
			return transition, []domain.Finding{transitionFinding(
				"GDS_REPOSITORY_ARCHIVE_DELTA_INVALID",
				"Archive must move a non-archived repository to archived without changing its provider locator.",
			)}
		}
		normalized.Repository.Lifecycle = current.Repository.Lifecycle
	default:
		return transition, []domain.Finding{transitionFinding(
			"GDS_REPOSITORY_TRANSITION_UNSUPPORTED", "Repository transition operation is unsupported.",
		)}
	}
	if !reflect.DeepEqual(current, normalized) {
		return transition, []domain.Finding{transitionFinding(
			"GDS_REPOSITORY_TRANSITION_SCOPE_EXCEEDED",
			"The candidate changes facts outside the selected repository lifecycle operation.",
		)}
	}
	return transition, nil
}

func aliasesPreserveCurrentLocator(current domain.GitHubLocator, candidate domain.GitHubLocator) bool {
	expected := make(map[string]struct{}, len(current.Aliases)+1)
	for _, alias := range current.Aliases {
		expected[aliasKey(alias.Owner, alias.Name)] = struct{}{}
	}
	expected[aliasKey(current.Owner, current.Name)] = struct{}{}
	actual := make(map[string]struct{}, len(candidate.Aliases))
	for _, alias := range candidate.Aliases {
		key := aliasKey(alias.Owner, alias.Name)
		if _, duplicate := actual[key]; duplicate {
			return false
		}
		actual[key] = struct{}{}
	}
	return reflect.DeepEqual(sortedKeys(expected), sortedKeys(actual))
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func aliasKey(owner string, name string) string {
	return strings.ToLower(owner + "/" + name)
}

func transitionFinding(code string, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
