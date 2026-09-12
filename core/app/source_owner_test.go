package app

import (
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"testing"
)

func TestSourceVerificationUsesTheCanonicalRegisterOwner(t *testing.T) {
	for _, tc := range []struct {
		name       string
		roles      []string
		visibility string
		allowed    bool
	}{
		{"private control plane", []string{"control-plane"}, "private", true},
		{"public engine module", []string{"project", "module"}, "public", true},
		{"ordinary public project", []string{"project"}, "public", false},
		{"private consumer module", []string{"module"}, "private", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var anchor domain.RepositoryAnchor
			anchor.Repository.Roles = tc.roles
			anchor.Classification.VisibilityContract = tc.visibility
			finding := requireSourceOwnerRole(anchor)
			if (finding == nil) != tc.allowed {
				t.Fatalf("allowed=%v finding=%#v", tc.allowed, finding)
			}
		})
	}
}
