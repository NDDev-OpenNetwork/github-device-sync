package estate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func Compile(
	config Config,
	repositories []ObservedRepository,
) (CompiledInventory, []domain.Finding) {
	result := CompiledInventory{EstateID: config.Root.Estate.ID}
	if len(repositories) > 2000 {
		return result, []domain.Finding{{
			Code: "GDS_ESTATE_REPOSITORY_LIMIT_EXCEEDED", Severity: domain.SeverityHigh,
			Message:  "Observed inventory exceeds the 2000-repository contract bound.",
			Evidence: map[string]any{"count": len(repositories), "limit": 2000},
		}}
	}
	ownerByLogin := map[string]Owner{}
	for _, owner := range config.Owners {
		ownerByLogin[strings.ToLower(owner.Owner.ProviderLogin)] = owner
	}
	seenProviderIDs := map[int64]struct{}{}
	findings := []domain.Finding{}
	for _, repository := range repositories {
		if repository.ProviderID <= 0 || repository.Owner == "" || repository.Name == "" {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_OBSERVATION_INVALID", Severity: domain.SeverityHigh,
				Message:  "Observed repository is missing provider identity fields.",
				Evidence: map[string]any{"provider_id": repository.ProviderID},
			})
			continue
		}
		if _, duplicate := seenProviderIDs[repository.ProviderID]; duplicate {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_PROVIDER_ID_DUPLICATE", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Provider repository id %d occurs more than once.", repository.ProviderID),
				Evidence: map[string]any{"provider_id": repository.ProviderID},
			})
			continue
		}
		seenProviderIDs[repository.ProviderID] = struct{}{}
		assignment := Assignment{
			ProviderID: repository.ProviderID, Owner: repository.Owner, Name: repository.Name,
			IdentityState: "unassigned", ManagementMode: config.Root.Discovery.DefaultManagementMode,
			RolloutRing: config.Root.Rollout.DefaultRing,
		}
		owner, found := ownerByLogin[strings.ToLower(repository.Owner)]
		if !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_OWNER_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Observed owner %q is not registered in desired estate configuration.", repository.Owner),
				Evidence: map[string]any{"provider_id": repository.ProviderID, "owner": repository.Owner},
			})
			result.Repositories = append(result.Repositories, assignment)
			continue
		}
		assignment.OwnerID = owner.Owner.ID
		assignment.InstallationID = owner.Owner.Installation
		assignment.PolicyProfiles = []string{owner.Defaults.PolicyProfile}
		assignment.RolloutRing = owner.Defaults.RolloutRing
		matches := matchedSelectors(config.Selectors, owner.Owner.ID, repository)
		if conflict, conflictIDs := equalPriorityConflict(matches); conflict {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_SELECTOR_CONFLICT", Severity: domain.SeverityHigh,
				Message: "Multiple selectors with equal priority match one repository.",
				Evidence: map[string]any{
					"provider_id": repository.ProviderID,
					"selectors":   conflictIDs,
				},
			})
			result.Repositories = append(result.Repositories, assignment)
			continue
		}
		if len(matches) != 0 {
			selected := matches[len(matches)-1]
			assignment.ManagementMode = selected.Assign.ManagementMode
			assignment.Portfolios = append([]string(nil), selected.Assign.Portfolios...)
			assignment.PolicyProfiles = append([]string(nil), selected.Assign.PolicyProfiles...)
			assignment.RolloutRing = selected.Assign.RolloutRing
			assignment.MatchedSelector = selected.Selector.ID
		} else {
			if repository.Fork {
				assignment.Portfolios = []string{owner.Classification.ForkPortfolio}
			} else {
				assignment.Portfolios = []string{owner.Classification.SourcePortfolio}
			}
		}
		sort.Strings(assignment.Portfolios)
		sort.Strings(assignment.PolicyProfiles)
		result.Repositories = append(result.Repositories, assignment)
	}
	sort.Slice(result.Repositories, func(left, right int) bool {
		return result.Repositories[left].ProviderID < result.Repositories[right].ProviderID
	})
	sortFindings(findings)
	return result, findings
}

func sortFindings(findings []domain.Finding) {
	sort.SliceStable(findings, func(left, right int) bool {
		if findings[left].Code != findings[right].Code {
			return findings[left].Code < findings[right].Code
		}
		return fmt.Sprint(findings[left].Evidence) < fmt.Sprint(findings[right].Evidence)
	})
}

func matchedSelectors(selectors []Selector, ownerID string, repository ObservedRepository) []Selector {
	matches := []Selector{}
	for _, selector := range selectors {
		if selector.Match.Owner != ownerID {
			continue
		}
		if len(selector.Match.NamePrefixes) != 0 &&
			!matchesAnyNamePrefix(repository.Name, selector.Match.NamePrefixes) {
			continue
		}
		if selector.Match.Fork != nil && *selector.Match.Fork != repository.Fork {
			continue
		}
		if selector.Match.Archived != nil && *selector.Match.Archived != repository.Archived {
			continue
		}
		if len(selector.Match.Visibility) != 0 &&
			!containsString(selector.Match.Visibility, repository.Visibility) {
			continue
		}
		matches = append(matches, selector)
	}
	sort.Slice(matches, func(left, right int) bool {
		if matches[left].Selector.Priority != matches[right].Selector.Priority {
			return matches[left].Selector.Priority < matches[right].Selector.Priority
		}
		return matches[left].Selector.ID < matches[right].Selector.ID
	})
	return matches
}

// equalPriorityConflict reports whether any two adjacent selectors in the
// priority-sorted match list share the same priority, returning the pair of
// selector IDs (sorted for stable evidence) for the first conflicting pair.
func equalPriorityConflict(matches []Selector) (bool, []string) {
	for index := 1; index < len(matches); index++ {
		if matches[index].Selector.Priority == matches[index-1].Selector.Priority {
			conflictIDs := []string{matches[index-1].Selector.ID, matches[index].Selector.ID}
			sort.Strings(conflictIDs)
			return true, conflictIDs
		}
	}
	return false, nil
}

func matchesAnyNamePrefix(name string, prefixes []string) bool {
	lowered := strings.ToLower(name)
	for _, prefix := range prefixes {
		if strings.HasPrefix(lowered, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
