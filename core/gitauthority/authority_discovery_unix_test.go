//go:build darwin || linux

package gitauthority

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryRejectsUserOwnedExecutableAuthority(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the test process is root")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "git")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(executable); err != nil {
		t.Fatalf("explicit test authority should accept its owner: %v", err)
	}
	if err := validateDiscoveryPath(executable); err == nil {
		t.Fatal("production discovery accepted a user-owned Git executable")
	}
}
