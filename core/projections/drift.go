package projections

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

func Verify(root string, candidate Candidate) []domain.Finding {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return []domain.Finding{projectionError(
			"GDS_PROJECTION_ROOT_INVALID", "Cannot resolve projection root", err,
		)}
	}
	priorDigests, lockFinding := previousProjectionDigests(absoluteRoot)
	findings := []domain.Finding{}
	if lockFinding != nil {
		findings = append(findings, *lockFinding)
	}
	for _, expected := range candidate.Files {
		path := filepath.Join(absoluteRoot, filepath.FromSlash(expected.Path))
		if !pathWithin(absoluteRoot, path) {
			findings = append(findings, domain.Finding{
				Code: "GDS_PROJECTION_PATH_OUTSIDE_ROOT", Severity: domain.SeverityCritical,
				Message:  fmt.Sprintf("Projection path %s escapes the target root.", expected.Path),
				Evidence: map[string]any{"path": expected.Path, "root": absoluteRoot},
			})
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			findings = append(findings, domain.Finding{
				Code: "GDS_PROJECTION_MISSING", Severity: domain.SeverityMedium,
				Message:  fmt.Sprintf("Generated projection %s is missing.", expected.Path),
				Evidence: map[string]any{"path": expected.Path, "expected_digest": expected.Digest},
			})
			continue
		}
		if err != nil {
			findings = append(findings, projectionError(
				"GDS_PROJECTION_READ_FAILED", "Cannot inspect projection "+expected.Path, err,
			))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			findings = append(findings, domain.Finding{
				Code: "GDS_PROJECTION_TYPE_INVALID", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Generated projection %s is not a regular file.", expected.Path),
				Evidence: map[string]any{"path": expected.Path, "mode": info.Mode().String()},
			})
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			findings = append(findings, projectionError(
				"GDS_PROJECTION_READ_FAILED", "Cannot read projection "+expected.Path, err,
			))
			continue
		}
		observed := digestBytes(content)
		if observed != expected.Digest {
			if expected.Path == bundleLockPath {
				findings = append(findings, domain.Finding{
					Code: "GDS_PROJECTION_STALE", Severity: domain.SeverityMedium,
					Message: "Generated bundle lock differs from the current canonical candidate.",
					Evidence: map[string]any{
						"path": expected.Path, "expected_digest": expected.Digest,
						"observed_digest": observed,
					},
				})
				continue
			}
			priorDigest, previouslyManaged := priorDigests[expected.Path]
			if previouslyManaged && priorDigest == observed {
				findings = append(findings, domain.Finding{
					Code: "GDS_PROJECTION_STALE", Severity: domain.SeverityMedium,
					Message: fmt.Sprintf(
						"Generated projection %s matches its applied lock but canonical input changed.",
						expected.Path,
					),
					Evidence: map[string]any{
						"path": expected.Path, "previous_digest": priorDigest,
						"expected_digest": expected.Digest,
					},
				})
				continue
			}
			findings = append(findings, domain.Finding{
				Code: "GDS_PROJECTION_MANUALLY_MODIFIED", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf("Generated projection %s differs from canonical output.", expected.Path),
				Evidence: map[string]any{
					"path": expected.Path, "expected_digest": expected.Digest,
					"observed_digest": observed,
				},
			})
		}
	}
	return findings
}

func previousProjectionDigests(root string) (map[string]string, *domain.Finding) {
	path := filepath.Join(root, filepath.FromSlash(bundleLockPath))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		finding := domain.Finding{
			Code: "GDS_PROJECTION_LOCK_INVALID", Severity: domain.SeverityHigh,
			Message:  "Applied projection lock is not a readable regular file.",
			Evidence: map[string]any{"path": bundleLockPath, "error": errorText(err)},
		}
		return map[string]string{}, &finding
	}
	value, err := serialization.DecodeFile(path)
	if err != nil {
		finding := projectionError(
			"GDS_PROJECTION_LOCK_INVALID", "Cannot decode the applied projection lock", err,
		)
		return map[string]string{}, &finding
	}
	raw, err := json.Marshal(value)
	if err != nil {
		finding := projectionError(
			"GDS_PROJECTION_LOCK_INVALID", "Cannot normalize the applied projection lock", err,
		)
		return map[string]string{}, &finding
	}
	var document lockDocument
	if err := serialization.DecodeInto("bundle.lock.json", raw, &document); err != nil {
		finding := projectionError(
			"GDS_PROJECTION_LOCK_INVALID", "Cannot bind the applied projection lock", err,
		)
		return map[string]string{}, &finding
	}
	digests := make(map[string]string, len(document.Projection.Files))
	for _, file := range document.Projection.Files {
		if file.Path == "" || file.Digest == "" {
			finding := domain.Finding{
				Code: "GDS_PROJECTION_LOCK_INVALID", Severity: domain.SeverityHigh,
				Message:  "Applied projection lock contains an incomplete file record.",
				Evidence: map[string]any{"path": bundleLockPath},
			}
			return map[string]string{}, &finding
		}
		if _, duplicate := digests[file.Path]; duplicate {
			finding := domain.Finding{
				Code: "GDS_PROJECTION_LOCK_INVALID", Severity: domain.SeverityHigh,
				Message:  "Applied projection lock contains a duplicate file path.",
				Evidence: map[string]any{"path": file.Path},
			}
			return map[string]string{}, &finding
		}
		digests[file.Path] = file.Digest
	}
	return digests, nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
