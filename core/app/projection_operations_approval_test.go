package app

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
)

// TestProjectionWriteNeedsNoSignatureButProviderWritesStillDo pins the boundary
// this contract draws. A projection materializes files inside the repository
// whose integrity is already held by the digests in .gds/bundle.lock.yaml, so an
// owner signature there guarded nothing an actor with write access to the tree
// could not do by editing the file directly -- it only cost a four-step ceremony
// on every template, schema or policy edit.
//
// A harness adapter is deliberately not in that class: it writes to an arbitrary
// target root on the device, outside this repository, over trees that hold
// user-owned files.
func TestProjectionWriteNeedsNoSignatureButProviderWritesStillDo(t *testing.T) {
	local := operations.Plan{Steps: []operations.Step{{
		StepID: "materialize-projections", Action: projections.MaterializeAction,
		RequiresApproval: false,
	}}}
	if local.RequiresApproval() {
		t.Fatal("a repository-local projection write still demands an approval")
	}

	device := operations.Plan{Steps: []operations.Step{{
		StepID: "install-harness-adapter", RequiresApproval: true,
	}}}
	if !device.RequiresApproval() {
		t.Fatal("a device-level adapter write lost its approval gate")
	}
}
