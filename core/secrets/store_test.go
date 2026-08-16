package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnvironmentStoreRequiresExplicitReferenceMapping(t *testing.T) {
	store := EnvironmentStore{
		References: map[string]string{"github-app-key": "TEST_GDS_KEY"},
		LookupEnv: func(name string) (string, bool) {
			if name == "TEST_GDS_KEY" {
				return "secret\n", true
			}
			return "", false
		},
	}
	value, err := store.Get(context.Background(), "github-app-key")
	if err != nil || string(value) != "secret" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if _, err := store.Get(context.Background(), "TEST_GDS_KEY"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unmapped reference error=%v", err)
	}
}

func TestFileStoreRequiresPrivateStableRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-key.pem")
	if err := os.WriteFile(path, []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := FileStore{References: map[string]string{"key": path}}
	value, err := store.Get(context.Background(), "key")
	if err != nil || string(value) != "private" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("public mode error=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink error=%v", err)
	}
}

func TestMacOSKeychainStoreUsesFixedArgumentVector(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS keychain adapter")
	}
	runner := &recordingRunner{value: []byte("secret\n")}
	store := MacOSKeychainStore{
		Service: "github-device-sync", Accounts: map[string]string{"key": "inventory-app"},
		Runner: runner,
	}
	value, err := store.Get(context.Background(), "key")
	if err != nil || string(value) != "secret" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	expected := []string{
		"security", "find-generic-password", "-w", "-s", "github-device-sync", "-a", "inventory-app",
	}
	if len(runner.arguments) != len(expected) {
		t.Fatalf("arguments=%v", runner.arguments)
	}
	for index := range expected {
		if runner.arguments[index] != expected[index] {
			t.Fatalf("arguments=%v", runner.arguments)
		}
	}
}

func TestCommandEnvironmentUsesAnExplicitSafeAllowlist(t *testing.T) {
	t.Setenv("HOME", "/tmp/gds-home")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/tmp/gds-dbus")
	t.Setenv("LD_PRELOAD", "/tmp/untrusted.so")
	environment := commandEnvironment()
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "HOME=/tmp/gds-home") ||
		!strings.Contains(joined, "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/gds-dbus") {
		t.Fatalf("required environment is missing: %v", environment)
	}
	if strings.Contains(joined, "LD_PRELOAD") {
		t.Fatalf("unsafe environment was inherited: %v", environment)
	}
}

func TestCredentialHelperResolutionIgnoresAmbientPath(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "executed")
	name := "security"
	if runtime.GOOS == "linux" {
		name = "secret-tool"
	}
	malicious := filepath.Join(directory, name)
	if err := os.WriteFile(malicious, []byte("#!/bin/sh\ntouch \""+marker+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	resolved, err := credentialHelperPath(name)
	if err == nil && resolved == malicious {
		t.Fatal("credential helper was resolved from the ambient PATH")
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ambient credential helper executed: %v", err)
	}
}

type recordingRunner struct {
	value     []byte
	arguments []string
}

func (runner *recordingRunner) Run(_ context.Context, executable string, arguments ...string) ([]byte, error) {
	runner.arguments = append([]string{executable}, arguments...)
	return normalize(runner.value)
}
