// Package compiler resolves canonical GDS policy sources into one deterministic
// effective policy with leaf-level provenance.
package compiler

import (
	"encoding/json"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// DevelopmentBundleVersion stamps bundles compiled from policy sources on a
// policy-owner checkout. It tracks the current release line with a -dev
// suffix so a development bundle is dated honestly; the development channel
// field, not this string, is what classifies the bundle.
const DevelopmentBundleVersion = "0.8.0-dev"

type PolicySource struct {
	SchemaVersion int               `json:"schema_version"`
	Policy        PolicyMetadata    `json:"policy"`
	Match         PolicyMatch       `json:"match,omitempty"`
	Apply         map[string]any    `json:"apply"`
	Constraints   PolicyConstraints `json:"constraints,omitempty"`
	Path          string            `json:"-"`
	Digest        string            `json:"-"`
}

type PolicyMetadata struct {
	ID           string `json:"id"`
	Tier         string `json:"tier"`
	Priority     int    `json:"priority"`
	Distribution string `json:"distribution"`
}

type PolicyMatch struct {
	Owner              string   `json:"owner,omitempty"`
	Roles              []string `json:"roles,omitempty"`
	Portfolios         []string `json:"portfolios,omitempty"`
	VisibilityContract []string `json:"visibility_contract,omitempty"`
	Lifecycle          []string `json:"lifecycle,omitempty"`
}

type PolicyConstraints struct {
	Monotonic []string `json:"monotonic,omitempty"`
}

type PolicyException struct {
	SchemaVersion int                     `json:"schema_version"`
	Exception     PolicyExceptionMetadata `json:"exception"`
	Path          string                  `json:"-"`
	Digest        string                  `json:"-"`
}

type PolicyExceptionMetadata struct {
	ID               string `json:"id"`
	RepositoryID     string `json:"repository_id"`
	PolicyPath       string `json:"policy_path"`
	RequestedValue   any    `json:"requested_value"`
	Reason           string `json:"reason"`
	OwnerApprovalRef string `json:"owner_approval_ref"`
	ExpiresAt        string `json:"expires_at"`
}

type CompiledPolicyDocument struct {
	SchemaVersion  int                    `json:"schema_version"`
	CompiledPolicy CompiledPolicyMetadata `json:"compiled_policy"`
	Sources        []PolicySourceRef      `json:"sources"`
	Effective      map[string]any         `json:"effective"`
	Provenance     map[string]Provenance  `json:"provenance"`
}

type CompiledPolicyMetadata struct {
	RepositoryID  string `json:"repository_id"`
	BundleVersion string `json:"bundle_version"`
	Digest        string `json:"digest"`
}

type PolicySourceRef struct {
	ID           string `json:"id"`
	Tier         string `json:"tier"`
	Priority     int    `json:"priority"`
	Distribution string `json:"distribution"`
	Path         string `json:"path"`
	Digest       string `json:"digest"`
}

type Provenance struct {
	Source          string `json:"source"`
	Tier            string `json:"tier"`
	Priority        int    `json:"priority"`
	File            string `json:"file"`
	Operation       string `json:"operation"`
	ExceptionID     string `json:"exception_id,omitempty"`
	ApprovalRef     string `json:"approval_ref,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	ExceptionDigest string `json:"exception_digest,omitempty"`
}

func (document CompiledPolicyDocument) CanonicalJSON() ([]byte, error) {
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

type CompileResult struct {
	Document CompiledPolicyDocument
	Findings []domain.Finding
}
