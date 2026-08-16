package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type tokenSourceFunc func(context.Context, string) (InstallationToken, error)

func (function tokenSourceFunc) Token(
	ctx context.Context,
	installationID string,
) (InstallationToken, error) {
	return function(ctx, installationID)
}

func fixedToken(value string, expiresAt time.Time) TokenSource {
	return tokenSourceFunc(func(_ context.Context, _ string) (InstallationToken, error) {
		return InstallationToken{
			Value: value, ExpiresAt: expiresAt,
			Permissions: map[string]string{"metadata": "read"}, RepositorySelection: "all",
		}, nil
	})
}

func testClient(
	t *testing.T,
	server *httptest.Server,
	tokens TokenSource,
	configure func(*Config),
) *Client {
	t.Helper()
	httpClient := *server.Client()
	httpClient.Timeout = 2 * time.Second
	config := Config{
		BaseURL: server.URL + "/", HTTPClient: &httpClient, TokenSource: tokens,
		InstallationID: "installation:test", AllowInsecureLoopback: true,
		Now: func() time.Time { return time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC) },
		PermissionContract: PermissionContract{
			Permissions: map[string]string{"metadata": "read"}, RepositorySelection: "all",
		},
	}
	if configure != nil {
		configure(&config)
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestPermissionContractRejectsMissingAndExcessPermissionsBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	for name, permissions := range map[string]map[string]string{
		"missing":  {"contents": "read"},
		"excess":   {"metadata": "read", "contents": "read"},
		"stronger": {"metadata": "write"},
	} {
		t.Run(name, func(t *testing.T) {
			tokens := tokenSourceFunc(func(context.Context, string) (InstallationToken, error) {
				return InstallationToken{
					Value: "token", ExpiresAt: now.Add(time.Hour),
					Permissions: permissions, RepositorySelection: "all",
				}, nil
			})
			client := testClient(t, server, tokens, nil)
			_, _, _, err := client.GetRepository(
				context.Background(), "example", "repository", "",
			)
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.Kind != ErrorPermissionContract {
				t.Fatalf("permission mismatch error=%v", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("permission mismatch made %d provider requests", requests.Load())
	}
}

func TestGetRepositorySendsPinnedHeadersAndDecodesMetadata(t *testing.T) {
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/example/repository" ||
			request.Header.Get("Authorization") != "Bearer secret-token" ||
			request.Header.Get("Accept") != "application/vnd.github+json" ||
			request.Header.Get("X-GitHub-Api-Version") != APIVersion ||
			request.Header.Get("User-Agent") != DefaultUserAgent {
			t.Errorf("unexpected request: %s %#v", request.URL, request.Header)
		}
		writer.Header().Set("ETag", `"repo-etag"`)
		writer.Header().Set("X-GitHub-Request-Id", "request-1")
		writer.Header().Set("X-RateLimit-Limit", "5000")
		writer.Header().Set("X-RateLimit-Remaining", "4999")
		writer.Header().Set("X-RateLimit-Used", "1")
		writer.Header().Set("X-RateLimit-Reset", fmt.Sprint(now.Add(time.Hour).Unix()))
		_, _ = writer.Write([]byte(repositoryJSON(1, "example", "repository", false)))
	}))
	defer server.Close()
	client := testClient(t, server, fixedToken("secret-token", now.Add(time.Hour)), nil)
	repository, meta, notModified, err := client.GetRepository(
		context.Background(), "example", "repository", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if notModified || repository.ID != 1 || repository.Owner != "example" ||
		meta.RequestID != "request-1" || meta.ETag != `"repo-etag"` ||
		!meta.Rate.Known || meta.Rate.Remaining != 4999 {
		t.Fatalf("repository=%#v meta=%#v notModified=%v", repository, meta, notModified)
	}
}

func TestListInstallationRepositoriesFollowsBoundedLinkAndSorts(t *testing.T) {
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	server := httptest.NewServer(nil)
	defer server.Close()
	server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/installation/repositories" || request.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected inventory request: %s", request.URL)
		}
		switch request.URL.Query().Get("page") {
		case "1":
			writer.Header().Set(
				"Link", fmt.Sprintf(
					`<%s/installation/repositories?per_page=100&page=2>; rel="next"`, server.URL,
				),
			)
			writer.Header().Set("X-GitHub-Request-Id", "page-1")
			_, _ = writer.Write([]byte(`{"total_count":2,"repositories":[` +
				repositoryJSON(2, "example", "second", true) + `]}`))
		case "2":
			writer.Header().Set("X-GitHub-Request-Id", "page-2")
			_, _ = writer.Write([]byte(`{"total_count":2,"repositories":[` +
				repositoryJSON(1, "example", "first", false) + `]}`))
		default:
			http.Error(writer, "unexpected page", http.StatusBadRequest)
		}
	})
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)
	inventory, err := client.ListInstallationRepositories(context.Background(), 2000)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Pages != 2 || inventory.TotalCount != 2 ||
		len(inventory.Repositories) != 2 || inventory.Repositories[0].ID != 1 ||
		inventory.Repositories[1].ID != 2 || strings.Join(inventory.RequestIDs, ",") != "page-1,page-2" {
		t.Fatalf("inventory = %#v", inventory)
	}
}

