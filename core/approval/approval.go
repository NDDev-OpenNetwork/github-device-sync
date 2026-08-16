// Package approval verifies signed, exact-plan mutation approvals.
package approval

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

const SignatureDomain = "gds-approval/v1"

type Record struct {
	SchemaVersion     int             `json:"schema_version"`
	ApprovalID        string          `json:"approval_id"`
	PlanID            string          `json:"plan_id"`
	PlanDigest        string          `json:"plan_digest"`
	ActorID           string          `json:"actor_id"`
	ActorType         string          `json:"actor_type"`
	IssuedAt          time.Time       `json:"issued_at"`
	ExpiresAt         time.Time       `json:"expires_at"`
	ApprovalClass     string          `json:"approval_class"`
	ScopeDigest       string          `json:"scope_digest"`
	ExternalReference string          `json:"external_reference,omitempty"`
	Signature         trust.Signature `json:"signature"`
}

type Payload struct {
	SchemaVersion     int       `json:"schema_version"`
	ApprovalID        string    `json:"approval_id"`
	PlanID            string    `json:"plan_id"`
	PlanDigest        string    `json:"plan_digest"`
	ActorID           string    `json:"actor_id"`
	ActorType         string    `json:"actor_type"`
	IssuedAt          time.Time `json:"issued_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	ApprovalClass     string    `json:"approval_class"`
	ScopeDigest       string    `json:"scope_digest"`
	ExternalReference string    `json:"external_reference,omitempty"`
}

type Expectation struct {
	PlanID        string
	PlanDigest    string
	ApprovalClass string
	ScopeDigest   string
	RequiredRole  string
}

type Verifier struct {
	Trust         trust.Verifier
	MaximumTTL    time.Duration
	MaximumFuture time.Duration
	Now           func() time.Time
}

func (record Record) Payload() Payload {
	return Payload{
		SchemaVersion: record.SchemaVersion, ApprovalID: record.ApprovalID,
		PlanID: record.PlanID, PlanDigest: record.PlanDigest, ActorID: record.ActorID,
		ActorType: record.ActorType, IssuedAt: record.IssuedAt.UTC(), ExpiresAt: record.ExpiresAt.UTC(),
		ApprovalClass: record.ApprovalClass, ScopeDigest: record.ScopeDigest,
		ExternalReference: record.ExternalReference,
	}
}

func (record Record) Digest() (string, error) {
	return canonicaljson.Digest(record)
}

func (verifier Verifier) Verify(record Record, expected Expectation) error {
	if verifier.Now == nil || verifier.MaximumTTL <= 0 || verifier.MaximumFuture < 0 {
		return errors.New("approval verifier configuration is invalid")
	}
	now := verifier.Now().UTC()
	if record.SchemaVersion != 1 || !identity.Valid("approval", record.ApprovalID) || !identity.Valid("plan", record.PlanID) ||
		record.PlanDigest == "" || record.ActorID == "" || record.ApprovalClass == "" ||
		record.ScopeDigest == "" || expected.RequiredRole == "" {
		return errors.New("approval identity contract is invalid")
	}
	if strings.ContainsAny(record.ExternalReference, "\x00\r\n") || len(record.ExternalReference) > 512 {
		return errors.New("approval external reference is invalid")
	}
	if record.ActorType != "owner" && record.ActorType != "delegate" && record.ActorType != "automation" {
		return errors.New("approval actor type is invalid")
	}
	if record.PlanID != expected.PlanID || record.PlanDigest != expected.PlanDigest ||
		record.ApprovalClass != expected.ApprovalClass || record.ScopeDigest != expected.ScopeDigest {
		return errors.New("approval does not bind the exact plan and scope")
	}
	issued, expires := record.IssuedAt.UTC(), record.ExpiresAt.UTC()
	if issued.IsZero() || expires.IsZero() || !expires.After(issued) ||
		expires.Sub(issued) > verifier.MaximumTTL || issued.After(now.Add(verifier.MaximumFuture)) ||
		!expires.After(now) {
		return errors.New("approval validity window is invalid or expired")
	}
	if err := verifier.Trust.Verify(
		SignatureDomain, record.ActorID, expected.RequiredRole, issued, record.Payload(), record.Signature,
	); err != nil {
		return fmt.Errorf("verify approval signature: %w", err)
	}
	return nil
}
