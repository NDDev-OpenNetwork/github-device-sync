// Package security implements deterministic portable-source and tracked-file
// checks. It complements, but does not replace, the external secret scanner.
package security

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const maxSecurityScanFileBytes = 4 << 20

var portableRoots = []string{
	"core/", "harnesses/", "plugins/", "policies/", "schemas/", "skills/", "templates/",
}

var secretPatterns = []struct {
	name       string
	expression *regexp.Regexp
}{
	{"private-key", regexp.MustCompile(`-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`)},
	{"github-pat", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`)},
	{"aws-access-key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"authorization-header", regexp.MustCompile(`(?i)authorization:\s*(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]{12,}`)},
	{"x-access-token", regexp.MustCompile(`\bx-access-token:[A-Za-z0-9._~+/=-]{12,}`)},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
}

// ScanContent reports whether content matches any high-confidence secret
// pattern. It is the single source of truth for secret detection shared by
// the tracked-source scanner and the portable-bundle content gate.
func ScanContent(content []byte) (string, bool) {
	for _, pattern := range secretPatterns {
		if pattern.expression.Match(content) {
			return pattern.name, true
		}
	}
	return "", false
}

var absoluteUserPath = regexp.MustCompile(
	`(?:^|[^A-Za-z0-9_])(?:` +
		regexp.QuoteMeta("/"+"Users/") + `[^/\s]+|` +
		regexp.QuoteMeta("/"+"home/") + `[^/\s]+)(?:/|\b)`,
)

var portableSystemHomePrefixes = [][]byte{
	[]byte("/home/linuxbrew/.linuxbrew/"),
}

func containsDeviceSpecificHomePath(content []byte) bool {
	scanContent := bytes.Clone(content)
	for _, prefix := range portableSystemHomePrefixes {
		scanContent = bytes.ReplaceAll(scanContent, prefix, []byte("${SYSTEM_PREFIX}"))
	}
	return absoluteUserPath.Match(scanContent)
}

type Report struct {
	TrackedFiles  int `json:"tracked_files"`
	ScannedFiles  int `json:"scanned_files"`
	PortableFiles int `json:"portable_files"`
}

func Scan(root string, tracked []gitprovider.TrackedPath) (Report, []domain.Finding) {
	report := Report{TrackedFiles: len(tracked)}
	findings := []domain.Finding{}
	for _, entry := range tracked {
		if entry.Mode == "160000" || entry.Mode == "120000" {
			continue
		}
		path, err := confinedPath(root, entry.Path)
		if err != nil {
			findings = append(findings, securityFinding(
				"GDS_SECURITY_PATH_INVALID", entry.Path, err.Error(), "",
			))
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSecurityScanFileBytes {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(content, 0) >= 0 {
			continue
		}
		report.ScannedFiles++
		portable := isPortablePath(entry.Path)
		if portable {
			report.PortableFiles++
			if containsDeviceSpecificHomePath(content) {
				findings = append(findings, securityFinding(
					"GDS_PORTABLE_ABSOLUTE_PATH", entry.Path,
					"Portable source contains a device-specific home path.", "absolute-user-path",
				))
			}
		}
		for _, pattern := range secretPatterns {
			if pattern.expression.Match(content) {
				findings = append(findings, securityFinding(
					"GDS_SECRET_PATTERN_DETECTED", entry.Path,
					"Tracked text matches a high-confidence secret pattern.", pattern.name,
				))
			}
		}
	}
	sort.Slice(findings, func(left, right int) bool {
		leftPath := fmt.Sprint(findings[left].Evidence["path"])
		rightPath := fmt.Sprint(findings[right].Evidence["path"])
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		return findings[left].Code < findings[right].Code
	})
	return report, findings
}

func confinedPath(root, relative string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("tracked path escapes repository root")
	}
	return filepath.Join(root, clean), nil
}

func isPortablePath(path string) bool {
	for _, root := range portableRoots {
		if strings.HasPrefix(path, root) {
			return true
		}
	}
	return false
}

func securityFinding(code, path, message, pattern string) domain.Finding {
	evidence := map[string]any{"path": path}
	if pattern != "" {
		evidence["pattern"] = pattern
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityCritical, Message: message, Evidence: evidence,
	}
}
