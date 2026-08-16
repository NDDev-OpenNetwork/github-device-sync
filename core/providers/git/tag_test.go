package git

import (
	"context"
	"strings"
	"testing"
)

func TestPublishVersionTagCreatesExactImmutableLocalAndRemoteRef(t *testing.T) {
	fixture := fastForwardFixture(t)
	runner, err := NewMutationRunner()
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.PublishVersionTag(
		context.Background(), fixture.client, "refs/tags/v1.2.3", fixture.firstOID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.LocalOID != zeroOID(40) || report.Before.RemoteOID != zeroOID(40) ||
		report.After.LocalOID != fixture.firstOID || report.After.RemoteOID != fixture.firstOID {
		t.Fatalf("report=%+v", report)
	}
	remotePath := strings.TrimSpace(runFetchGit(t, fixture.client, "remote", "get-url", "origin"))
	if remote := strings.TrimSpace(runFetchGit(t, remotePath, "rev-parse", "refs/tags/v1.2.3")); remote != fixture.firstOID {
		t.Fatalf("remote tag=%s", remote)
	}
	if _, err := runner.PublishVersionTag(
		context.Background(), fixture.client, "refs/tags/v1.2.3", fixture.firstOID,
	); err == nil {
		t.Fatal("immutable version tag was published twice")
	}
}

func TestVersionTagRefRejectsNonCanonicalVersions(t *testing.T) {
	for _, version := range []string{"v1.2.3", "01.2.3", "1.2", "1.2.3/escape", "1.2.3..x"} {
		if _, err := VersionTagRef(version); err == nil {
			t.Fatalf("accepted version %q", version)
		}
	}
	for _, version := range []string{"1.2.3", "1.2.3-rc.1", "1.2.3+build.7"} {
		if _, err := VersionTagRef(version); err != nil {
			t.Fatalf("rejected version %q: %v", version, err)
		}
	}
}

func TestVersionTagRefWithStyleMakesThePrefixExplicit(t *testing.T) {
	for style, expected := range map[string]string{
		"":         "refs/tags/v1.2.3",
		"v-semver": "refs/tags/v1.2.3",
		"semver":   "refs/tags/1.2.3",
	} {
		observed, err := VersionTagRefWithStyle("1.2.3", style)
		if err != nil || observed != expected {
			t.Fatalf("style %q: got %q, %v; want %q", style, observed, err, expected)
		}
	}
	if _, err := VersionTagRefWithStyle("1.2.3", "numeric-ish"); err == nil {
		t.Fatal("unsupported tag style was accepted")
	}
}
