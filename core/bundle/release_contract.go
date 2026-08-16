package bundle

import (
	"fmt"
	"strings"
)

// ReleaseTarget is one platform supported by the portable release contract.
type ReleaseTarget struct {
	OS   string
	Arch string
}

var requiredReleaseTargets = [...]ReleaseTarget{
	{OS: "darwin", Arch: "amd64"},
	{OS: "darwin", Arch: "arm64"},
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
}

var requiredReleaseExecutables = [...]string{
	"gds",
	"gds-controller",
	"gds-codex-runtime-driver",
}

// RequiredReleaseTargets returns an immutable copy of the portable target set.
func RequiredReleaseTargets() []ReleaseTarget {
	return append([]ReleaseTarget(nil), requiredReleaseTargets[:]...)
}

// RequiredReleaseExecutables returns an immutable copy of the executable set.
func RequiredReleaseExecutables() []string {
	return append([]string(nil), requiredReleaseExecutables[:]...)
}

// RequiredReleaseExecutablePaths returns the complete portable executable
// matrix in deterministic target/command order.
func RequiredReleaseExecutablePaths() []string {
	paths := make([]string, 0, len(requiredReleaseTargets)*len(requiredReleaseExecutables))
	for _, target := range requiredReleaseTargets {
		for _, executable := range requiredReleaseExecutables {
			paths = append(paths, fmt.Sprintf("bin/%s/%s/%s", target.OS, target.Arch, executable))
		}
	}
	return paths
}

// ReleaseTargetSupported reports whether a host target belongs to the portable
// release contract.
func ReleaseTargetSupported(targetOS, targetArch string) bool {
	for _, target := range requiredReleaseTargets {
		if target.OS == targetOS && target.Arch == targetArch {
			return true
		}
	}
	return false
}

// ValidateReleaseExecutableMatrix proves that a manifest contains exactly the
// executable topology required by the portable release contract.
func ValidateReleaseExecutableMatrix(manifest Manifest) error {
	required := make(map[string]struct{}, len(requiredReleaseTargets)*len(requiredReleaseExecutables))
	for _, path := range RequiredReleaseExecutablePaths() {
		required[path] = struct{}{}
	}
	seen := make(map[string]struct{}, len(required))
	for _, file := range manifest.Files {
		if !strings.HasPrefix(file.Path, "bin/") {
			continue
		}
		if _, expected := required[file.Path]; !expected {
			return fmt.Errorf("release manifest contains unexpected executable path %s", file.Path)
		}
		if file.Mode != "0755" || file.Size < 1 || !strings.HasPrefix(file.Digest, "sha256:") {
			return fmt.Errorf("release executable metadata is invalid for %s", file.Path)
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return fmt.Errorf("release manifest contains duplicate executable path %s", file.Path)
		}
		seen[file.Path] = struct{}{}
	}
	for _, path := range RequiredReleaseExecutablePaths() {
		if _, found := seen[path]; !found {
			return fmt.Errorf("release manifest is missing executable path %s", path)
		}
	}
	return nil
}
