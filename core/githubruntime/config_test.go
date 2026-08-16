package githubruntime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestLoadRequiresPrivateConfigAndExactEstateBindings(t *testing.T) {
	root, desired, schemas := loadTestEstate(t)
	path := copyPrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-runtime.yaml",
	))
	config, err := Load(path, desired, schemas)
	if err != nil {
		t.Fatal(err)
	}
	if config.GitHub.MaxRepositories != 2000 || len(config.GitHub.Installations) != 5 {
		t.Fatalf("config=%+v", config)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, desired, schemas); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("public configuration error=%v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	delete(config.GitHub.Installations, "installation:github-personal")
	if err := validateAgainstEstate(config, desired, nil); err == nil {
		t.Fatal("incomplete installation set was accepted")
	}
}

func TestBuildReadersMintsTokensAndEnumeratesWithoutPersistingSecrets(t *testing.T) {
	root, desired, schemas := loadTestEstate(t)
	path := copyPrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-runtime.yaml",
	))
	config, err := Load(path, desired, schemas)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	t.Setenv("GDS_GITHUB_APP_ORGANIZATION_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_APP_PERSONAL_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_APP_EXAMPLE_MEDIA_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_APP_GUILD_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_APP_OPENNETWORK_KEY", string(privatePEM))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasPrefix(request.URL.Path, "/app/installations/"):
			_, _ = fmt.Fprintf(
				writer, `{"token":"ghs_fixture","expires_at":%q,`+
					`"permissions":{"actions":"read","administration":"read",`+
					`"checks":"read","contents":"read","metadata":"read",`+
					`"pull_requests":"read"},"repository_selection":"all"}`,
				time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			)
		case request.URL.Path == "/installation/repositories":
			if request.Header.Get("Authorization") != "Bearer ghs_fixture" {
				t.Errorf("authorization=%q", request.Header.Get("Authorization"))
			}
			_, _ = writer.Write([]byte(`{"total_count":0,"repositories":[]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	readers, err := BuildReaders(config, desired, BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range desired.Root.Installations {
		inventory, err := readers[id].ListInstallationRepositories(context.Background(), 2000)
		if err != nil || inventory.InstallationID != id || inventory.TotalCount != 0 {
			t.Fatalf("installation=%s inventory=%+v err=%v", id, inventory, err)
		}
	}
}

func loadTestEstate(t *testing.T) (string, estate.Config, *validation.Set) {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	desired, findings := estate.Load(root, schemas)
	if len(findings) != 0 {
		t.Fatalf("estate findings=%+v", findings)
	}
	return root, desired, schemas
}

func copyPrivateFixture(t *testing.T, source string) string {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "github-runtime.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
