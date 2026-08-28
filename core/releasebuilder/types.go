// Package releasebuilder assembles one reproducible, installable GDS release
// unit from an exact clean Git commit and an exact Go toolchain.
package releasebuilder

import (
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
)

const ExpectedGoVersion = "go1.26.7"

type Target = bundle.ReleaseTarget

func defaultTargets() []Target {
	return bundle.RequiredReleaseTargets()
}

type Request struct {
	Root                          string
	OutputDirectory               string
	Version                       string
	ReleaseSequence               int
	Channel                       string
	MinimumCLIVersion             string
	SourceRef                     string
	GoBinary                      string
	HarnessEvidenceManifestDigest string
	HarnessEvidenceDirectory      string
	HarnessEvidenceTrustPolicy    string
}

type Source struct {
	Commit    string    `json:"commit"`
	Ref       string    `json:"ref"`
	Timestamp time.Time `json:"timestamp"`
}

type OutputFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}

type Result struct {
	SchemaVersion   int          `json:"schema_version"`
	Version         string       `json:"version"`
	ReleaseSequence int          `json:"release_sequence"`
	Channel         string       `json:"channel"`
	Source          Source       `json:"source"`
	GoVersion       string       `json:"go_version"`
	GitVersion      string       `json:"git_version"`
	GitDigest       string       `json:"git_digest"`
	OutputDirectory string       `json:"output_directory"`
	ArtifactName    string       `json:"artifact_name"`
	ArtifactDigest  string       `json:"artifact_digest"`
	ManifestDigest  string       `json:"manifest_digest"`
	SBOMDigest      string       `json:"sbom_digest"`
	Reproducible    bool         `json:"reproducible"`
	Files           []OutputFile `json:"files"`
}

type DirectoryVerification struct {
	SchemaVersion   int          `json:"schema_version"`
	Status          string       `json:"status"`
	Version         string       `json:"version"`
	ReleaseSequence int          `json:"release_sequence"`
	Channel         string       `json:"channel"`
	SourceCommit    string       `json:"source_commit"`
	SourceRef       string       `json:"source_ref"`
	ArtifactName    string       `json:"artifact_name"`
	ArtifactDigest  string       `json:"artifact_digest"`
	ManifestDigest  string       `json:"manifest_digest"`
	SBOMDigest      string       `json:"sbom_digest"`
	Reproducible    bool         `json:"reproducible"`
	Files           []OutputFile `json:"files"`
}

type moduleRecord struct {
	Path    string
	Version string
	Sum     string
}

type binaryRecord struct {
	Path    string
	Digest  string
	Content []byte
}
