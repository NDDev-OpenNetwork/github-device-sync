package validation

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

func TestEmbeddedSchemasCompile(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatalf("NewSchemaSet() error = %v", err)
	}
	want := map[string]bool{
		"approval": true, "trust-policy": true, "plan-enablement": true,
		"freshness-policy": true, "field-ownership": true,
		"device-evidence": true, "delegated-harness-evidence": true, "harness-runtime-manifest": true,
		"common": true, "repository": true, "estate": true, "policy": true,
		"harness-profile": true, "harness-registry": true, "module-harness-bridge": true,
		"device": true, "plan": true,
		"estate-registration":      true,
		"harness-runtime-contract": true, "memory-metadata": true,
		"operation-result": true, "migration-registry": true,
		"installation": true, "owner": true, "selector": true,
		"source-register":        true,
		"release-installation":   true,
		"rollback-authorization": true,
	}
	for _, name := range set.Names() {
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing compiled schemas: %v", want)
	}
}

func TestCanonicalSchemasAndFixturesPass(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatalf("NewSchemaSet() error = %v", err)
	}
	root := repositoryRoot(t)
	findings := set.ValidateCanonical(
		root,
		filepath.Join(root, "tests", "fixtures", "schemas", "v1", "cases.json"),
	)
	if len(findings) != 0 {
		t.Fatalf("ValidateCanonical() findings = %#v", findings)
	}
}

func TestEnvelopeConformsToOperationResultSchema(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatalf("NewSchemaSet() error = %v", err)
	}
	envelope := domain.Success("gds validate schemas", map[string]any{"schemas": 9})
	findings := set.Validate("operation-result", toJSONValue(t, envelope), "envelope")
	if len(findings) != 0 {
		t.Fatalf("envelope findings = %#v", findings)
	}
}

func TestECMARegexpIsSafeUnderConcurrentValidation(t *testing.T) {
	matcher, err := compileECMARegexp(
		`^(?!/)(?!.*(?:^|/)\.\.(?:/|$))[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$`,
	)
	if err != nil {
		t.Fatal(err)
	}
	var failures atomic.Int32
	var wait sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for attempt := 0; attempt < 1000; attempt++ {
				if !matcher.MatchString("policies/base/repository-default.yaml") {
					failures.Add(1)
				}
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 {
		t.Fatalf("valid safe relative path was rejected %d times", failures.Load())
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func toJSONValue(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	decoded, err := serialization.Decode("value.json", raw)
	if err != nil {
		t.Fatalf("serialization.Decode() error = %v", err)
	}
	return decoded
}
