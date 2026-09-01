package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunPrintsVersionWithoutLoadingRuntime(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := run(context.Background(), []string{"--version"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("stdout=%q stderr=%q exit=%d", stdout.String(), stderr.String(), exit)
	}
	if strings.TrimSpace(stdout.String()) != version || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunRequiresExplicitPrivateRuntime(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := run(context.Background(), nil, &stdout, &stderr); exit != 2 {
		t.Fatalf("stdout=%q stderr=%q exit=%d", stdout.String(), stderr.String(), exit)
	}
	if !strings.Contains(stderr.String(), "--runtime-config is required") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestTelemetryFlusherKeepsAbsentExporterUntyped(t *testing.T) {
	if telemetryFlusher(nil) != nil {
		t.Fatal("absent telemetry became a typed nil inside the interface")
	}
}
