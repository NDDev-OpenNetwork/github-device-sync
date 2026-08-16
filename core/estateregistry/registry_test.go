package estateregistry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const (
	testDeviceID     = "device_01JEXAMPZ00000000000000000"
	testRepositoryID = "repo_01JEXAMPZ0000000000000000C"
	testAnchorDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestDefaultPathUsesXDGConfigHomeOrHomeFallback(t *testing.T) {
	home := t.TempDir()
	path, err := DefaultPath(func(string) string { return "" }, func() (string, error) {
		return home, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, ".config", "github-device-sync", FileName)
	if path != expected {
		t.Fatalf("path = %q, want %q", path, expected)
	}
	configHome := filepath.Join(home, "xdg-config")
	path, err = DefaultPath(func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return configHome
		}
		return ""
	}, func() (string, error) { return home, nil })
	if err != nil || path != filepath.Join(configHome, "github-device-sync", FileName) {
		t.Fatalf("xdg path = %q, err = %v", path, err)
	}
	if _, err := ResolvePath("relative/registration.json", func(string) string { return "" }, func() (string, error) {
		return home, nil
	}); err == nil {
		t.Fatal("relative registration path accepted")
	}
}

func TestCandidateMaterializationAndVerification(t *testing.T) {
	schemas := testSchemas(t)
	estateRoot := t.TempDir()
	path := filepath.Join(t.TempDir(), "nested", FileName)
	candidate, findings := NewCandidate(
		testDeviceID, testRepositoryID, estateRoot, testAnchorDigest, schemas,
	)
	if len(findings) != 0 {
		t.Fatalf("candidate findings = %#v", findings)
	}
	before, err := Observe(path)
	if err != nil || before.File.State != "missing" {
		t.Fatalf("before = %#v, err = %v", before, err)
	}
	parameters, err := Parameters(path, before.File, candidate)
	if err != nil {
		t.Fatal(err)
	}
	step := operations.Step{
		StepID: "register-estate", RepositoryID: testRepositoryID,
		Action: MaterializeAction, Parameters: parameters,
	}
	handler, err := NewHandler(schemas)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := handler.Apply(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, err := json.Marshal(evidence.After)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Verify(context.Background(), step, afterRaw); err != nil {
		t.Fatal(err)
	}
	loaded, findings := Load(path, schemas)
	physicalEstateRoot, err := filepath.EvalSymlinks(estateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || loaded.Digest != candidate.Digest ||
		loaded.Document.Estate.Root != physicalEstateRoot {
		t.Fatalf("loaded = %#v, findings = %#v", loaded, findings)
	}
	resolvedPath, decoded, err := StepCandidate(step, schemas)
	if err != nil || resolvedPath != path || decoded.Digest != candidate.Digest {
		t.Fatalf("step candidate path=%q candidate=%#v err=%v", resolvedPath, decoded, err)
	}
}

func TestHandlerRejectsChangedRegistrationPrecondition(t *testing.T) {
	schemas := testSchemas(t)
	estateRoot := t.TempDir()
	configRoot := t.TempDir()
	path := filepath.Join(configRoot, FileName)
	candidate, findings := NewCandidate(
		testDeviceID, testRepositoryID, estateRoot, testAnchorDigest, schemas,
	)
	if len(findings) != 0 {
		t.Fatal(findings)
	}
	before, err := Observe(path)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := Parameters(path, before.File, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unexpected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(schemas)
	_, err = handler.Apply(context.Background(), operations.Step{
		StepID: "register-estate", RepositoryID: testRepositoryID,
		Action: MaterializeAction, Parameters: parameters,
	})
	if err == nil {
		t.Fatal("changed registration precondition accepted")
	}
}

func TestObserveRejectsSymlinkedRegistration(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, FileName)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Observe(path); err == nil {
		t.Fatal("symlinked registration accepted")
	}
}

func testSchemas(t *testing.T) *validation.Set {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	return schemas
}
