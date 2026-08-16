package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func repositoryRootForMemory(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func TestDigestSourcesIsOrderIndependent(t *testing.T) {
	root := t.TempDir()
	for name, value := range map[string]string{"a.txt": "a\n", "b.txt": "b\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	left, err := DigestSources(root, []string{"a.txt", "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	right, err := DigestSources(root, []string{"b.txt", "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("digests differ: %s %s", left, right)
	}
}

func TestValidateDetectsMemorySourceDrift(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".serena", "memories"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.md"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	memory := fmt.Sprintf(`---
gds_memory_schema: 1
scope_id: "repo_01JEXAMPZ0000000000000000C"
status: "generated-unverified"
visibility: "private"
source_commit: "433c46b6923f7dc1efb96713b9ffc9330ca8ba58"
source_state: "working-tree"
source_digest: "sha256:%s"
generated_by: "gds-memory-compiler"
bundle_version: "0.1.0-dev"
verified_at: "2026-07-11T12:00:00Z"
refresh_triggers: ["source-change"]
sources: ["source.md"]
---
# Fixture

## Purpose

Fixture.

## Invariants

Fixture.

## Sources

Fixture.

## Refresh

Fixture.
`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.WriteFile(
		filepath.Join(root, ".serena", "memories", "fixture.md"), []byte(memory), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	_, findings := Validate(root, schemas)
	found := false
	for _, finding := range findings {
		if finding.Code == "GDS_MEMORY_SOURCE_DIGEST_MISMATCH" {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestGenerateBuildsDeterministicCandidateWithoutWriting(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".serena", "memories"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.md"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := DigestSources(root, []string{"source.md"})
	if err != nil {
		t.Fatal(err)
	}
	memory := fmt.Sprintf(`---
gds_memory_schema: 1
scope_id: "repo_01JEXAMPZ0000000000000000C"
status: "verified"
visibility: "private"
source_commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
source_state: "committed"
source_digest: %q
generated_by: "gds-memory-compiler"
bundle_version: "0.1.0-dev"
verified_at: "2026-07-11T12:00:00Z"
refresh_triggers: ["source-change"]
sources: ["source.md"]
---
# Fixture

## Purpose

Fixture.

## Invariants

Fixture.

## Sources

Fixture.

## Refresh

Fixture.
`, digest)
	path := filepath.Join(root, ".serena", "memories", "fixture.md")
	if err := os.WriteFile(path, []byte(memory), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	commit := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	first, findings := Generate(root, "fixture", commit, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	second, findings := Generate(root, "fixture.md", commit, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if first.OutputDigest != second.OutputDigest || first.Content != second.Content {
		t.Fatalf("candidate is not deterministic: first=%#v second=%#v", first, second)
	}
	if first.Metadata.Status != "generated-unverified" ||
		first.Metadata.SourceCommit != commit || first.Metadata.SourceState != "committed" {
		t.Fatalf("candidate metadata = %#v", first.Metadata)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("candidate generation changed the source memory")
	}
}

func TestVerifiedAtOrderingFindings(t *testing.T) {
	committed := time.Date(2026, 7, 21, 3, 36, 38, 0, time.UTC)
	resolve := func(commit string) (time.Time, bool) {
		if commit == "known" {
			return committed, true
		}
		return time.Time{}, false
	}
	path := ".serena/memories/x.md"

	// verified_at earlier than the source commit -> impossible-order finding.
	before := Metadata{Status: "verified", SourceCommit: "known", VerifiedAt: "2026-07-18T01:42:53Z"}
	got := verifiedAtOrderingFindings(path, before, resolve)
	if len(got) != 1 || got[0].Code != "GDS_MEMORY_VERIFIED_AT_PRECEDES_SOURCE" {
		t.Fatalf("expected precedes-source finding, got %+v", got)
	}

	// verified_at at/after the source commit -> accepted.
	after := Metadata{Status: "verified", SourceCommit: "known", VerifiedAt: "2026-07-21T04:00:00Z"}
	if got := verifiedAtOrderingFindings(path, after, resolve); len(got) != 0 {
		t.Fatalf("expected no finding for later verified_at, got %+v", got)
	}

	// Unresolvable commit (shallow clone) -> skipped, not failed closed.
	unknown := Metadata{Status: "verified", SourceCommit: "missing", VerifiedAt: "2026-07-18T01:42:53Z"}
	if got := verifiedAtOrderingFindings(path, unknown, resolve); len(got) != 0 {
		t.Fatalf("expected skip for unresolvable commit, got %+v", got)
	}

	// Not human-verified -> outside the check's scope.
	generated := Metadata{Status: "generated-unverified", SourceCommit: "known", VerifiedAt: "2026-07-18T01:42:53Z"}
	if got := verifiedAtOrderingFindings(path, generated, resolve); len(got) != 0 {
		t.Fatalf("expected skip for non-verified memory, got %+v", got)
	}
}

func newTestSchemas(t *testing.T) *validation.Set {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatalf("schema set: %v", err)
	}
	return schemas
}

func TestVerifyRefusesAMemoryItCannotGenerate(t *testing.T) {
	// Verify exists so the honest action is a command rather than an edit to a
	// digest-bearing file. It must therefore refuse everything Generate
	// refuses: a memory that cannot be brought current must not become
	// verified, or the command becomes a way to launder one.
	committed := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	_, findings := Verify(
		t.TempDir(), "does-not-exist", "abc", committed, committed.Add(time.Hour),
		newTestSchemas(t),
	)
	if len(findings) == 0 {
		t.Fatal("verify labelled a memory that could not be generated")
	}
}

func TestVerifyRefusesToPredateTheSourceCommit(t *testing.T) {
	// The validator rejects verified_at before the source commit. The command
	// that writes it must reject the same thing, or it would emit a file its
	// own validator refuses.
	committed := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	_, findings := Verify(
		t.TempDir(), "any", "abc", committed, committed.Add(-time.Second),
		newTestSchemas(t),
	)
	if len(findings) != 1 || findings[0].Code != "GDS_MEMORY_VERIFIED_AT_PRECEDES_SOURCE" {
		t.Fatalf("expected the ordering refusal, got %#v", findings)
	}
}

func TestVerifyRefusesATimestampEqualToTheSourceCommit(t *testing.T) {
	// Equal is not after. A verification claiming the same instant as the
	// commit it read has not demonstrated it happened afterwards.
	committed := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	_, findings := Verify(
		t.TempDir(), "any", "abc", committed, committed, newTestSchemas(t),
	)
	if len(findings) != 1 || findings[0].Code != "GDS_MEMORY_VERIFIED_AT_PRECEDES_SOURCE" {
		t.Fatalf("expected the ordering refusal for an equal timestamp, got %#v", findings)
	}
}
