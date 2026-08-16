package githubruntime

import (
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPathUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath error=%v", err)
	}
	want := filepath.Clean("/custom/config/github-device-sync/github-runtime.yaml")
	if path != want {
		t.Fatalf("path=%q want=%q", path, want)
	}
}

func TestDefaultPathFallsBackToHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath error=%v", err)
	}
	want := filepath.Join(home, ".config", "github-device-sync", "github-runtime.yaml")
	if path != want {
		t.Fatalf("path=%q want=%q", path, want)
	}
}

func TestDefaultPathRejectsRelativeXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	if _, err := DefaultPath(); err == nil ||
		!strings.Contains(err.Error(), "XDG_CONFIG_HOME must be an absolute path") {
		t.Fatalf("expected absolute-path error, got err=%v", err)
	}
}

// An omitted --runtime-config must resolve the documented default inside the
// loader itself. Before this was centralized, callers that passed the empty
// string straight through reached privateconfig.Read("") and failed with an
// unavailable runtime, so the same flag behaved differently depending on which
// command was invoked.
func TestLoadResolvesTheDefaultPathWhenOmitted(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	expected, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}

	_, loadErr := Load("", estate.Config{}, nil)
	if loadErr == nil {
		t.Fatal("expected an error for a default path that does not exist")
	}
	// The error must name the resolved default, proving the empty path was
	// defaulted rather than passed through as an empty string.
	if !strings.Contains(loadErr.Error(), filepath.Base(expected)) &&
		!strings.Contains(loadErr.Error(), expected) {
		t.Fatalf("error does not reference the resolved default %q: %v", expected, loadErr)
	}
}
