package module

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func TestAddAndRemoveGitSubmoduleRelationshipRequiresMatchingTopology(t *testing.T) {
	consumer := moduleAnchor()
	consumer.Relationships = nil
	module := domain.RepositoryAnchor{
		Repository: domain.RepositoryIdentity{ID: "repo_01JEXAMPZ0000000000000000D", Roles: []string{"module"}},
		Provider:   domain.GitHubLocator{Owner: "example", Name: "module"},
	}
	topology := moduleTopology()
	updated, findings := AddGitSubmoduleRelationship(consumer, module, "module", topology)
	if len(findings) != 0 || len(updated.Relationships) != 1 ||
		updated.Relationships[0].Target != module.Repository.ID {
		t.Fatalf("updated=%+v findings=%+v", updated.Relationships, findings)
	}
	idempotent, findings := AddGitSubmoduleRelationship(updated, module, "module", topology)
	if len(findings) != 0 || len(idempotent.Relationships) != 1 {
		t.Fatalf("idempotent=%+v findings=%+v", idempotent.Relationships, findings)
	}
	_, findings = RemoveGitSubmoduleRelationship(updated, module.Repository.ID, "module", topology)
	if !hasFinding(findings, "GDS_MODULE_TOPOLOGY_STILL_PRESENT") {
		t.Fatalf("present topology findings=%+v", findings)
	}
	removed, findings := RemoveGitSubmoduleRelationship(
		updated, module.Repository.ID, "module", gitprovider.Topology{},
	)
	if len(findings) != 0 || len(removed.Relationships) != 0 {
		t.Fatalf("removed=%+v findings=%+v", removed.Relationships, findings)
	}
}

func TestAddGitSubmoduleRelationshipRejectsIdentityMismatch(t *testing.T) {
	consumer := moduleAnchor()
	consumer.Relationships = nil
	module := domain.RepositoryAnchor{
		Repository: domain.RepositoryIdentity{ID: "repo_01JEXAMPZ0000000000000000D", Roles: []string{"module"}},
		Provider:   domain.GitHubLocator{Owner: "other", Name: "module"},
	}
	_, findings := AddGitSubmoduleRelationship(consumer, module, "module", moduleTopology())
	if !hasFinding(findings, "GDS_MODULE_IDENTITY_NOT_PROVEN") {
		t.Fatalf("findings=%+v", findings)
	}
}
