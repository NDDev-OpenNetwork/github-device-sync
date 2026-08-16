package githubruntime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// TestBuildReadersCLIBuildsSupersetClients proves the gh-cli runtime branch:
// the fixture's gh-cli secret store routes BuildReaders into CLITokenSource
// construction with superset permission contracts, and the resulting clients
// enumerate the account list endpoints instead of /installation/repositories.
// stubCLIRunner stands in for `gh auth token`. Without it this test only
// passes on a machine with an authenticated gh CLI, so the gh-cli branch was
// green on a laptop and red in CI - untested where it mattered.
type stubCLIRunner struct{}

func (stubCLIRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte("gho_stub_token_for_tests\n"), nil
}

func TestBuildReadersCLIBuildsSupersetClients(t *testing.T) {
	root, desired, schemas := loadTestEstate(t)
	path := copyPrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-runtime-ghcli.yaml",
	))
	config, err := Load(path, desired, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if config.SecretStore.Provider != "gh-cli" {
		t.Fatalf("provider=%q", config.SecretStore.Provider)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/user":
			writer.Header().Set("X-OAuth-Scopes", "repo, read:org, workflow")
			_, _ = writer.Write([]byte(`{"login":"example-user","id":1,"type":"User"}`))
		case "/user/repos":
			_, _ = fmt.Fprint(writer, `[]`)
		case "/orgs/example-org/repos":
			_, _ = fmt.Fprint(writer, `[]`)
		case "/orgs/example-media/repos":
			_, _ = fmt.Fprint(writer, `[]`)
		case "/orgs/example-guild/repos":
			_, _ = fmt.Fprint(writer, `[]`)
		case "/orgs/NDDev-OpenNetwork/repos":
			_, _ = fmt.Fprint(writer, `[]`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	readers, err := BuildReaders(config, desired, BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: httpClient, AllowInsecureLoopback: true,
		CLIRunner: stubCLIRunner{},
	})
	if err != nil {
		t.Fatalf("build readers: %v", err)
	}
	if len(readers) != len(desired.Installations) {
		t.Fatalf("readers=%d installations=%d", len(readers), len(desired.Installations))
	}
	for _, id := range desired.Root.Installations {
		inventory, err := readers[id].ListInstallationRepositories(context.Background(), 2000)
		if err != nil {
			t.Fatalf("installation=%s inventory: %v", id, err)
		}
		if inventory.TotalCount != 0 {
			t.Fatalf("installation=%s total=%d", id, inventory.TotalCount)
		}
		if evidence := inventory.Permissions; evidence.Status != "verified-superset" {
			t.Fatalf("installation=%s status=%q", id, evidence.Status)
		}
	}
}
