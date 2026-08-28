package bundle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// MaterializeProjectionSource verifies one complete release unit before
// exposing only its policy/schema/template inputs through an owned temporary
// directory. Callers must invoke cleanup.
func MaterializeProjectionSource(
	artifact []byte,
	envelope ReleaseEnvelope,
	schemas *validation.Set,
) (root string, manifest Manifest, cleanup func(), findings []domain.Finding) {
	manifest, findings = VerifyReleaseUnit(artifact, envelope, schemas)
	if len(findings) != 0 {
		return "", Manifest{}, func() {}, findings
	}
	files, err := readArchive(artifact)
	if err != nil {
		return "", Manifest{}, func() {}, []domain.Finding{bundleFinding(
			"GDS_BUNDLE_ARCHIVE_INVALID", err,
		)}
	}
	root, err = os.MkdirTemp("", "gds-projection-source-*")
	if err != nil {
		return "", Manifest{}, func() {}, []domain.Finding{bundleFinding(
			"GDS_BUNDLE_PROJECTION_SOURCE_FAILED", err,
		)}
	}
	cleanup = func() { _ = os.RemoveAll(root) }
	for name, file := range files {
		if !projectionSourceMember(name) {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			cleanup()
			return "", Manifest{}, func() {}, []domain.Finding{bundleFinding(
				"GDS_BUNDLE_PROJECTION_SOURCE_FAILED", err,
			)}
		}
		if err := os.WriteFile(target, file.content, 0o600); err != nil {
			cleanup()
			return "", Manifest{}, func() {}, []domain.Finding{bundleFinding(
				"GDS_BUNDLE_PROJECTION_SOURCE_FAILED", err,
			)}
		}
	}
	for _, required := range []string{"policies", "schemas", "templates"} {
		info, err := os.Stat(filepath.Join(root, required))
		if err != nil || !info.IsDir() {
			cleanup()
			return "", Manifest{}, func() {}, []domain.Finding{bundleFinding(
				"GDS_BUNDLE_PROJECTION_SOURCE_INCOMPLETE",
				fmt.Errorf("verified release omits required %s source tree", required),
			)}
		}
	}
	return root, manifest, cleanup, nil
}

func projectionSourceMember(name string) bool {
	for _, prefix := range []string{"policies/", "schemas/", "templates/", "estate/exceptions/"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
