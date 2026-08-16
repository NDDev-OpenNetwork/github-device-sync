package app

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

// normalizeInstallationID is a pure helper; exercising it directly keeps the
// behavior contract (exact match wins, short form is prefixed, unknown input is
// returned verbatim so the caller's error evidence stays honest) independent of
// the live GitHub runtime plumbing.
func TestNormalizeInstallationID(t *testing.T) {
	clients := map[string]*githubprovider.Client{
		"installation:github-personal":     {},
		"installation:github-organization": {},
	}
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"exact canonical form is returned unchanged", "installation:github-personal", "installation:github-personal"},
		{"short form without prefix is normalized", "github-personal", "installation:github-personal"},
		{"short form organization is normalized", "github-organization", "installation:github-organization"},
		{"already-prefixed unknown is returned verbatim", "installation:nonexistent", "installation:nonexistent"},
		{"short unknown is returned verbatim (honest error evidence)", "nonexistent", "nonexistent"},
		{"empty input is returned verbatim", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeInstallationID(test.input, clients); got != test.want {
				t.Fatalf("normalizeInstallationID(%q)=%q want=%q", test.input, got, test.want)
			}
		})
	}
}

// classifyDriftReport keeps a zero-drift scan as a clean success so the command
// composes with automation, and marks any divergence as stale so it surfaces
// without blocking. The ModuleDriftReport read-only plumbing that feeds this
// classifier is exercised end-to-end against the real control-plane checkout.
func TestClassifyDriftReport(t *testing.T) {
	if got := classifyDriftReport(0); got != domain.ExitSuccess {
		t.Fatalf("classifyDriftReport(0)=%v want=%v", got, domain.ExitSuccess)
	}
	if got := classifyDriftReport(1); got != domain.ExitStale {
		t.Fatalf("classifyDriftReport(1)=%v want=%v", got, domain.ExitStale)
	}
	if got := classifyDriftReport(17); got != domain.ExitStale {
		t.Fatalf("classifyDriftReport(17)=%v want=%v", got, domain.ExitStale)
	}
}
