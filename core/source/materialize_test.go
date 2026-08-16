package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

func TestVerificationMaterializerAppliesExactCandidate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "docs", "source-register")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	registerPath := filepath.Join(path, "sources.yaml")
	if err := os.WriteFile(registerPath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	request := VerificationRequest{
		ID: "official-source", Status: "current", VerifiedAt: "2026-07-11",
		NextReview: "2026-08-11", EvidenceRef: "review:source-001",
	}
	candidate := VerificationCandidate{
		Path: RegisterPath, SourceID: request.ID,
		ObservedDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CandidateDigest: "sha256:7aa7a5359173d05b63cfd682e3c38487f3cb4f7f1d60659fe59fab1505977d4c",
		Content:         []byte("new\n"),
	}
	materializer, err := NewVerificationMaterializer(root, candidate, request)
	if err != nil {
		t.Fatal(err)
	}
	step := operations.Step{
		Action:     MaterializeVerificationAction,
		Parameters: VerificationParameters(candidate, request),
	}
	if _, err := materializer.Apply(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Verify(context.Background(), step, nil); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(registerPath)
	if err != nil || string(content) != "new\n" {
		t.Fatalf("content = %q, err = %v", content, err)
	}
}
