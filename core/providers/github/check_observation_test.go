package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListCheckRunsReturnsSortedExactCommitEvidence(t *testing.T) {
	oid := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/example/module/commits/"+oid+"/check-runs" ||
			request.URL.Query().Get("filter") != "latest" || request.URL.Query().Get("per_page") != "100" ||
			request.URL.Query().Get("page") != "1" {
			t.Fatalf("request = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		_, _ = fmt.Fprintf(writer, `{"total_count":2,"check_runs":[`+
			`{"id":12,"name":"test","head_sha":%q,"status":"completed","conclusion":"success",`+
			`"completed_at":"2026-08-10T01:00:00Z","details_url":"https://github.com/example/module/actions/runs/7/job/12",`+
			`"external_id":"job-12","app":{"id":2,"slug":"github-actions"}},`+
			`{"id":11,"name":"lint","head_sha":%q,"status":"completed","conclusion":"success",`+
			`"completed_at":"2026-08-10T00:59:00Z","details_url":"https://checks.example.test/11",`+
			`"external_id":"lint-11","app":{"id":3,"slug":"quality-app"}}]}`, oid, oid)
	}))
	defer server.Close()
	permissions := map[string]string{"checks": "read", "metadata": "read"}
	now := time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC)
	tokens := tokenSourceFunc(func(context.Context, string) (InstallationToken, error) {
		return InstallationToken{
			Value: "token", ExpiresAt: now.Add(time.Hour),
			Permissions: clonePermissions(permissions), RepositorySelection: "all",
		}, nil
	})
	client := testClient(t, server, tokens, func(config *Config) {
		config.Now = func() time.Time { return now }
		config.PermissionContract = PermissionContract{Permissions: permissions, RepositorySelection: "all"}
	})
	runs, _, err := client.ListCheckRuns(context.Background(), "example", "module", oid)
	if err != nil || len(runs) != 2 || runs[0].Name != "lint" || runs[1].Name != "test" ||
		runs[1].AppSlug != "github-actions" || runs[1].HeadSHA != oid {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
}

func TestListCheckRunsRejectsIncompleteOrForeignEvidence(t *testing.T) {
	oid := strings.Repeat("a", 40)
	foreign := strings.Repeat("b", 40)
	for name, body := range map[string]string{
		"truncated page": `{"total_count":101,"check_runs":[]}`,
		"foreign sha":    fmt.Sprintf(`{"total_count":1,"check_runs":[{"id":1,"name":"test","head_sha":%q,"status":"completed","conclusion":"success","completed_at":"2026-08-10T01:00:00Z","details_url":"https://github.com/example/module/actions/runs/1","app":{"id":2,"slug":"github-actions"}}]}`, foreign),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(body))
			}))
			defer server.Close()
			client := testClient(t, server, fixedToken("token", time.Now().Add(time.Hour)), nil)
			if _, _, err := client.ListCheckRuns(context.Background(), "example", "module", oid); err == nil {
				t.Fatal("invalid check evidence was accepted")
			}
		})
	}
}
