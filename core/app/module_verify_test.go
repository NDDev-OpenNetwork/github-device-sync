package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A failing command may be a defect in the module or a tool missing from this
// device, and nothing in the exit code separates those. The diagnostic is the
// whole difference between a usable report and a guess.
func TestDeclaredCommandReportsWhyItFailed(t *testing.T) {
	t.Parallel()
	report := runDeclaredCommand(
		context.Background(), t.TempDir(),
		"echo 'the reason' >&2; exit 3", 30*time.Second,
	)
	if report.Status != "failed" || report.ExitCode != 3 {
		t.Fatalf("report = %#v", report)
	}
	if !strings.Contains(report.Diagnostic, "the reason") {
		t.Fatalf("diagnostic = %q", report.Diagnostic)
	}
}

func TestDeclaredCommandPassesWithoutADiagnostic(t *testing.T) {
	t.Parallel()
	report := runDeclaredCommand(context.Background(), t.TempDir(), "true", 30*time.Second)
	if report.Status != "passed" || report.ExitCode != 0 || report.Diagnostic != "" {
		t.Fatalf("report = %#v", report)
	}
}

// A hung command is not a failed one. Reporting it as failed sends whoever
// reads the result looking for a defect that is not there.
func TestDeclaredCommandSeparatesTimeoutFromFailure(t *testing.T) {
	t.Parallel()
	report := runDeclaredCommand(
		context.Background(), t.TempDir(), "sleep 30", 200*time.Millisecond,
	)
	if report.Status != "timeout" {
		t.Fatalf("report = %#v", report)
	}
}

// The command runs where it is told. Verifying the pinned checkout is the whole
// point, so a command that silently ran somewhere else would prove nothing.
func TestDeclaredCommandRunsInTheGivenDirectory(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	report := runDeclaredCommand(
		context.Background(), directory, "test \"$(pwd -P)\" = \"$(cd '"+directory+"' && pwd -P)\"",
		30*time.Second,
	)
	if report.Status != "passed" {
		t.Fatalf("report = %#v", report)
	}
}

// stderr lands in a result envelope with a size contract, and a failing build
// prints its reason last.
func TestBoundedDiagnosticKeepsTheTail(t *testing.T) {
	t.Parallel()
	if boundedDiagnostic("   ") != "" {
		t.Fatal("blank stderr produced a diagnostic")
	}
	long := strings.Repeat("noise line\n", 400) + "the reason"
	bounded := boundedDiagnostic(long)
	if len(bounded) > 1210 {
		t.Fatalf("diagnostic is %d bytes", len(bounded))
	}
	if !strings.HasSuffix(bounded, "the reason") || !strings.HasPrefix(bounded, "...") {
		t.Fatalf("diagnostic = %q", bounded[:40])
	}
}
