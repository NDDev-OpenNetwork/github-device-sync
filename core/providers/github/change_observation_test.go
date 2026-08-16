package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChangeObservationReadsBoundedTypedProviderState(t *testing.T) {
	oidA := strings.Repeat("a", 40)
	oidB := strings.Repeat("b", 40)
	content := []byte("name: gds\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/example/repository/git/ref/heads/task/gds":
			_, _ = fmt.Fprintf(writer, `{"ref":"refs/heads/task/gds","object":{"sha":%q}}`, oidA)
		case "/repos/example/repository/contents/.github/workflows/gds.yml":
			if request.URL.Query().Get("ref") != "task/gds" {
				t.Errorf("content query=%s", request.URL.RawQuery)
			}
			_, _ = fmt.Fprintf(writer, `{"type":"file","encoding":"base64","path":".github/workflows/gds.yml","sha":%q,"size":%d,"content":%q}`,
				oidA, len(content), base64.StdEncoding.EncodeToString(content))
		case "/repos/example/repository/pulls":
			query := request.URL.Query()
			if query.Get("state") != "open" || query.Get("head") != "example:task/gds" ||
				query.Get("base") != "main" || query.Get("per_page") != "2" || query.Get("page") != "1" {
				t.Errorf("pull query=%s", request.URL.RawQuery)
			}
			_, _ = fmt.Fprintf(writer, `[{"number":7,"id":8,"html_url":"https://github.com/example/repository/pull/7","draft":true,"state":"open","title":"GDS update","body":"Generated change","head":{"ref":"task/gds","sha":%q},"base":{"ref":"main"}}]`, oidB)
		case "/repos/example/repository/properties/values":
			_, _ = writer.Write([]byte(`[{"property_name":"gds-role","value":"project"},{"property_name":"gds-portfolios","value":["one","two"]}]`))
		case "/repos/example/repository/rulesets/9":
			_, _ = writer.Write([]byte(`{"id":9,"name":"gds-default-branch","target":"branch","source_type":"Repository","source":"example/repository","enforcement":"active"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	permissions := map[string]string{
		"administration": "read", "contents": "read", "custom_properties": "read",
		"metadata": "read", "pull_requests": "read",
	}
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	tokens := tokenSourceFunc(func(context.Context, string) (InstallationToken, error) {
		return InstallationToken{
			Value: "read-token", ExpiresAt: now.Add(time.Hour),
			Permissions: clonePermissions(permissions), RepositorySelection: "all",
		}, nil
	})
	client := testClient(t, server, tokens, func(config *Config) {
		config.PermissionContract = PermissionContract{
			Permissions: clonePermissions(permissions), RepositorySelection: "all",
		}
	})
	branch, _, err := client.GetBranchRef(context.Background(), "example", "repository", "task/gds")
	if err != nil || branch.SHA != oidA {
		t.Fatalf("branch=%#v err=%v", branch, err)
	}
	file, _, err := client.GetContent(
		context.Background(), "example", "repository", ".github/workflows/gds.yml", "task/gds",
	)
	if err != nil || file.BlobSHA != oidA || string(file.Content) != string(content) {
		t.Fatalf("file=%#v err=%v", file, err)
	}
	pulls, _, err := client.ListOpenPullRequests(
		context.Background(), "example", "repository", "task/gds", "main",
	)
	if err != nil || len(pulls) != 1 || pulls[0].HeadSHA != oidB || pulls[0].Title != "GDS update" {
		t.Fatalf("pulls=%#v err=%v", pulls, err)
	}
	properties, _, err := client.GetCustomPropertyValues(context.Background(), "example", "repository")
	if err != nil || len(properties) != 2 || properties[0].Name != "gds-portfolios" {
		t.Fatalf("properties=%#v err=%v", properties, err)
	}
	portfolios, ok := properties[0].Value.([]string)
	if !ok || len(portfolios) != 2 || portfolios[0] != "one" || portfolios[1] != "two" {
		t.Fatalf("normalized multi-select=%#v", properties[0].Value)
	}
	ruleset, _, err := client.GetRepositoryRulesetSummary(context.Background(), "example", "repository", 9)
	if err != nil || ruleset.Name != "gds-default-branch" {
		t.Fatalf("ruleset=%#v err=%v", ruleset, err)
	}
}

func TestChangeObservationRejectsDuplicatePullRequestsAndOversizedContent(t *testing.T) {
	oid := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/pulls"):
			entry := fmt.Sprintf(`{"number":7,"id":8,"html_url":"https://github.com/example/repository/pull/7","draft":true,"state":"open","title":"Title","body":"Body","head":{"ref":"task/gds","sha":%q},"base":{"ref":"main"}}`, oid)
			_, _ = fmt.Fprintf(writer, "[%s,%s]", entry, entry)
		default:
			_, _ = fmt.Fprintf(writer, `{"type":"file","encoding":"base64","path":"file.txt","sha":%q,"size":%d,"content":""}`,
				oid, maxMutationContentBytes+1)
		}
	}))
	defer server.Close()
	client := testClient(t, server, fixedToken("token", time.Now().Add(time.Hour)), nil)
	if _, _, err := client.ListOpenPullRequests(
		context.Background(), "example", "repository", "task/gds", "main",
	); err == nil {
		t.Fatal("duplicate open pull requests were accepted")
	}
	if _, _, err := client.GetContent(
		context.Background(), "example", "repository", "file.txt", "main",
	); err == nil {
		t.Fatal("oversized content was accepted")
	}
}
