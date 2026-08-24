package app

import (
	"os"
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

func TestReleaseImplementationRootFollowsThePinnedPublicEngine(t *testing.T) {
	root := t.TempDir()
	if observed := releaseImplementationRoot(root); observed != root {
		t.Fatalf("monolithic release root = %q, want %q", observed, root)
	}
	engine := filepath.Join(root, "modules", "github-device-sync")
	if err := os.MkdirAll(engine, 0o755); err != nil {
		t.Fatal(err)
	}
	if observed := releaseImplementationRoot(root); observed != engine {
		t.Fatalf("split release root = %q, want pinned engine %q", observed, engine)
	}
}
