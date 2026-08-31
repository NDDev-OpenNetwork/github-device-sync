package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	gdsschemas "github.com/NDDev-OpenNetwork/github-device-sync/schemas"
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

// TestSchemaDigestIdentifiesTheEmbeddedSet covers what a version string cannot:
// the digest must name the schema revision this binary compiled, so a caller can
// tie a green validation to the schemas that produced it. A build-time version
// can name a revision that was never released, which is why the digest is
// derived from content rather than stamped.
func TestSchemaDigestIdentifiesTheEmbeddedSet(t *testing.T) {
	t.Parallel()
	first, err := NewSchemaSet()
	if err != nil {
		t.Fatalf("NewSchemaSet() error = %v", err)
	}
	digest := first.Digest()
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		t.Fatalf("Digest() = %q, want a sha256: prefix and 64 hex characters", digest)
	}
	// Determinism matters more than the value: two sets compiled from the same
	// embedded schemas must agree, or the digest identifies the run instead of
	// the schemas and is worse than printing nothing.
	second, err := NewSchemaSet()
	if err != nil {
		t.Fatalf("NewSchemaSet() second call error = %v", err)
	}
	if second.Digest() != digest {
		t.Fatalf("Digest() is not deterministic: %q then %q", digest, second.Digest())
	}
}

// TestSchemaDigestCoversEverySchemaFile guards the direction the digest exists
// to guard. A digest that skipped a file would stay identical while that file
// changed, which is the failure it is meant to make impossible.
func TestSchemaDigestCoversEverySchemaFile(t *testing.T) {
	t.Parallel()
	set, err := NewSchemaSet()
	if err != nil {
		t.Fatalf("NewSchemaSet() error = %v", err)
	}
	entries, err := fs.ReadDir(gdsschemas.V1, "v1")
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	expected := sha256.New()
	files := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		path := "v1/" + entry.Name()
		raw, err := gdsschemas.V1.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		expected.Write([]byte(path))
		expected.Write([]byte{0})
		expected.Write(raw)
		expected.Write([]byte{0})
		files++
	}
	if files == 0 {
		t.Fatal("no embedded schema files found; the digest would be vacuous")
	}
	want := "sha256:" + hex.EncodeToString(expected.Sum(nil))
	if set.Digest() != want {
		t.Fatalf("Digest() = %q, want %q over %d schema files", set.Digest(), want, files)
	}
}
