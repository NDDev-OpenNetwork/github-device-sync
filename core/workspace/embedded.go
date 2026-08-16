package workspace

import (
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// EmbeddedConsumer names one repository that consumes another repository as a
// Git submodule gitlink.
type EmbeddedConsumer struct {
	ConsumerID     string `json:"consumer_id"`
	GitmodulesName string `json:"gitmodules_name,omitempty"`
}

// EmbeddedConsumers returns every observed anchor that declares repositoryID as
// the target of a `git-submodule-consumer` relationship.
//
// Membership is decided by the incoming edge alone, never by the dependency's
// own `module` role: a reusable module with no consumer in this estate is
// legitimately standalone (ADR 0018), while a consumed one is not (ADR 0027).
func EmbeddedConsumers(
	anchors []domain.RepositoryAnchor,
	repositoryID string,
) []EmbeddedConsumer {
	consumers := []EmbeddedConsumer{}
	if repositoryID == "" {
		return consumers
	}
	seen := map[string]bool{}
	for _, anchor := range anchors {
		if anchor.Repository.ID == repositoryID {
			continue
		}
		for _, relationship := range matchingSubmoduleRelations(anchor, repositoryID) {
			key := anchor.Repository.ID + "\x00" + relationship.GitmodulesName
			if seen[key] {
				continue
			}
			seen[key] = true
			consumers = append(consumers, EmbeddedConsumer{
				ConsumerID:     anchor.Repository.ID,
				GitmodulesName: relationship.GitmodulesName,
			})
		}
	}
	sort.Slice(consumers, func(left, right int) bool {
		if consumers[left].ConsumerID != consumers[right].ConsumerID {
			return consumers[left].ConsumerID < consumers[right].ConsumerID
		}
		return consumers[left].GitmodulesName < consumers[right].GitmodulesName
	})
	return consumers
}

// EmbeddedOnlyFinding reports `GDS_WORKSPACE_EMBEDDED_ONLY` when a repository
// carries at least one incoming submodule-consumer edge and therefore may exist
// only as its superproject's gitlink. It returns nil when standalone placement
// is permitted.
func EmbeddedOnlyFinding(
	repositoryID string,
	consumers []EmbeddedConsumer,
) *domain.Finding {
	if len(consumers) == 0 {
		return nil
	}
	names := make([]string, 0, len(consumers))
	gitmodules := make([]string, 0, len(consumers))
	for _, consumer := range consumers {
		names = append(names, consumer.ConsumerID)
		if consumer.GitmodulesName != "" {
			gitmodules = append(gitmodules, consumer.GitmodulesName)
		}
	}
	return &domain.Finding{
		Code: "GDS_WORKSPACE_EMBEDDED_ONLY", Severity: domain.SeverityHigh,
		Message: "Repository is consumed as a Git submodule and is materialized " +
			"only as the superproject gitlink; it gets no standalone checkout.",
		Evidence: map[string]any{
			"repository_id":   repositoryID,
			"consumers":       names,
			"gitmodules_name": gitmodules,
		},
	}
}
