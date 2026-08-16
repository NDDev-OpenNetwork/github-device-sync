package gitops

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func TestUpdateRemoteHandlerAppliesAndVerifiesRecordedEvidence(t *testing.T) {
	root, _ := gitFixture(t)
	expected := "https://github.com/old-owner/source.git"
	target := "https://github.com/new-owner/source.git"
	command := exec.Command("git", "remote", "add", "origin", expected)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, output)
	}
	runner, err := gitprovider.NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	handler := &UpdateRemoteHandler{Git: runner}
	step := operations.Step{
		Action:     UpdateRemoteAction,
		Parameters: RemoteUpdateParameters(root, "origin", expected, target),
	}
	evidence, err := handler.Apply(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := json.Marshal(evidence.After)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Verify(context.Background(), step, recorded); err != nil {
		t.Fatal(err)
	}
}
