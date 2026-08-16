package githubmutationruntime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

func TestBuildMutatorsUsesSeparateExactWriteApp(t *testing.T) {
	root, desired, schemas := mutationTestEstate(t)
	readConfig, err := githubruntime.Load(mutationPrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-runtime.yaml",
	)), desired, schemas)
	if err != nil {
		t.Fatal(err)
	}
	mutationConfig, err := Load(mutationPrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-mutation-runtime.yaml",
	)), desired, schemas)
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
	t.Setenv("GDS_GITHUB_MUTATION_APP_PERSONAL_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_MUTATION_APP_ORGANIZATION_KEY", string(privatePEM))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasPrefix(request.URL.Path, "/app/installations/"):
			_, _ = fmt.Fprintf(writer, `{"token":"ghs_mutation","expires_at":%q,"permissions":{"administration":"write","contents":"write","custom_properties":"write","metadata":"read","pull_requests":"write","workflows":"write"},"repository_selection":"selected"}`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
		case request.Method == http.MethodPatch && request.URL.Path == "/repos/example/repository":
			if request.Header.Get("Authorization") != "Bearer ghs_mutation" {
				t.Errorf("authorization=%q", request.Header.Get("Authorization"))
			}
			_, _ = writer.Write([]byte(`{
  "id":42,"node_id":"R_42","name":"repository","full_name":"example/repository",
  "private":true,"visibility":"private","fork":false,"archived":false,"disabled":false,
  "default_branch":"main","html_url":"https://github.com/example/repository",
  "owner":{"login":"example"},"allow_squash_merge":true
}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	mutators, err := BuildMutators(mutationConfig, readConfig, desired, BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mutator := mutators["mutation:github-personal"]
	repository, err := mutator.BindRepository(githubprovider.RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{githubprovider.MutationRepositorySettings},
	})
	if err != nil {
		t.Fatal(err)
	}
	allowSquash := true
	observed, _, err := repository.UpdateRepositorySettings(
		context.Background(), githubprovider.RepositorySettingsUpdate{
			AllowSquashMerge: &allowSquash,
		},
	)
	if err != nil || observed.ID != 42 || !observed.Merge.AllowSquashMerge {
		t.Fatalf("repository=%+v err=%v", observed, err)
	}
}
