package source

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestCheckerClassifiesPinnedAndUnpinnedContent(t *testing.T) {
	body := "official contract\n"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.Header.Get("User-Agent") == "" ||
			!strings.HasPrefix(request.Header.Get("Accept"), "text/markdown,") ||
			!strings.Contains(request.Header.Get("Accept"), "application/atom+xml") {
			t.Fatalf("request = %#v", request)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	checker := NewCheckerForTest(client, func() time.Time {
		return time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
	})
	entry := Entry{ID: "official-source", URL: "https://docs.example.test/reference"}
	first, err := checker.Check(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "not-proven" || first.ObservedAt != "2026-07-11T01:02:03Z" ||
		first.ObservedDigest == "" || first.Bytes != len(body) {
		t.Fatalf("first = %#v", first)
	}
	entry.ContentDigest = &first.ObservedDigest
	unchanged, err := checker.Check(context.Background(), entry)
	if err != nil || unchanged.State != "unchanged" {
		t.Fatalf("unchanged = %#v, err = %v", unchanged, err)
	}
	other := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	entry.ContentDigest = &other
	changed, err := checker.Check(context.Background(), entry)
	if err != nil || changed.State != "changed-unreviewed" {
		t.Fatalf("changed = %#v, err = %v", changed, err)
	}
}

func TestCheckerRejectsOversizedAndUnsafeSources(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{},
			Body:    io.NopCloser(strings.NewReader(strings.Repeat("x", maxSourceResponseBytes+1))),
			Request: request,
		}, nil
	})}
	checker := NewCheckerForTest(client, time.Now)
	if _, err := checker.Check(context.Background(), Entry{
		ID: "oversized", URL: "https://docs.example.test/reference",
	}); err == nil {
		t.Fatal("oversized source was accepted")
	}
	for _, rawURL := range []string{
		"http://docs.example.test", "https://user@example.test", "https://example.test/#fragment",
	} {
		if _, err := checker.Check(context.Background(), Entry{ID: "unsafe", URL: rawURL}); err == nil {
			t.Fatalf("unsafe URL %q was accepted", rawURL)
		}
	}
	for _, rawIP := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "::1", "ff02::1"} {
		if !unsafeSourceIP(net.ParseIP(rawIP)) {
			t.Fatalf("unsafe IP %s was accepted", rawIP)
		}
	}
}

func TestCheckerRequiresReproducibleVerificationBaseline(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf("request=%d\n", requests))),
			Request:    request,
		}, nil
	})}
	checker := NewCheckerForTest(client, time.Now)
	_, err := checker.CheckReproducible(context.Background(), Entry{
		ID: "dynamic-source", URL: "https://docs.example.test/reference",
	})
	if err == nil || !strings.Contains(err.Error(), "is not reproducible") {
		t.Fatalf("err = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestCheckerAcceptsReproducibleVerificationBaseline(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/markdown"}},
			Body:       io.NopCloser(strings.NewReader("stable official contract\n")),
			Request:    request,
		}, nil
	})}
	checker := NewCheckerForTest(client, time.Now)
	result, err := checker.CheckReproducible(context.Background(), Entry{
		ID: "stable-source", URL: "https://docs.example.test/reference.md",
	})
	if err != nil || result.ObservedDigest == "" || requests != 2 {
		t.Fatalf("result = %#v, requests = %d, err = %v", result, requests, err)
	}
}
