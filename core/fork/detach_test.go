package fork

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func TestValidateDetachPreservesUpstreamHistoryAndAllOtherFacts(t *testing.T) {
	current := forkAnchor("maintained-patch")
	candidate := current
	candidateFork := *current.Fork
	candidateFork.Policy = "detached"
	candidate.Fork = &candidateFork
	if findings := ValidateDetach(current, candidate); len(findings) != 0 {
		t.Fatalf("findings=%#v", findings)
	}
	candidate.Fork.Upstream = domain.ForkUpstream{Provider: "github", RepositoryID: 3, Owner: "other", Name: "source"}
	findings := ValidateDetach(current, candidate)
	if len(findings) != 1 || findings[0].Code != "GDS_FORK_DETACH_SCOPE_EXCEEDED" {
		t.Fatalf("findings=%#v", findings)
	}
}
