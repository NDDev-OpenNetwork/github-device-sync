package approval

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

func TestApprovalRejectsEveryExactBindingMutation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		SchemaVersion: 1, ApprovalID: "approval_01K20J6M6E6M2YAHG8W0W8N4AN",
		PlanID: "plan_01K20J6M6E6M2YAHG8W0W8N4AP", PlanDigest: digest('a'),
		ActorID: "owner:example-user", ActorType: "owner", IssuedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Hour), ApprovalClass: "estate-mutation", ScopeDigest: digest('b'),
		ExternalReference: "issue:123",
		Signature:         trust.Signature{Algorithm: trust.Ed25519, KeyID: "owner-primary-2026"},
	}
	signingBytes, err := trust.SigningBytes(SignatureDomain, record.Payload())
	if err != nil {
		t.Fatal(err)
	}
	record.Signature.Value = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, signingBytes))
	verifier := Verifier{
		MaximumTTL: 24 * time.Hour, MaximumFuture: 5 * time.Minute, Now: func() time.Time { return now },
		Trust: trust.Verifier{Policy: trust.Policy{
			SchemaVersion: 1, PolicyID: "estate-trust",
			Identities: []trust.Identity{{ActorID: record.ActorID, Roles: []string{"estate-approver"}, Keys: []trust.Key{{
				Algorithm: trust.Ed25519, KeyID: record.Signature.KeyID,
				PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
				ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(24 * time.Hour), Status: "active",
			}}}},
		}},
	}
	expected := Expectation{
		PlanID: record.PlanID, PlanDigest: record.PlanDigest, ApprovalClass: record.ApprovalClass,
		ScopeDigest: record.ScopeDigest, RequiredRole: "estate-approver",
	}
	if err := verifier.Verify(record, expected); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Record){
		"plan-id":     func(value *Record) { value.PlanID += "x" },
		"plan-digest": func(value *Record) { value.PlanDigest = digest('c') },
		"actor":       func(value *Record) { value.ActorID = "owner:other" },
		"issued":      func(value *Record) { value.IssuedAt = value.IssuedAt.Add(time.Second) },
		"expires":     func(value *Record) { value.ExpiresAt = value.ExpiresAt.Add(time.Second) },
		"class":       func(value *Record) { value.ApprovalClass = "other-mutation" },
		"scope":       func(value *Record) { value.ScopeDigest = digest('d') },
		"metadata":    func(value *Record) { value.ExternalReference = "issue:124" },
	}
	for name, mutate := range mutations {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := record
			mutate(&candidate)
			if err := verifier.Verify(candidate, expected); err == nil {
				t.Fatal("tampered approval was accepted")
			}
		})
	}
}

func digest(value byte) string {
	return "sha256:" + string(make([]byte, 0)) + repeat(value, 64)
}

func repeat(value byte, count int) string {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return string(result)
}
