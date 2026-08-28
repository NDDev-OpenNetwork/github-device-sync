package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

const maximumProjectionBundleBytes = 256 << 20

type ProjectionSourceOptions struct {
	BundleArchive   string
	ReleaseEnvelope string
}

func (options ProjectionSourceOptions) released() bool {
	return options.BundleArchive != "" || options.ReleaseEnvelope != ""
}

func (services *Services) materializeReleasedProjectionSource(
	options ProjectionSourceOptions,
) (string, bundle.Manifest, projections.Bundle, func(), []domain.Finding) {
	if options.BundleArchive == "" || options.ReleaseEnvelope == "" {
		return "", bundle.Manifest{}, projections.Bundle{}, func() {}, []domain.Finding{{
			Code: "GDS_PROJECTION_RELEASE_INPUT_INCOMPLETE", Severity: domain.SeverityHigh,
			Message: "Released projection generation requires both bundle archive and release envelope.",
		}}
	}
	artifact, err := readBoundedProjectionFile(options.BundleArchive, maximumProjectionBundleBytes)
	if err != nil {
		return "", bundle.Manifest{}, projections.Bundle{}, func() {}, []domain.Finding{dependencyFinding(options.BundleArchive, err)}
	}
	envelopeRaw, err := readBoundedProjectionFile(options.ReleaseEnvelope, 2<<20)
	if err != nil {
		return "", bundle.Manifest{}, projections.Bundle{}, func() {}, []domain.Finding{dependencyFinding(options.ReleaseEnvelope, err)}
	}
	var envelope bundle.ReleaseEnvelope
	if err := serialization.DecodeInto(options.ReleaseEnvelope, envelopeRaw, &envelope); err != nil {
		return "", bundle.Manifest{}, projections.Bundle{}, func() {}, []domain.Finding{dependencyFinding(options.ReleaseEnvelope, err)}
	}
	root, manifest, cleanup, findings := bundle.MaterializeProjectionSource(artifact, envelope, services.Schemas)
	if len(findings) != 0 {
		return "", bundle.Manifest{}, projections.Bundle{}, func() {}, findings
	}
	if err := services.Projector.VerifyEmbeddedSources(root); err != nil {
		cleanup()
		return "", bundle.Manifest{}, projections.Bundle{}, func() {}, []domain.Finding{{
			Code: "GDS_PROJECTION_RELEASE_ENGINE_MISMATCH", Severity: domain.SeverityHigh,
			Message: err.Error(),
		}}
	}
	identity := projections.Bundle{
		Version: manifest.BundleVersion, ReleaseSequence: manifest.ReleaseSequence,
		Channel: manifest.Channel, SourceCommit: manifest.SourceCommit,
		SourceTreeDigest: manifest.ContentSetDigest, Digest: envelope.ArtifactDigest,
		AttestationIdentityDigest: envelope.ExpectedAttestationIdentityDigest,
	}
	return root, manifest, identity, cleanup, nil
}

func readBoundedProjectionFile(path string, maximum int64) ([]byte, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > maximum {
		return nil, fmt.Errorf("projection release input is not a bounded regular file")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) != info.Size() {
		return nil, fmt.Errorf("projection release input changed while reading")
	}
	return raw, nil
}
