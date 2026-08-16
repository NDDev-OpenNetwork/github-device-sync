package source

import (
	"bytes"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestEvaluateClassifiesAndSortsSources(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	report := Evaluate(Register{Sources: []Entry{
		{ID: "z-current", Status: "current", NextReview: "2026-08-11", ContentDigest: &digest},
		{ID: "missing-digest", Status: "current", NextReview: "2026-08-11"},
		{ID: "overdue", Status: "current", NextReview: "2026-07-10", ContentDigest: &digest},
		{ID: "blocked", Status: "changed-unreviewed", NextReview: "2026-08-11", ContentDigest: &digest},
	}}, time.Date(2026, 7, 11, 12, 0, 0, 0, time.FixedZone("test", 7*60*60)))

	if report.AsOf != "2026-07-11" || report.Count != 4 ||
		report.Summary.Current != 1 || report.Summary.NotProven != 1 ||
		report.Summary.Overdue != 1 || report.Summary.Blocked != 1 {
		t.Fatalf("report = %#v", report)
	}
	if report.Items[0].ID != "blocked" || report.Items[3].ID != "z-current" {
		t.Fatalf("items are not sorted: %#v", report.Items)
	}
}

func TestClassifyDoesNotBlockOnRuntimeNotProvenWithPinnedDigest(t *testing.T) {
	// Runtime proof is owned by the isolated per-harness setup systems, not the
	// control plane: a docs-verified claim with a pinned digest is release-current
	// here even while its runtime remains unproven. A missing digest still blocks.
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	report := Evaluate(Register{Sources: []Entry{
		{ID: "runtime-not-proven-pinned", Status: "current-docs-verified-runtime-not-proven", NextReview: "2026-08-11", ContentDigest: &digest},
		{ID: "runtime-not-proven-unpinned", Status: "release-checksums-verified-runtime-not-proven", NextReview: "2026-08-11"},
	}}, time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC))
	if report.Summary.Current != 1 || report.Summary.NotProven != 1 {
		t.Fatalf("pinned runtime-not-proven must be current; unpinned must stay not-proven: %#v", report)
	}
}

func TestBuildVerificationCandidateIsDeterministicAndValidated(t *testing.T) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	register := Register{SchemaVersion: 1, Sources: []Entry{{
		ID: "official-source", Authority: "official", URL: "https://docs.example.test/reference",
		Volatility: "high", VerifiedAt: "2026-06-01", NextReview: "2026-07-01",
		Status: "not-proven", Governs: []string{"example.contract"},
	}}}
	request := VerificationRequest{
		ID: "official-source", Status: "current", VerifiedAt: "2026-07-11",
		NextReview: "2026-08-11", EvidenceRef: "review:source-001",
	}
	check := CheckResult{
		ID: "official-source", HTTPStatus: 200, ObservedAt: "2026-07-11T01:02:03Z",
		ObservedDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Bytes:          123,
	}
	first, findings := BuildVerificationCandidate(register, request, check, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	second, findings := BuildVerificationCandidate(register, request, check, schemas)
	if len(findings) != 0 || first.CandidateDigest != second.CandidateDigest ||
		!bytes.Equal(first.Content, second.Content) {
		t.Fatalf("first = %#v, second = %#v, findings = %#v", first, second, findings)
	}
	if !bytes.Contains(first.Content, []byte("content_digest:")) ||
		bytes.Contains(first.Content, []byte("observed_at:")) ||
		bytes.Contains(first.Content, []byte("evidence_ref:")) {
		t.Fatalf("candidate content = %s", first.Content)
	}
	changedObservation := check
	changedObservation.ObservedAt = "2026-07-11T02:03:04Z"
	changedObservation.Bytes = 999
	third, findings := BuildVerificationCandidate(register, request, changedObservation, schemas)
	if len(findings) != 0 || first.CandidateDigest != third.CandidateDigest ||
		!bytes.Equal(first.Content, third.Content) {
		t.Fatalf("runtime observation changed tracked candidate: third=%#v findings=%#v", third, findings)
	}
}
