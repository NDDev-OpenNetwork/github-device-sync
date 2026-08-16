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

func observationClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return testClient(t, server, fixedToken("t", time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC)), nil)
}

func sha(seed byte) string {
	return strings.Repeat(fmt.Sprintf("%x", seed%16), 40)
}

// An unread page and an empty one are indistinguishable to a caller asking
// whether anything unfinished remains, so every collection is enumerated to
// exhaustion. This proves the second page is actually fetched and joined.
func TestBranchEnumerationFollowsEveryPage(t *testing.T) {
	t.Parallel()
	client := observationClient(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("page") == "1" {
			writer.Header().Set("Link",
				`<`+"http://"+request.Host+request.URL.Path+`?per_page=100&page=2>; rel="next"`)
			_, _ = writer.Write([]byte(
				`[{"name":"main","commit":{"sha":"` + sha(1) + `"},"protected":true}]`))
			return
		}
		_, _ = writer.Write([]byte(
			`[{"name":"task/one","commit":{"sha":"` + sha(2) + `"},"protected":false}]`))
	})
	branches, _, err := client.ListBranches(context.Background(), "example-org", "example")
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 || branches[0].Name != "main" || branches[1].Name != "task/one" {
		t.Fatalf("branches = %#v", branches)
	}
	if !branches[0].Protected || branches[1].Protected {
		t.Fatalf("protection = %#v", branches)
	}
}

// GitHub reports a merged pull request as `closed`. Collapsing the two would let
// work that was abandoned with its commits still on a branch read as landed.
func TestAMergedPullRequestIsDistinguishedFromAClosedOne(t *testing.T) {
	t.Parallel()
	client := observationClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[
      {"number":1,"state":"closed","draft":false,"title":"landed","merged_at":"2026-08-01T00:00:00Z",
       "head":{"ref":"task/one","sha":"` + sha(3) + `"},"base":{"ref":"main"}},
      {"number":2,"state":"closed","draft":false,"title":"abandoned","merged_at":null,
       "head":{"ref":"task/two","sha":"` + sha(4) + `"},"base":{"ref":"main"}},
      {"number":3,"state":"open","draft":true,"title":"in progress",
       "head":{"ref":"task/three","sha":"` + sha(5) + `"},"base":{"ref":"main"}}]`))
	})
	requests, _, err := client.ListPullRequests(context.Background(), "example-org", "example")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %#v", requests)
	}
	if !requests[0].Merged || requests[1].Merged || requests[2].Merged {
		t.Fatalf("merged = %#v", requests)
	}
	if !requests[2].Draft || requests[2].State != "open" {
		t.Fatalf("draft = %#v", requests[2])
	}
}

// The issues endpoint returns pull requests too. Counting one twice would report
// the same work as blocking in two places.
func TestPullRequestsAreNotCountedAsIssues(t *testing.T) {
	t.Parallel()
	client := observationClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[
      {"number":10,"state":"open","title":"a real issue"},
      {"number":11,"state":"open","title":"a pull request","pull_request":{"url":"x"}}]`))
	})
	issues, _, err := client.ListOpenIssues(context.Background(), "example-org", "example")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 10 {
		t.Fatalf("issues = %#v", issues)
	}
}

// A response that is not exact is refused rather than partially believed: a
// branch with no commit or a pull request with no head SHA cannot be classified,
// and guessing would be the failure this evidence exists to prevent.
func TestAnInexactResponseIsRefused(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		body string
		call func(*Client) error
	}{
		{
			name: "branch without a commit",
			body: `[{"name":"main","commit":{"sha":""}}]`,
			call: func(client *Client) error {
				_, _, err := client.ListBranches(context.Background(), "o", "r")
				return err
			},
		},
		{
			name: "pull request without a head",
			body: `[{"number":1,"state":"open","head":{"ref":"x","sha":"short"},"base":{"ref":"main"}}]`,
			call: func(client *Client) error {
				_, _, err := client.ListPullRequests(context.Background(), "o", "r")
				return err
			},
		},
		{
			name: "pull request in an unknown state",
			body: `[{"number":1,"state":"merged","head":{"ref":"x","sha":"` + sha(6) + `"},"base":{"ref":"main"}}]`,
			call: func(client *Client) error {
				_, _, err := client.ListPullRequests(context.Background(), "o", "r")
				return err
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := observationClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(testCase.body))
			})
			if err := testCase.call(client); err == nil {
				t.Fatal("an inexact response was accepted")
			}
		})
	}
}

// Review-thread resolution exists only in GraphQL, and an unresolved
// conversation on a merged pull request is unfinished work a retirement decision
// must not step over.
func TestUnresolvedReviewThreadsAreCountedAcrossEveryState(t *testing.T) {
	t.Parallel()
	client := observationClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/graphql") || request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"repository":{"pullRequests":{
      "pageInfo":{"hasNextPage":false,"endCursor":null},
      "nodes":[
        {"number":1,"reviewThreads":{"pageInfo":{"hasNextPage":false},
          "nodes":[{"isResolved":true,"isOutdated":false},{"isResolved":false,"isOutdated":false}]}},
        {"number":2,"reviewThreads":{"pageInfo":{"hasNextPage":false},
          "nodes":[{"isResolved":false,"isOutdated":true}]}}]}}}}`))
	})
	count, _, err := client.CountUnresolvedReviewThreads(context.Background(), "example-org", "example")
	if err != nil {
		t.Fatal(err)
	}
	// Two unresolved, one of them outdated. An outdated conversation is one whose
	// lines were rewritten; nobody closed it, so it still counts.
	if count != 2 {
		t.Fatalf("count = %d", count)
	}
}

// More conversations than one page holds would report a smaller number than
// exists, which is the direction that lets unfinished work through.
func TestATruncatedReviewThreadPageIsRefused(t *testing.T) {
	t.Parallel()
	client := observationClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"repository":{"pullRequests":{
      "pageInfo":{"hasNextPage":false},
      "nodes":[{"number":7,"reviewThreads":{"pageInfo":{"hasNextPage":true},"nodes":[]}}]}}}}`))
	})
	if _, _, err := client.CountUnresolvedReviewThreads(
		context.Background(), "example-org", "example",
	); err == nil {
		t.Fatal("a truncated review-thread page was accepted")
	}
}

func TestAGraphQLErrorIsNotReadAsZeroUnresolvedThreads(t *testing.T) {
	t.Parallel()
	client := observationClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"errors":[{"message":"Resource not accessible"}],"data":null}`))
	})
	if _, _, err := client.CountUnresolvedReviewThreads(
		context.Background(), "example-org", "example",
	); err == nil {
		t.Fatal("a GraphQL error was read as an empty result")
	}
}
