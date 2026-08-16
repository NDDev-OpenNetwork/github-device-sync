package estate

import (
	"encoding/json"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func indexedAnchor(id string, providerID int64, owner string, name string) domain.RepositoryAnchor {
	return domain.RepositoryAnchor{
		Repository:     domain.RepositoryIdentity{ID: id, Roles: []string{"project"}, Lifecycle: "active"},
		Provider:       domain.GitHubLocator{RepositoryID: providerID, Owner: owner, Name: name},
		Classification: domain.RepositoryClassification{Portfolios: []string{"portfolio:test"}},
	}
}

func TestBuildIdentityIndexIsDeterministicAndDerivesConsumers(t *testing.T) {
	moduleID := "repo_01JEXAMPZ0000000000000000C"
	consumerID := "repo_01JEXAMPZ0000000000000000D"
	module := indexedAnchor(moduleID, 1, "owner", "module")
	consumer := indexedAnchor(consumerID, 2, "owner", "consumer")
	consumer.Relationships = []domain.Relationship{{
		Type: "git-submodule-consumer", Target: moduleID, GitmodulesName: "module",
	}}
	input := []IndexedRepository{{Path: "/consumer", Anchor: consumer}, {Path: "/module", Anchor: module}}
	first, findings := BuildIdentityIndex(input, true)
	if len(findings) != 0 || len(first.Repositories) != 2 || len(first.Consumers) != 1 ||
		first.Consumers[0].Dependency != moduleID || first.Consumers[0].Consumer != consumerID {
		t.Fatalf("index=%+v findings=%+v", first, findings)
	}
	input[0], input[1] = input[1], input[0]
	second, secondFindings := BuildIdentityIndex(input, true)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if len(secondFindings) != 0 || string(firstJSON) != string(secondJSON) {
		t.Fatalf("non-deterministic index\n%s\n%s\n%+v", firstJSON, secondJSON, secondFindings)
	}
}

func TestBuildIdentityIndexDerivesWorkflowModuleConsumer(t *testing.T) {
	moduleID := "repo_01JEXAMPZ0000000000000000K"
	consumerID := "repo_01JEXAMPZ0000000000000000C"
	module := indexedAnchor(moduleID, 1289065451, "example-org", "ci-workflows")
	consumer := indexedAnchor(consumerID, 1000000001, "example-user", "github-device-sync")
	consumer.Relationships = []domain.Relationship{{
		Type: "workflow-module-consumer", Target: moduleID,
	}}
	// A ci.workflow_ref whose repository matches the module identity must be
	// accepted (no disagreement finding).
	consumer.CI = &domain.CIPolicy{
		WorkflowRef: "example-org/ci-workflows/.github/workflows/go-ci.yml@2ccb80e96f5771b6a6b4eae63a4f47e232906dc7",
	}
	index, findings := BuildIdentityIndex([]IndexedRepository{
		{Path: "/consumer", Anchor: consumer}, {Path: "/module", Anchor: module},
	}, true)
	if len(findings) != 0 {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	if len(index.Consumers) != 1 || index.Consumers[0].Dependency != moduleID ||
		index.Consumers[0].Consumer != consumerID || index.Consumers[0].Mode != "workflow-module" {
		t.Fatalf("workflow-module consumer edge not derived: %+v", index.Consumers)
	}
}

func TestBuildIdentityIndexRejectsWorkflowRefModuleMismatch(t *testing.T) {
	moduleID := "repo_01JEXAMPZ0000000000000000K"
	consumerID := "repo_01JEXAMPZ0000000000000000C"
	module := indexedAnchor(moduleID, 1289065451, "example-org", "ci-workflows")
	consumer := indexedAnchor(consumerID, 1000000001, "example-user", "github-device-sync")
	consumer.Relationships = []domain.Relationship{{
		Type: "workflow-module-consumer", Target: moduleID,
	}}
	// workflow_ref points at a DIFFERENT repository than the resolved module.
	consumer.CI = &domain.CIPolicy{
		WorkflowRef: "example-org/some-other-repo/.github/workflows/go-ci.yml@2ccb80e96f5771b6a6b4eae63a4f47e232906dc7",
	}
	_, findings := BuildIdentityIndex([]IndexedRepository{
		{Path: "/consumer", Anchor: consumer}, {Path: "/module", Anchor: module},
	}, true)
	if !estateHasFinding(findings, "GDS_IDENTITY_WORKFLOW_REF_MODULE_MISMATCH") {
		t.Fatalf("expected workflow_ref/module mismatch finding, got: %+v", findings)
	}
}

func TestBuildIdentityIndexRejectsIdentityLocatorAndMissingTargetConflicts(t *testing.T) {
	firstID := "repo_01JEXAMPZ0000000000000000C"
	secondID := "repo_01JEXAMPZ0000000000000000D"
	missingID := "repo_01JEXAMPZ0000000000000000E"
	first := indexedAnchor(firstID, 1, "Owner", "same")
	first.Relationships = []domain.Relationship{{Type: "package-consumer", Target: missingID}}
	second := indexedAnchor(secondID, 1, "owner", "SAME")
	_, findings := BuildIdentityIndex([]IndexedRepository{
		{Path: "/same", Anchor: first},
		{Path: "/same", Anchor: second},
		{Path: "/other", Anchor: first},
	}, true)
	for _, code := range []string{
		"GDS_IDENTITY_INDEX_ID_CONFLICT", "GDS_IDENTITY_INDEX_PROVIDER_CONFLICT",
		"GDS_IDENTITY_INDEX_LOCATOR_CONFLICT", "GDS_IDENTITY_INDEX_PATH_CONFLICT",
		"GDS_IDENTITY_INDEX_TARGET_MISSING",
	} {
		if !estateHasFinding(findings, code) {
			t.Fatalf("missing %s in %+v", code, findings)
		}
	}
}

func consumptionIndexed(id string, consumption []string) IndexedRepository {
	anchor := domain.RepositoryAnchor{}
	anchor.Repository.ID = id
	anchor.Provider.RepositoryID = int64(len(id))
	anchor.Provider.Owner = "example-org"
	anchor.Provider.Name = id
	if consumption != nil {
		anchor.Module = &domain.ModulePolicy{Consumption: consumption}
	}
	return IndexedRepository{Path: "/tmp/" + id, Anchor: anchor}
}

func hasIndexCode(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestIdentityIndexRejectsUndeclaredSubmoduleConsumption(t *testing.T) {
	consumer := consumptionIndexed("consumer", nil)
	consumer.Anchor.Relationships = []domain.Relationship{{
		Type: "git-submodule-consumer", Target: "module", GitmodulesName: "modules/module",
	}}
	_, findings := BuildIdentityIndex([]IndexedRepository{
		consumer, consumptionIndexed("module", []string{"runtime-service"}),
	}, false)
	if !hasIndexCode(findings, "GDS_IDENTITY_CONSUMPTION_UNDECLARED") {
		t.Fatalf("expected GDS_IDENTITY_CONSUMPTION_UNDECLARED, got %+v", findings)
	}
}

func TestIdentityIndexAcceptsBothDeclaredConsumptionModes(t *testing.T) {
	consumer := consumptionIndexed("consumer", nil)
	consumer.Anchor.Relationships = []domain.Relationship{
		{Type: "git-submodule-consumer", Target: "module", GitmodulesName: "modules/module"},
		{Type: "workflow-module-consumer", Target: "module"},
	}
	_, findings := BuildIdentityIndex([]IndexedRepository{
		consumer, consumptionIndexed("module", []string{"git-submodule", "runtime-service"}),
	}, false)
	if hasIndexCode(findings, "GDS_IDENTITY_CONSUMPTION_UNDECLARED") {
		t.Fatalf("expected no consumption finding, got %+v", findings)
	}
}

// The generic consumption check is not per-relationship policy: every typed
// consumer edge owes the module contract the mechanism it uses, or a planner
// reading module.consumption never learns this consumer exists.
func TestIdentityIndexRejectsUndeclaredPackageConsumption(t *testing.T) {
	consumer := consumptionIndexed("consumer", nil)
	consumer.Anchor.Relationships = []domain.Relationship{{
		Type: "package-consumer", Target: "module",
	}}
	_, findings := BuildIdentityIndex([]IndexedRepository{
		consumer, consumptionIndexed("module", []string{"git-submodule"}),
	}, false)
	if !hasIndexCode(findings, "GDS_IDENTITY_CONSUMPTION_UNDECLARED") {
		t.Fatalf("expected GDS_IDENTITY_CONSUMPTION_UNDECLARED, got %+v", findings)
	}
}

func TestIdentityIndexAcceptsDeclaredPackageConsumption(t *testing.T) {
	consumer := consumptionIndexed("consumer", nil)
	consumer.Anchor.Relationships = []domain.Relationship{{
		Type: "package-consumer", Target: "module",
	}}
	_, findings := BuildIdentityIndex([]IndexedRepository{
		consumer, consumptionIndexed("module", []string{"package"}),
	}, false)
	if hasIndexCode(findings, "GDS_IDENTITY_CONSUMPTION_UNDECLARED") {
		t.Fatalf("declared package consumption must pass, got %+v", findings)
	}
}
