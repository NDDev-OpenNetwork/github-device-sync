package module

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func moduleAnchor() domain.RepositoryAnchor {
	return domain.RepositoryAnchor{
		Repository: domain.RepositoryIdentity{ID: "repo_01JEXAMPZ0000000000000000C"},
		Provider:   domain.GitHubLocator{Owner: "example", Name: "consumer"},
		Relationships: []domain.Relationship{{
			Type: "git-submodule-consumer", Target: "repo_01JEXAMPZ0000000000000000D",
			GitmodulesName: "module",
		}},
	}
}

func moduleTopology() gitprovider.Topology {
	return gitprovider.Topology{
		Remotes: []gitprovider.Remote{{
			Name: "origin",
			FetchURLs: []gitprovider.RemoteURL{{
				Value: "https://github.com/example/consumer.git",
			}},
		}},
		Submodules: []gitprovider.Submodule{{
			Name: "module", Path: "modules/module",
			URL:           "https://github.com/example/module.git",
			GitlinkOID:    "0123456789abcdef0123456789abcdef01234567",
			CurrentOID:    "0123456789abcdef0123456789abcdef01234567",
			WorktreeState: "at-gitlink",
		}},
	}
}

func TestInspectValidTypedGitlink(t *testing.T) {
	t.Parallel()
	report, findings := Inspect(moduleAnchor(), moduleTopology())
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if len(report.Relationships) != 1 || report.Relationships[0].Path != "modules/module" ||
		report.Relationships[0].IdentityVerification != "not-proven-without-estate-index" {
		t.Fatalf("report = %#v", report)
	}
}

func TestInspectDetectsMissingRelationshipAndOffPinState(t *testing.T) {
	t.Parallel()
	anchor := moduleAnchor()
	anchor.Relationships = nil
	topology := moduleTopology()
	topology.Submodules[0].CurrentOID = "1123456789abcdef0123456789abcdef01234567"
	topology.Submodules[0].WorktreeState = "off-gitlink"
	_, findings := Inspect(anchor, topology)
	if !hasFinding(findings, "GDS_GITLINK_RELATIONSHIP_MISSING") ||
		!hasFinding(findings, "GDS_GITLINK_WORKTREE_DRIFT") {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestInspectDetectsCredentialBearingURLs(t *testing.T) {
	t.Parallel()
	topology := moduleTopology()
	topology.Remotes[0].FetchURLs[0].CredentialsRedacted = true
	topology.Submodules[0].URLRedacted = true
	_, findings := Inspect(moduleAnchor(), topology)
	if !hasFinding(findings, "GDS_REMOTE_CREDENTIALS_PRESENT") ||
		!hasFinding(findings, "GDS_GITLINK_URL_CREDENTIALS_PRESENT") {
		t.Fatalf("findings = %#v", findings)
	}
}

func hasFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
