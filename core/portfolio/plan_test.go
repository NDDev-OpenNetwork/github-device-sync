package portfolio

import (
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestBuildPortfolioPlanPreservesIndependentBlockedSubplan(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	plan, findings := Build(BuildInput{
		PlanID:    "plan_01KX7BV07RHD6KRA4Z4J0KCHGR",
		CreatedAt: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
		Portfolio: "portfolio:personal-projects", Operation: "repository-change",
		Intent: "Update one repository contract independently.",
		Subplans: []Subplan{
			{
				RepositoryID: "repo_01JEXAMPZ0000000000000000D", Path: "/work/ready", Status: "ready",
				HeadOID:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ManifestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				PolicyDigest:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				FindingCodes:   []string{},
			},
			{
				RepositoryID: "repo_01JEXAMPZ0000000000000000E", Path: "/work/blocked",
				Status: "blocked", FindingCodes: []string{"GDS_PORTFOLIO_REPOSITORY_DIRTY"},
			},
		},
	}, schemas)
	if len(findings) != 0 || plan.ReadyCount != 1 || plan.BlockedCount != 1 ||
		plan.PlanDigest == "" || plan.Subplans[0].SubplanDigest == "" {
		t.Fatalf("plan=%#v findings=%#v", plan, findings)
	}
}

func TestPortfolioPlanUsesDependencyOrderAndRejectsUnprovenAutomaticCompensation(t *testing.T) {
	schemas, _ := validation.NewSchemaSet()
	base := BuildInput{PlanID: "plan_01KX7BV07RHD6KRA4Z4J0KCHGX", CreatedAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
		Portfolio: "portfolio:organization-projects", Operation: "repository-change", Intent: "Producer before consumer.",
		Subplans: []Subplan{
			{RepositoryID: "repo_01JEXAMPZ0000000000000000H", Path: "/consumer", Status: "ready", HeadOID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ManifestDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", FindingCodes: []string{}, DependsOn: []string{"repo_01JEXAMPZ0000000000000000G"}},
			{RepositoryID: "repo_01JEXAMPZ0000000000000000G", Path: "/producer", Status: "ready", HeadOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ManifestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FindingCodes: []string{}, Compensation: Compensation{Mode: "automatic", Action: "restore", Reversible: true, Idempotent: true}},
		}}
	plan, findings := Build(base, schemas)
	if len(findings) != 0 || plan.Subplans[0].RepositoryID != "repo_01JEXAMPZ0000000000000000G" {
		t.Fatalf("plan=%#v findings=%#v", plan, findings)
	}
	base.Subplans[1].Compensation.Idempotent = false
	if _, findings := Build(base, schemas); len(findings) != 1 || findings[0].Code != "GDS_PORTFOLIO_COMPENSATION_UNPROVEN" {
		t.Fatalf("findings=%#v", findings)
	}
}
