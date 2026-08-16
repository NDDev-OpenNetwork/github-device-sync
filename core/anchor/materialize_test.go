package anchor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestHandlerMaterializesAndVerifiesExactAnchor(t *testing.T) {
	root := t.TempDir()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "schemas", "v1", "valid-repository.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, findings := DecodeCandidate("candidate.yaml", raw, schemas)
	if len(findings) != 0 {
		t.Fatalf("candidate findings=%+v", findings)
	}
	before, err := Observe(root)
	if err != nil || before.File.State != "missing" {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	step := operations.Step{
		StepID: "anchor", RepositoryID: candidate.Anchor.Repository.ID,
		Action: MaterializeAction, Parameters: Parameters(root, before.File, candidate),
	}
	handler, err := NewHandler(schemas)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := handler.Apply(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(evidence.After)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Verify(context.Background(), step, after); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerRejectsChangedAndSymlinkedAnchor(t *testing.T) {
	root := t.TempDir()
	schemas, _ := validation.NewSchemaSet()
	raw, _ := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "schemas", "v1", "valid-repository.yaml"))
	candidate, _ := DecodeCandidate("candidate.yaml", raw, schemas)
	before, _ := Observe(root)
	if err := os.Mkdir(filepath.Join(root, ".gds"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("preserve"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".gds", "repository.yaml")); err != nil {
		t.Fatal(err)
	}
	step := operations.Step{
		StepID: "anchor", RepositoryID: candidate.Anchor.Repository.ID,
		Action: MaterializeAction, Parameters: Parameters(root, before.File, candidate),
	}
	handler, _ := NewHandler(schemas)
	if _, err := handler.Apply(context.Background(), step); err == nil {
		t.Fatal("anchor handler accepted a symlink race")
	}
	content, _ := os.ReadFile(outside)
	if string(content) != "preserve" {
		t.Fatalf("outside content changed: %q", content)
	}
}
