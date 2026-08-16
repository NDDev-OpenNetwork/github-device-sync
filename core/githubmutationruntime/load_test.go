package githubmutationruntime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestLoadRequiresExactMutationCapabilitiesAndPrivateFile(t *testing.T) {
	root, desired, schemas := mutationTestEstate(t)
	path := mutationPrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-mutation-runtime.yaml",
	))
	config, err := Load(path, desired, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if config.GitHub.MinimumMutationSpacingMS != 1000 ||
		len(config.GitHub.Capabilities) != 4 {
		t.Fatalf("config=%+v", config)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, desired, schemas); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("public configuration error=%v", err)
	}
	delete(config.GitHub.Capabilities, "mutation:github-personal")
	if err := validateAgainstEstate(config, desired); err == nil {
		t.Fatal("incomplete mutation capability set was accepted")
	}
}

func TestValidateSeparationRejectsSharedAppOrSecretIdentity(t *testing.T) {
	root, desired, schemas := mutationTestEstate(t)
	readPath := mutationPrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-runtime.yaml",
	))
	mutationPath := mutationPrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-mutation-runtime.yaml",
	))
	readConfig, err := githubruntime.Load(readPath, desired, schemas)
	if err != nil {
		t.Fatal(err)
	}
	mutationConfig, err := Load(mutationPath, desired, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSeparation(readConfig, mutationConfig, desired); err != nil {
		t.Fatal(err)
	}
	sharedApp := mutationConfig
	personal := sharedApp.GitHub.Capabilities["mutation:github-personal"]
	personal.AppID = readConfig.GitHub.Installations["installation:github-personal"].AppID
	sharedApp.GitHub.Capabilities["mutation:github-personal"] = personal
	if err := ValidateSeparation(readConfig, sharedApp, desired); err == nil {
		t.Fatal("shared read/write App identity was accepted")
	}
	personal.AppID = "456789"
	mutationConfig.GitHub.Capabilities["mutation:github-personal"] = personal
	sharedSecret := mutationConfig
	sharedSecret.SecretStore.References["secret:gds/github-mutation-app/personal"] =
		readConfig.SecretStore.References["secret:gds/github-app/personal"]
	if err := ValidateSeparation(readConfig, sharedSecret, desired); err == nil {
		t.Fatal("shared read/write secret locator was accepted")
	}
}

func mutationTestEstate(t *testing.T) (string, estate.Config, *validation.Set) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	desired, findings := estate.Load(root, schemas)
	if len(findings) != 0 {
		t.Fatalf("estate findings=%+v", findings)
	}
	return root, desired, schemas
}

func mutationPrivateFixture(t *testing.T, source string) string {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), filepath.Base(source))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