func TestPaginationRejectsCrossOriginLink(t *testing.T) {
	var escaped atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		escaped.Add(1)
	}))
	defer evil.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set(
			"Link", fmt.Sprintf(
				`<%s/installation/repositories?per_page=100&page=2>; rel="next"`, evil.URL,
			),
		)
		_, _ = writer.Write([]byte(`{"total_count":2,"repositories":[` +
			repositoryJSON(1, "example", "first", false) + `]}`))
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)
	if _, err := client.ListInstallationRepositories(context.Background(), 2000); err == nil ||
		escaped.Load() != 0 {
		t.Fatalf("cross-origin pagination err=%v escaped=%d", err, escaped.Load())
	}
}

func TestAPIErrorDoesNotExposeTokenOrResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-GitHub-Request-Id", "request-secret")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"message":"private-repository-name secret-token"}`))
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("secret-token", now.Add(time.Hour)), nil)
	_, _, _, err := client.GetRepository(context.Background(), "example", "repository", "")
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Kind != ErrorAuthentication ||
		strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "private-repository-name") {
		t.Fatalf("unsafe API error: %#v %v", apiError, err)
	}
}

func TestAPIErrorSafeOperationFailureEvidenceIsBounded(t *testing.T) {
	apiError := &APIError{
		Kind: ErrorRateLimited, StatusCode: http.StatusForbidden,
		RequestID: "request-123", RetryAfter: 3 * time.Second,
		Cause: errors.New("secret response body"),
	}

	evidence := apiError.SafeOperationFailureEvidence()
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"kind":"rate-limited"`) ||
		!strings.Contains(text, `"status_code":403`) ||
		!strings.Contains(text, `"request_id":"request-123"`) ||
		!strings.Contains(text, `"retry_after_ms":3000`) ||
		strings.Contains(text, "secret response body") {
		t.Fatalf("unsafe or incomplete evidence: %s", text)
	}
}

func TestExpiredTokenPreventsHTTPRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now), nil)
	if _, _, _, err := client.GetRepository(context.Background(), "example", "repository", ""); err == nil {
		t.Fatal("expected expired token rejection")
	}
	if requests.Load() != 0 {
		t.Fatalf("expired token made %d requests", requests.Load())
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/other" {
			redirected.Add(1)
			return
		}
		http.Redirect(writer, request, "/other", http.StatusMovedPermanently)
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)
	_, _, _, err := client.GetRepository(context.Background(), "example", "repository", "")
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusMovedPermanently || redirected.Load() != 0 {
		t.Fatalf("redirect handling err=%v redirected=%d", err, redirected.Load())
	}
}

func TestResponseLimitAndNotModified(t *testing.T) {
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("If-None-Match") == `"cached"` {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = writer.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer server.Close()
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), func(config *Config) {
		config.MaxResponseBytes = 1024
	})
	if _, _, _, err := client.GetRepository(context.Background(), "example", "repository", ""); err == nil {
		t.Fatal("expected response limit failure")
	}
	_, _, notModified, err := client.GetRepository(
		context.Background(), "example", "repository", `"cached"`,
	)
	if err != nil || !notModified {
		t.Fatalf("not-modified result=%v err=%v", notModified, err)
	}
}

func TestRateLimitObservationBlocksNextRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.Header().Set("X-RateLimit-Limit", "5000")
		writer.Header().Set("X-RateLimit-Remaining", "0")
		writer.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(2*time.Second).Unix()))
		_, _ = writer.Write([]byte(repositoryJSON(1, "example", "repository", false)))
	}))
	defer server.Close()
	now := time.Now()
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), func(config *Config) {
		config.Now = time.Now
	})
	if _, _, _, err := client.GetRepository(context.Background(), "example", "repository", ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, _, _, err := client.GetRepository(ctx, "example", "repository", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rate-limited request error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("rate scheduler sent %d requests", requests.Load())
	}
}

func TestSchedulerBoundsConcurrentReads(t *testing.T) {
	scheduler, err := NewScheduler(1, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	release, err := scheduler.Acquire(context.Background(), "installation:test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := scheduler.Acquire(ctx, "installation:test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second scheduler acquire = %v", err)
	}
	release()
	second, err := scheduler.Acquire(context.Background(), "installation:test")
	if err != nil {
		t.Fatal(err)
	}
	second()
}

func TestSchedulerRechecksRateLimitAfterSemaphoreWait(t *testing.T) {
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	scheduler, err := NewScheduler(1, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	firstRelease, err := scheduler.Acquire(context.Background(), "installation:test")
	if err != nil {
		t.Fatal(err)
	}

	initialCheck := make(chan struct{})
	continueCheck := make(chan struct{})
	postAcquireCheck := make(chan struct{})
	var nowCalls atomic.Int32
	scheduler.now = func() time.Time {
		switch nowCalls.Add(1) {
		case 1:
			close(initialCheck)
			<-continueCheck
		case 3:
			close(postAcquireCheck)
		}
		return base
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		release, acquireErr := scheduler.Acquire(ctx, "installation:test")
		if release != nil {
			release()
		}
		result <- acquireErr
	}()
	<-initialCheck
	scheduler.Observe("installation:test", http.StatusTooManyRequests, ResponseMeta{
		RetryAfter: time.Hour,
	})
	close(continueCheck)
	firstRelease()
	select {
	case <-postAcquireCheck:
	case <-time.After(time.Second):
		t.Fatal("queued acquire did not recheck the learned rate limit")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued acquire bypassed learned rate limit: %v", err)
	}
}

func TestTokenSourceFailureIsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	tokens := tokenSourceFunc(func(context.Context, string) (InstallationToken, error) {
		return InstallationToken{}, errors.New("secret-key-material")
	})
	client := testClient(t, server, tokens, nil)
	_, _, _, err := client.GetRepository(context.Background(), "example", "repository", "")
	if err == nil || strings.Contains(err.Error(), "secret-key-material") {
		t.Fatalf("token source error leaked: %v", err)
	}
}

func TestInventoryRejectsReportedCountAboveBoundBeforeNextPage(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte(`{"total_count":2001,"repositories":[]}`))
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)
	if _, err := client.ListInstallationRepositories(context.Background(), 2000); err == nil {
		t.Fatal("expected inventory bound rejection")
	}
	if requests.Load() != 1 {
		t.Fatalf("inventory made %d requests", requests.Load())
	}
}

func repositoryJSON(id int64, owner, name string, fork bool) string {
	return fmt.Sprintf(
		`{"id":%d,"node_id":"node-%d","name":%q,"full_name":%q,`+
			`"private":false,"visibility":"public","fork":%t,"archived":false,`+
			`"disabled":false,"default_branch":"main","html_url":%q,"owner":{"login":%q}}`,
		id, id, name, owner+"/"+name, fork, "https://github.com/"+owner+"/"+name, owner,
	)
}
