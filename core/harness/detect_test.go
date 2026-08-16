package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectProfileRecordsBoundedVersionEvidence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture-harness")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'fixture 1.2.3\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	profile := CapabilityProfile{
		ID: "fixture", CapabilityVersion: "2026-07-11",
		Detection: DetectionConfig{
			CommandCandidates: []string{"fixture-harness"},
			VersionArguments:  []string{"--version"},
		},
	}
	observation, findings := detectProfile(context.Background(), profile)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if observation.Result != "observed" || observation.Version != "fixture 1.2.3" {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestNormalizeVersionRemovesControlCharactersAndCapsOutput(t *testing.T) {
	value := normalizeVersion("  version\n1.0\t" + string(make([]byte, 700)))
	if len(value) > versionValueLimit {
		t.Fatalf("version length = %d", len(value))
	}
	if value != "version 1.0" {
		t.Fatalf("version = %q", value)
	}
}
