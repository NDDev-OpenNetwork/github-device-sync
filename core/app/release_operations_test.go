package app

import (
	"path/filepath"
	"testing"
)

func TestNormalizeReleaseOperationPathsProducesStableAbsoluteInputs(t *testing.T) {
	options, finding := normalizeReleaseOperationPaths(ReleaseOperationOptions{
		ReleaseDirectory:          "release",
		EvidenceDirectory:         "evidence",
		TrustPolicyPath:           filepath.Join("requirements", "bundle-trust.yaml"),
		InstallRoot:               "installation",
		RollbackAuthorizationPath: filepath.Join("approval", "rollback.yaml"),
	})
	if finding != nil {
		t.Fatalf("finding: %+v", finding)
	}
	for name, path := range map[string]string{
		"release":       options.ReleaseDirectory,
		"evidence":      options.EvidenceDirectory,
		"trust":         options.TrustPolicyPath,
		"install":       options.InstallRoot,
		"authorization": options.RollbackAuthorizationPath,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			t.Fatalf("%s path is not canonical absolute input: %s", name, path)
		}
	}
}
