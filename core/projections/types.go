// Package projections renders standalone repository context from typed GDS
// inputs without mutating a working tree.
package projections

import (
	"crypto/sha256"
	"fmt"
)

type Bundle struct {
	Version         string `json:"version"`
	ReleaseSequence int    `json:"release_sequence"`
	Channel         string `json:"channel"`
	// SourceCommit is trace metadata: it records which commit carried the
	// canonical sources when the bundle was generated. It is deliberately not
	// part of the bundle identity, because a commit cannot contain its own SHA
	// and binding to one made a source edit and its regenerated projection
	// impossible to commit atomically.
	SourceCommit string `json:"source_commit"`
	// SourceTreeDigest is the bundle identity: a content digest of the declared
	// canonical source paths, knowable before the commit that carries them.
	SourceTreeDigest          string `json:"source_tree_digest,omitempty"`
	Digest                    string `json:"digest"`
	AttestationIdentityDigest string `json:"attestation_identity_digest,omitempty"`
}

// BundleIdentity is the part of a bundle that identifies it. It deliberately
// omits SourceCommit, which is trace metadata: a projection must not change
// identity because a different commit carried byte-identical sources.
type BundleIdentity struct {
	Version                   string `json:"version"`
	ReleaseSequence           int    `json:"release_sequence"`
	Channel                   string `json:"channel"`
	SourceTreeDigest          string `json:"source_tree_digest,omitempty"`
	Digest                    string `json:"digest"`
	AttestationIdentityDigest string `json:"attestation_identity_digest,omitempty"`
}

func (bundle Bundle) Identity() BundleIdentity {
	return BundleIdentity{
		Version: bundle.Version, ReleaseSequence: bundle.ReleaseSequence,
		Channel: bundle.Channel, SourceTreeDigest: bundle.SourceTreeDigest,
		Digest: bundle.Digest, AttestationIdentityDigest: bundle.AttestationIdentityDigest,
	}
}

type File struct {
	Path    string `json:"path"`
	Content []byte `json:"-"`
	Digest  string `json:"digest"`
}

type Candidate struct {
	InputDigest  string `json:"input_digest"`
	OutputDigest string `json:"output_digest"`
	Files        []File `json:"files"`
}

type lockDocument struct {
	SchemaVersion int            `json:"schema_version"`
	Bundle        Bundle         `json:"bundle"`
	Projection    lockProjection `json:"projection"`
}

type lockProjection struct {
	InputDigest  string     `json:"input_digest"`
	OutputDigest string     `json:"output_digest"`
	Files        []lockFile `json:"files"`
}

type lockFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

func digestBytes(content []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(content))
}
