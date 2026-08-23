package app

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func TestPublicModuleSelfProjectionDoesNotInheritRegisteredEstateSource(t *testing.T) {
	anchor := domain.RepositoryAnchor{}
	anchor.Repository.Roles = []string{"project", "module"}
	anchor.Classification.VisibilityContract = "public"

	if !isPublicModuleProjection(anchor) {
		t.Fatal("public module was not recognized as a local self-projection boundary")
	}
	anchor.Classification.VisibilityContract = "private"
	if isPublicModuleProjection(anchor) {
		t.Fatal("private module was allowed to detach projection policy from its estate")
	}
}
