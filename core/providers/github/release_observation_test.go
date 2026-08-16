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

func TestReleaseObservationDistinguishesExistingAndAbsentTargets(t *testing.T) {
	oid := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/example/repository/git/ref/tags/1.4.0":
			_, _ = fmt.Fprintf(writer, `{"ref":"refs/tags/1.4.0","object":{"sha":%q}}`, oid)
		case "/repos/example/repository/releases/tags/1.4.0":
			_, _ = writer.Write([]byte(releaseJSON(21, "example", "repository", "1.4.0", oid, true)))
		case "/repos/example/repository/git/ref/tags/1.5.0",
			"/repos/example/repository/releases/tags/1.5.0":
			http.NotFound(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("read-token", now.Add(time.Hour)), nil)

	tag, _, found, err := client.GetVersionTagRefOptional(
		context.Background(), "example", "repository", "1.4.0",
	)
	if err != nil || !found || tag.Ref != "refs/tags/1.4.0" || tag.SHA != oid {
		t.Fatalf("tag=%#v found=%v err=%v", tag, found, err)
	}
	release, _, found, err := client.GetReleaseByTagOptional(
		context.Background(), "example", "repository", "1.4.0",
	)
	if err != nil || !found || release.ID != 21 || release.TagName != "1.4.0" {
		t.Fatalf("release=%#v found=%v err=%v", release, found, err)
	}
	if _, _, found, err = client.GetVersionTagRefOptional(
		context.Background(), "example", "repository", "1.5.0",
	); err != nil || found {
		t.Fatalf("absent tag found=%v err=%v", found, err)
	}
	if _, _, found, err = client.GetReleaseByTagOptional(
		context.Background(), "example", "repository", "1.5.0",
	); err != nil || found {
		t.Fatalf("absent release found=%v err=%v", found, err)
	}
}

func TestReleaseObservationFailsClosedOutsideNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("read-token", now.Add(time.Hour)), nil)

	if _, _, found, err := client.GetVersionTagRefOptional(
		context.Background(), "example", "repository", "1.4.0",
	); err == nil || found {
		t.Fatalf("forbidden tag observation found=%v err=%v", found, err)
	}
	if _, _, found, err := client.GetReleaseByTagOptional(
		context.Background(), "example", "repository", "1.4.0",
	); err == nil || found {
		t.Fatalf("forbidden release observation found=%v err=%v", found, err)
	}
}
