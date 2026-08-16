package releasebuilder

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitauthority"
)

var testGitIdentity = gitauthority.Identity{
	Version: "2.50.1", Digest: "sha256:" + strings.Repeat("a", 64),
}

func TestBuildSBOMIsReproducibleAndValidated(t *testing.T) {
	sum := sha256.Sum256([]byte("module archive"))
	modules := []moduleRecord{
		{Path: "github.com/NDDev-OpenNetwork/github-device-sync", Version: "1.2.3"},
		{Path: "example.com/dependency", Version: "v1.0.0", Sum: "h1:" + base64.StdEncoding.EncodeToString(sum[:])},
	}
	content := []byte("binary")
	binaries := []binaryRecord{{Path: "bin/linux/amd64/gds", Content: content, Digest: digestBytes(content)}}
	source := Source{
		Commit:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Ref:       "refs/tags/gds-v1.2.3",
		Timestamp: time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC),
	}
	first, err := buildSBOM("1.2.3", source, modules, binaries, testGitIdentity)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildSBOM("1.2.3", source, modules, binaries, testGitIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("SPDX output is not byte-reproducible")
	}
	if !strings.Contains(string(first), "git-"+testGitIdentity.Version+"-"+testGitIdentity.Digest) {
		t.Fatal("SPDX output omitted the authoritative Git toolchain identity")
	}
	if err := validateSBOM(first); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSBOMRejectsInvalidBinaryDigest(t *testing.T) {
	_, err := buildSBOM(
		"1.2.3",
		Source{Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Timestamp: time.Now()},
		[]moduleRecord{{Path: "github.com/NDDev-OpenNetwork/github-device-sync", Version: "1.2.3"}},
		[]binaryRecord{{Path: "bin/gds", Content: []byte("binary"), Digest: "sha256:" + string(make([]byte, 64))}},
		testGitIdentity,
	)
	if err == nil {
		t.Fatal("invalid binary digest was accepted")
	}
}
