package module

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func AddGitSubmoduleRelationship(
	consumer domain.RepositoryAnchor,
	module domain.RepositoryAnchor,
	gitmodulesName string,
	topology gitprovider.Topology,
) (domain.RepositoryAnchor, []domain.Finding) {
	if consumer.Repository.ID == module.Repository.ID || !hasRepositoryRole(module, "module") {
		return consumer, []domain.Finding{moduleLifecycleFinding(
			"GDS_MODULE_RELATIONSHIP_INVALID", "Module relationship requires a distinct repository with the module role.",
		)}
	}
	if strings.TrimSpace(gitmodulesName) == "" {
		return consumer, []domain.Finding{moduleLifecycleFinding(
			"GDS_MODULE_GITMODULES_NAME_REQUIRED", "Module relationship requires an exact .gitmodules name.",
		)}
	}
	var matched *gitprovider.Submodule
	for index := range topology.Submodules {
		submodule := &topology.Submodules[index]
		if submodule.Name == gitmodulesName {
			matched = submodule
			break
		}
	}
	if matched == nil || matched.GitlinkOID == "" || matched.GitlinkStage != 0 ||
		(matched.WorktreeState != "at-gitlink" && matched.WorktreeState != "uninitialized") {
		return consumer, []domain.Finding{moduleLifecycleFinding(
			"GDS_MODULE_GITLINK_NOT_READY", "A clean stage-zero gitlink must exist before its typed relationship is onboarded.",
		)}
	}
	observed, err := gitprovider.ParseGitHubRepository(matched.URL)
	if err != nil || !strings.EqualFold(observed.Owner, module.Provider.Owner) ||
		!strings.EqualFold(observed.Name, module.Provider.Name) {
		return consumer, []domain.Finding{moduleLifecycleFinding(
			"GDS_MODULE_IDENTITY_NOT_PROVEN", "The .gitmodules URL does not match the selected module provider identity.",
		)}
	}
	for _, relationship := range consumer.Relationships {
		if relationship.Type != "git-submodule-consumer" {
			continue
		}
		if relationship.Target == module.Repository.ID && relationship.GitmodulesName == gitmodulesName {
			return consumer, nil
		}
		if relationship.Target == module.Repository.ID || relationship.GitmodulesName == gitmodulesName {
			return consumer, []domain.Finding{moduleLifecycleFinding(
				"GDS_MODULE_RELATIONSHIP_CONFLICT", "Module identity or .gitmodules name is already bound differently.",
			)}
		}
	}
	consumer.Relationships = append(consumer.Relationships, domain.Relationship{
		Type: "git-submodule-consumer", Target: module.Repository.ID,
		GitmodulesName: gitmodulesName,
	})
	sortRelationships(consumer.Relationships)
	return consumer, nil
}

func RemoveGitSubmoduleRelationship(
	consumer domain.RepositoryAnchor,
	moduleID string,
	gitmodulesName string,
	topology gitprovider.Topology,
) (domain.RepositoryAnchor, []domain.Finding) {
	for _, submodule := range topology.Submodules {
		if submodule.Name == gitmodulesName || (submodule.Name == "" && submodule.Path == gitmodulesName) {
			return consumer, []domain.Finding{moduleLifecycleFinding(
				"GDS_MODULE_TOPOLOGY_STILL_PRESENT", "Remove the .gitmodules entry and index gitlink before retiring the typed relationship.",
			)}
		}
	}
	removed := false
	relationships := make([]domain.Relationship, 0, len(consumer.Relationships))
	for _, relationship := range consumer.Relationships {
		if relationship.Type == "git-submodule-consumer" &&
			relationship.Target == moduleID && relationship.GitmodulesName == gitmodulesName {
			removed = true
			continue
		}
		relationships = append(relationships, relationship)
	}
	if !removed {
		return consumer, []domain.Finding{moduleLifecycleFinding(
			"GDS_MODULE_RELATIONSHIP_MISSING", "The exact typed module relationship does not exist.",
		)}
	}
	consumer.Relationships = relationships
	sortRelationships(consumer.Relationships)
	return consumer, nil
}

func sortRelationships(relationships []domain.Relationship) {
	sort.Slice(relationships, func(left, right int) bool {
		leftKey := fmt.Sprintf("%s\x00%s\x00%s", relationships[left].Type, relationships[left].Target, relationships[left].GitmodulesName)
		rightKey := fmt.Sprintf("%s\x00%s\x00%s", relationships[right].Type, relationships[right].Target, relationships[right].GitmodulesName)
		return leftKey < rightKey
	})
}

func hasRepositoryRole(anchor domain.RepositoryAnchor, role string) bool {
	for _, current := range anchor.Repository.Roles {
		if current == role {
			return true
		}
	}
	return false
}

func moduleLifecycleFinding(code string, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
