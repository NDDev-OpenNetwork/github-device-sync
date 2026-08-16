package gitcontracts

import (
	"testing"

	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

func TestExactRemoteURLRequiresOneSharedCredentialFreeURL(t *testing.T) {
	t.Parallel()
	topology := gitprovider.Topology{Remotes: []gitprovider.Remote{{
		Name:      "origin",
		FetchURLs: []gitprovider.RemoteURL{{Value: "https://github.com/owner/source.git"}},
		PushURLs:  []gitprovider.RemoteURL{{Value: "https://github.com/owner/source.git"}},
	}}}
	got, err := ExactRemoteURL(topology, "origin")
	if err != nil || got != "https://github.com/owner/source.git" {
		t.Fatalf("url=%q err=%v", got, err)
	}

	topology.Remotes[0].PushURLs[0].Value = "git@github.com:owner/source.git"
	if _, err := ExactRemoteURL(topology, "origin"); err == nil {
		t.Fatal("accepted different fetch and push URLs")
	}
	topology.Remotes[0].PushURLs[0] = topology.Remotes[0].FetchURLs[0]
	topology.Remotes[0].FetchURLs[0].CredentialsRedacted = true
	if _, err := ExactRemoteURL(topology, "origin"); err == nil {
		t.Fatal("accepted a redacted remote URL")
	}
}
