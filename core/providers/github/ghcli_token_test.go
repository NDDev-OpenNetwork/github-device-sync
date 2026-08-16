package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// cliRunnerStub is a CommandRunner that returns a fixed token and records
// invocations, mirroring appTokenSecretStore for the App token source.
type cliRunnerStub struct {
	value []byte
	err   error
	calls atomic.Int32
}

func (runner *cliRunnerStub) Run(context.Context, string, ...string) ([]byte, error) {
	runner.calls.Add(1)
	return append([]byte(nil), runner.value...), runner.err
}

func TestCLITokenSourceMintsFromScopesAndCaches(t *testing.T) {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/user" || request.Header.Get("Authorization") != "Bearer gho_fixture" {
			t.Errorf("request=%s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		writer.Header().Set("X-OAuth-Scopes", "gist, read:org, repo, workflow")
		writer.Header().Set("X-GitHub-Request-Id", "req-1")
		_, _ = writer.Write([]byte(`{"login":"example-user","id":102828838,"type":"User"}`))
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	source, err := NewCLITokenSource(CLITokenConfig{
		AccountLogin: "example-user", AccountType: "user",
		BaseURL: server.URL + "/", HTTPClient: httpClient,
		Now: func() time.Time { return now }, AllowInsecureLoopback: true,
		Runner: &cliRunnerStub{value: []byte("gho_fixture")},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.Token(context.Background(), "installation:github-personal")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if first.Value != "gho_fixture" {
		t.Fatalf("value=%q", first.Value)
	}
	if first.RepositorySelection != "all" {
		t.Fatalf("selection=%q", first.RepositorySelection)
	}
	if first.ExpiresAt != now.Add(ghCLITokenLifetime) {
		t.Fatalf("expires=%v", first.ExpiresAt)
	}
	if first.Permissions["contents"] != "write" || first.Permissions["metadata"] != "read" ||
		first.Permissions["workflows"] != "write" {
		t.Fatalf("permissions=%+v", first.Permissions)
	}
	second, err := source.Token(context.Background(), "installation:github-personal")
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestCLITokenSourceRejectsTokenWithoutRepoScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-OAuth-Scopes", "read:org")
		_, _ = writer.Write([]byte(`{"login":"example-user","id":1,"type":"User"}`))
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	source, err := NewCLITokenSource(CLITokenConfig{
		AccountLogin: "example-user", AccountType: "user",
		BaseURL: server.URL + "/", HTTPClient: httpClient, AllowInsecureLoopback: true,
		Runner: &cliRunnerStub{value: []byte("gho_no_repo")},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token(context.Background(), "installation:github-personal")
	if err == nil {
		t.Fatal("token without repo scope was accepted")
	}
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Kind != ErrorAuthentication ||
		!strings.Contains(err.Error(), "repo scope") {
		t.Fatalf("expected repo-scope authentication error, got %v", err)
	}
}

func TestCLITokenSourceReportsMissingScopesHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"login":"example-user","id":1,"type":"User"}`))
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	source, err := NewCLITokenSource(CLITokenConfig{
		AccountLogin: "example-user", AccountType: "user",
		BaseURL: server.URL + "/", HTTPClient: httpClient, AllowInsecureLoopback: true,
		Runner: &cliRunnerStub{value: []byte("gho_no_scopes")},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token(context.Background(), "installation:github-personal")
	if err == nil || !strings.Contains(err.Error(), "scopes") {
		t.Fatalf("expected scopes error, got %v", err)
	}
}

func TestCLITokenSourceRejectsUnavailableRunner(t *testing.T) {
	source, err := NewCLITokenSource(CLITokenConfig{
		AccountLogin: "example-user", AccountType: "user",
		Runner:                &cliRunnerStub{err: &APIError{Kind: ErrorAuthentication}},
		AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token(context.Background(), "installation:github-personal")
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Kind != ErrorAuthentication {
		t.Fatalf("expected authentication error, got %v", err)
	}
}

func TestCLITokenSourceValidatesConfig(t *testing.T) {
	if _, err := NewCLITokenSource(CLITokenConfig{AccountType: "user"}); err == nil {
		t.Fatal("missing account login was accepted")
	}
	if _, err := NewCLITokenSource(CLITokenConfig{AccountLogin: "x"}); err == nil {
		t.Fatal("missing account type was accepted")
	}
	if _, err := NewCLITokenSource(CLITokenConfig{
		AccountLogin: "x", AccountType: "team",
	}); err == nil {
		t.Fatal("invalid account type was accepted")
	}
}

func TestScopesToPermissionsMapsConservativeSuperset(t *testing.T) {
	permissions := scopesToPermissions([]string{"repo", "workflow", "read:org"})
	if permissions["contents"] != "write" || permissions["metadata"] != "read" ||
		permissions["pull_requests"] != "write" || permissions["actions"] != "write" ||
		permissions["custom_properties"] != "write" || permissions["workflows"] != "write" ||
		permissions["organization_administration"] != "read" {
		t.Fatalf("permissions=%+v", permissions)
	}
	if len(scopesToPermissions([]string{"gist"})) != 0 {
		t.Fatal("non-repository scopes should not grant repository permissions")
	}
}

func TestParseScopesIsResilient(t *testing.T) {
	scopes := parseScopes("repo, read:org , workflow")
	if len(scopes) != 3 || scopes[0] != "repo" || scopes[1] != "read:org" || scopes[2] != "workflow" {
		t.Fatalf("scopes=%+v", scopes)
	}
}

// TestCLITokenSourceServesAccountInventoryThroughClient proves the full read
// path: a CLITokenSource-backed Client enumerates the account list endpoint
// (bare array shape) through the superset permission contract.
func TestCLITokenSourceServesAccountInventoryThroughClient(t *testing.T) {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/user":
			writer.Header().Set("X-OAuth-Scopes", "repo, workflow")
			_, _ = writer.Write([]byte(`{"login":"example-user","id":1,"type":"User"}`))
		case "/user/repos":
			_, _ = fmt.Fprint(writer, `[{"id":101,"node_id":"n1","name":"alpha","full_name":"example-user/alpha","private":true,"visibility":"private","fork":false,"archived":false,"disabled":false,"default_branch":"main","html_url":"https://github.com/example-user/alpha","owner":{"login":"example-user"}}]`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	contract, err := NewPermissionContract(
		map[string]string{"metadata": "read", "contents": "read", "pull_requests": "read",
			"actions": "read", "administration": "read", "checks": "read"},
		map[string]string{}, "all",
	)
	if err != nil {
		t.Fatal(err)
	}
	contract.Mode = PermissionModeSuperset
	tokens, err := NewCLITokenSource(CLITokenConfig{
		AccountLogin: "example-user", AccountType: "user",
		BaseURL: server.URL + "/", HTTPClient: httpClient,
		Now: func() time.Time { return now }, AllowInsecureLoopback: true,
		Runner: &cliRunnerStub{value: []byte("gho_fixture")},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{
		BaseURL: server.URL + "/", HTTPClient: httpClient,
		TokenSource: tokens, InstallationID: "installation:github-personal",
		PermissionContract: contract, AllowInsecureLoopback: true,
		Now:              func() time.Time { return now },
		InventoryAccount: InventoryAccount{Login: "example-user", Type: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := client.ListInstallationRepositories(context.Background(), 2000)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if inventory.TotalCount != 1 || inventory.Repositories[0].FullName != "example-user/alpha" {
		t.Fatalf("inventory=%+v", inventory)
	}
}
