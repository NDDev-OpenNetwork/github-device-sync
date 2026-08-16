package workspace

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func consumerAnchor(id string, target string, gitmodulesName string) domain.RepositoryAnchor {
	anchor := domain.RepositoryAnchor{}
	anchor.Repository.ID = id
	anchor.Relationships = []domain.Relationship{{
		Type: "git-submodule-consumer", Target: target, GitmodulesName: gitmodulesName,
	}}
	return anchor
}

func moduleAnchor(id string) domain.RepositoryAnchor {
	anchor := domain.RepositoryAnchor{}
	anchor.Repository.ID = id
	anchor.Repository.Roles = []string{"project", "module"}
	return anchor
}

func TestEmbeddedConsumersReportsIncomingSubmoduleEdge(t *testing.T) {
	anchors := []domain.RepositoryAnchor{
		consumerAnchor("repo_super", "repo_module", "modules/ci-workflows"),
		moduleAnchor("repo_module"),
	}
	consumers := EmbeddedConsumers(anchors, "repo_module")
	if len(consumers) != 1 {
		t.Fatalf("expected one consumer, got %d", len(consumers))
	}
	if consumers[0].ConsumerID != "repo_super" ||
		consumers[0].GitmodulesName != "modules/ci-workflows" {
		t.Fatalf("unexpected consumer: %+v", consumers[0])
	}
	finding := EmbeddedOnlyFinding("repo_module", consumers)
	if finding == nil || finding.Code != "GDS_WORKSPACE_EMBEDDED_ONLY" {
		t.Fatalf("expected GDS_WORKSPACE_EMBEDDED_ONLY, got %+v", finding)
	}
}

func TestEmbeddedConsumersIgnoresModuleRoleWithoutConsumer(t *testing.T) {
	// ADR 0018 keeps an unconsumed reusable module standalone-eligible; only an
	// actual incoming edge makes it embedded-only.
	anchors := []domain.RepositoryAnchor{moduleAnchor("repo_module")}
	if consumers := EmbeddedConsumers(anchors, "repo_module"); len(consumers) != 0 {
		t.Fatalf("expected no consumers, got %+v", consumers)
	}
	if finding := EmbeddedOnlyFinding("repo_module", nil); finding != nil {
		t.Fatalf("expected no finding, got %+v", finding)
	}
}

func TestEmbeddedConsumersIgnoresOtherRelationshipTypes(t *testing.T) {
	anchor := domain.RepositoryAnchor{}
	anchor.Repository.ID = "repo_super"
	anchor.Relationships = []domain.Relationship{
		{Type: "workflow-module-consumer", Target: "repo_module"},
		{Type: "package-consumer", Target: "repo_module"},
	}
	if consumers := EmbeddedConsumers(
		[]domain.RepositoryAnchor{anchor}, "repo_module",
	); len(consumers) != 0 {
		t.Fatalf("expected no consumers, got %+v", consumers)
	}
}

func TestAuditLayoutRefusesStandaloneEmbeddedOnlyCheckout(t *testing.T) {
	descriptor := DeviceDescriptor{}
	descriptor.Device.ID = "device_test"
	descriptor.WorkspaceRoots = map[string]string{"nddev": "${HOME}/Developer/nddev"}
	descriptor.Materialization = MaterializationPolicy{
		DefaultMode: "absent",
		Include: []MaterializationAssignment{{
			Selector: "portfolio:organization-projects", WorkspaceRoot: "nddev", Mode: "active",
		}},
	}
	descriptor.State.Path = "${XDG_STATE_HOME}/gds"

	module := moduleAnchor("repo_module")
	module.Provider.Name = "ci-workflows"
	module.Classification.Portfolios = []string{"portfolio:organization-projects"}

	report, findings := AuditLayout(
		descriptor,
		Environment{Home: "/srv/device", XDGStateHome: "/srv/device/.local/state"},
		[]LayoutRepository{
			{Path: "/srv/device/Developer/control-plane/github-device-sync",
				Anchor: consumerAnchor("repo_super", "repo_module", "modules/ci-workflows")},
			{Path: "/srv/device/Developer/nddev/ci-workflows", Anchor: module},
		},
	)
	found := false
	for _, finding := range findings {
		if finding.Code == "GDS_WORKSPACE_EMBEDDED_ONLY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected GDS_WORKSPACE_EMBEDDED_ONLY, got %+v", findings)
	}
	if report.Invalid == 0 {
		t.Fatalf("expected the embedded-only checkout to count as invalid")
	}
}
