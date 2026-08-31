package app

import (
	"context"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// TestModuleCoverageOverNothingIsNotProven covers the case the command was
// silent about. An estate whose repositories are not declared as
// git-submodule-consumer relationships gets an empty module set, and reporting
// success there is indistinguishable from every module being covered -- while
// the estates most likely to hit it are exactly the ones asking whether their
// gates are watched at all.
//
// This runs against the external estate fixture rather than the developer's own
// machine. A first version of this test lived in core/cli and passed locally
// only because a registered estate happened to be configured there; in CI the
// command failed earlier at GDS_POLICY_ESTATE_NOT_PROVEN and never reached this
// path, so it was green over the wrong refusal.
func TestModuleCoverageOverNothingIsNotProven(t *testing.T) {
	root := appTestRepositoryRoot(t)
	runtimePath := appTestRuntimeConfig(t, root)
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	envelope := services.CoverModules(context.Background(), root, ModuleCoverageOptions{
		GitHubReadOptions: GitHubReadOptions{RuntimeConfig: runtimePath},
	})
	if envelope.ExitClass == domain.ExitSuccess {
		t.Fatalf("coverage over an empty module set reported success: %#v", envelope)
	}
	found := false
	for _, finding := range envelope.Findings {
		if finding.Code == "GDS_MODULE_COVERAGE_SCOPE_NOT_PROVEN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no GDS_MODULE_COVERAGE_SCOPE_NOT_PROVEN finding: %#v", envelope)
	}
}
