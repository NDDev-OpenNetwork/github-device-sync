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

func TestCompareBranchesReturnsSortedBoundedFiles(t *testing.T) {
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	blobA := strings.Repeat("c", 40)
	blobB := strings.Repeat("d", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/example/repository/compare/main...task/gds" {
			t.Errorf("path=%q", request.URL.Path)
		}
		_, _ = fmt.Fprintf(writer, `{"status":"ahead","ahead_by":2,"behind_by":0,"total_commits":2,"base_commit":{"sha":%q},"head_commit":{"sha":%q},"merge_base_commit":{"sha":%q},"files":[{"sha":%q,"filename":"z.txt","status":"modified"},{"sha":%q,"filename":"a.txt","status":"added"}]}`,
			base, head, base, blobB, blobA)
	}))
	defer server.Close()
	client := testClient(t, server, fixedToken("token", time.Now().Add(time.Hour)), nil)
	comparison, _, err := client.CompareBranches(
		context.Background(), "example", "repository", "main", "task/gds",
	)
	if err != nil || comparison.Status != "ahead" || comparison.BaseSHA != base ||
		comparison.HeadSHA != head || len(comparison.Files) != 2 || comparison.Files[0].Path != "a.txt" {
		t.Fatalf("comparison=%#v err=%v", comparison, err)
	}
}

func TestCompareBranchesRejectsDuplicateFiles(t *testing.T) {
	oid := strings.Repeat("a", 40)
	blob := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(writer, `{"status":"ahead","ahead_by":1,"behind_by":0,"total_commits":1,"base_commit":{"sha":%q},"head_commit":{"sha":%q},"merge_base_commit":{"sha":%q},"files":[{"sha":%q,"filename":"file.txt","status":"modified"},{"sha":%q,"filename":"file.txt","status":"modified"}]}`,
			oid, oid, oid, blob, blob)
	}))
	defer server.Close()
	client := testClient(t, server, fixedToken("token", time.Now().Add(time.Hour)), nil)
	if _, _, err := client.CompareBranches(
		context.Background(), "example", "repository", "main", "task/gds",
	); err == nil {
		t.Fatal("duplicate comparison files were accepted")
	}
}
