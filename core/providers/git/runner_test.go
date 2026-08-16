package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func executableFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git-fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunnerRedactsErrorsAndCapsOutput(t *testing.T) {
	secret := "ghp_" + "1234567890abcdefghijklmnopqrstuv"
	path := executableFixture(t, "echo 'fatal: https://user:password@example.test token="+secret+"' >&2; exit 1")
	runner := NewRunnerForPath(path, 1024)
	_, err := runner.Run(context.Background(), t.TempDir(), "rev-parse", "--verify", "HEAD")
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "user:password") {
		t.Fatalf("git error was not redacted: %v", err)
	}

	path = executableFixture(t, "printf '01234567890123456789'")
	runner = NewRunnerForPath(path, 8)
	if _, err := runner.Run(
		context.Background(), t.TempDir(), "rev-parse", "--verify", "HEAD",
	); err == nil || !strings.Contains(err.Error(), "exceeded 8 bytes") {
		t.Fatalf("git output cap was not enforced: %v", err)
	}
}

func TestRunnerRejectsArgumentInjectionAndHonorsCancellation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	runner := NewRunnerForPath(executableFixture(t, "sleep 5"), 1024)
	malicious := "$(touch " + marker + ")"
	if _, err := runner.Run(context.Background(), t.TempDir(), "rev-parse", malicious); err == nil {
		t.Fatal("unexpected rev-parse arguments were accepted")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("argument was interpreted by a shell: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := runner.Run(ctx, t.TempDir(), "rev-parse", "--verify", "HEAD"); err == nil ||
		!strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("git cancellation was not reported: %v", err)
	}
}

func TestRemoteNamesCannotBecomeOptions(t *testing.T) {
	for _, name := range []string{"origin", "upstream", "fork-1", "team.remote"} {
		if !safeRemoteName(name) {
			t.Fatalf("safe remote %q rejected", name)
		}
	}
	for _, name := range []string{"--push", "origin/other", "origin other", "origin\nother"} {
		if safeRemoteName(name) {
			t.Fatalf("unsafe remote %q accepted", name)
		}
	}
}
