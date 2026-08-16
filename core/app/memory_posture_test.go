package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func postureRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

// A module in this estate declares Serena off, which is what makes the opt-out
// worth having rather than hypothetical. Derived from the tracked anchors so it
// cannot drift into agreement with a regression.
func TestAtLeastOneDeclaredModuleOptsOutOfSerena(t *testing.T) {
	t.Parallel()
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(postureRepositoryRoot(t), "modules")
	entries, err := os.ReadDir(modules)
	if err != nil {
		t.Skipf("no module tree on this device: %v", err)
	}
	optedOut := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(modules, entry.Name())
		if _, statErr := os.Stat(filepath.Join(path, ".gds", "repository.yaml")); statErr != nil {
			continue // not checked out on this device
		}
		if !services.serenaPosture(path).Enabled {
			optedOut++
		}
	}
	if optedOut == 0 {
		t.Skip("no checked-out module declares agent.serena.enabled: false")
	}
}
