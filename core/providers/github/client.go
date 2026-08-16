package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BaseURL               string
	HTTPClient            *http.Client
	TokenSource           TokenSource
	InstallationID        string
	Scheduler             *Scheduler
	APIVersion            string
	UserAgent             string
	MaxResponseBytes      int64
	Now                   func() time.Time
	AllowInsecureLoopback bool
	PermissionContract    PermissionContract
	// InventoryAccount holds the GitHub account (login + type) that the
	// installation observes. It selects the inventory endpoint and decoder:
	// the App installation-token contract lists repositories at
	// /installation/repositories, while a personal access token (gh CLI)
	// cannot use that endpoint and is instead enumerated through the account
	// list endpoints. Empty defaults to the installation-token strategy.
	InventoryAccount InventoryAccount
}

// InventoryAccount identifies the GitHub account an inventory read targets.
type InventoryAccount struct {
	Login string
	Type  string // "organization" or "user"
}

func (account InventoryAccount) IsZero() bool {
	return account.Login == "" && account.Type == ""
}

type Client struct {
	baseURL          *url.URL
	uploadBaseURL    *url.URL
	httpClient       *http.Client
	tokens           TokenSource
	installationID   string
	scheduler        *Scheduler
	apiVersion       string
	userAgent        string
	maxBody          int64
	now              func() time.Time
	permissions      PermissionContract
	inventoryAccount InventoryAccount
}

type getResult struct {
	Body       []byte
	StatusCode int
	Header     http.Header
	Meta       ResponseMeta
}

func NewClient(config Config) (*Client, error) {
	base := config.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("invalid GitHub API base URL")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	if parsed.Scheme != "https" {
		if !config.AllowInsecureLoopback || parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
			return nil, fmt.Errorf("GitHub API base URL must use HTTPS")
		}
	}
	if config.TokenSource == nil || config.InstallationID == "" {
		return nil, fmt.Errorf("GitHub client requires an installation token source and identity")
	}
	if err := validatePermissionContract(config.PermissionContract); err != nil {
		return nil, fmt.Errorf("GitHub client permission contract is invalid: %w", err)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	scheduler := config.Scheduler
	if scheduler == nil {
		scheduler, err = NewScheduler(4, now)
		if err != nil {
			return nil, err
		}
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	if httpClient.Timeout <= 0 {
		return nil, fmt.Errorf("GitHub HTTP client requires a positive timeout")
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	apiVersion := config.APIVersion
	if apiVersion == "" {
		apiVersion = APIVersion
	}
	if apiVersion != APIVersion {
		return nil, fmt.Errorf("unsupported GitHub API version %q", apiVersion)
	}
	userAgent := config.UserAgent
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	if strings.ContainsAny(userAgent, "\x00\r\n") {
		return nil, fmt.Errorf("invalid GitHub User-Agent")
	}
	maxBody := config.MaxResponseBytes
	if maxBody == 0 {
		maxBody = DefaultBodyLimit
	}
	if maxBody < 1024 || maxBody > 64<<20 {
		return nil, fmt.Errorf("GitHub response limit must be between 1 KiB and 64 MiB")
	}
	uploadBase := *parsed
	if strings.EqualFold(parsed.Hostname(), "api.github.com") && parsed.Path == "/" {
		uploadBase.Host = "uploads.github.com"
	}
	return &Client{
		baseURL: parsed, httpClient: httpClient, tokens: config.TokenSource,
		uploadBaseURL:  &uploadBase,
		installationID: config.InstallationID, scheduler: scheduler,
		apiVersion: apiVersion, userAgent: userAgent, maxBody: maxBody, now: now,
		permissions: config.PermissionContract, inventoryAccount: config.InventoryAccount,
	}, nil
}

func (client *Client) get(
	ctx context.Context,
	target *url.URL,
	ifNoneMatch string,
) (getResult, error) {
	return client.request(ctx, http.MethodGet, target, nil, ifNoneMatch)
}

func (client *Client) request(
	ctx context.Context,
	method string,
	target *url.URL,
	body []byte,
	ifNoneMatch string,
) (getResult, error) {
	contentType := ""
	if len(body) != 0 {
		contentType = "application/json"
	}
	return client.requestWithContentType(ctx, method, target, body, ifNoneMatch, contentType)
}

func (client *Client) requestWithContentType(
	ctx context.Context,
	method string,
	target *url.URL,
	body []byte,
	ifNoneMatch string,
	contentType string,
) (getResult, error) {
	if err := client.validateTarget(target); err != nil {
		return getResult{}, err
	}
	release, err := client.scheduler.Acquire(ctx, client.installationID)
	if err != nil {
		return getResult{}, err
	}
	defer release()
	token, err := client.tokens.Token(ctx, client.installationID)
	if err != nil {
		return getResult{}, &APIError{
			Kind:  ErrorAuthentication,
			Cause: errors.New("installation token source failed without exposing its internal error"),
		}
	}
	if err := validateToken(token, client.now()); err != nil {
		return getResult{}, err
	}
	if err := client.permissions.Validate(token); err != nil {
		return getResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return getResult{}, fmt.Errorf("build GitHub request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token.Value)
	request.Header.Set("X-GitHub-Api-Version", client.apiVersion)
	request.Header.Set("User-Agent", client.userAgent)
	if len(body) != 0 {
		if contentType != "application/json" && contentType != "application/octet-stream" {
			return getResult{}, fmt.Errorf("unsupported GitHub request content type")
		}
		request.Header.Set("Content-Type", contentType)
	}
	if ifNoneMatch != "" {
		if strings.ContainsAny(ifNoneMatch, "\x00\r\n") {
			return getResult{}, fmt.Errorf("invalid ETag")
		}
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return getResult{}, ctx.Err()
		}
		return getResult{}, &APIError{Kind: ErrorTransient, Cause: err}
	}
	defer response.Body.Close()
	meta := parseResponseMeta(response.Header)
	client.scheduler.Observe(client.installationID, response.StatusCode, meta)
	result := getResult{StatusCode: response.StatusCode, Header: response.Header.Clone(), Meta: meta}
	if response.StatusCode == http.StatusNotModified {
		return result, nil
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, client.maxBody+1))
	if err != nil {
		return result, &APIError{
			Kind: ErrorResponse, StatusCode: response.StatusCode,
			RequestID: meta.RequestID, Cause: fmt.Errorf("read response body: %w", err),
		}
	}
	if int64(len(responseBody)) > client.maxBody {
		return result, &APIError{
			Kind: ErrorResponse, StatusCode: response.StatusCode,
			RequestID: meta.RequestID, Cause: fmt.Errorf("response exceeded %d bytes", client.maxBody),
		}
	}
	result.Body = responseBody
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, &APIError{
			Kind: classifyStatus(response.StatusCode, responseBody, meta), StatusCode: response.StatusCode,
			RequestID: meta.RequestID, RetryAfter: meta.RetryAfter,
		}
	}
	return result, nil
}

func (client *Client) endpoint(relative string, query url.Values) (*url.URL, error) {
	if relative == "" || strings.HasPrefix(relative, "/") || strings.Contains(relative, "\\") {
		return nil, fmt.Errorf("GitHub endpoint must be a relative API path")
	}
	target, err := client.baseURL.Parse(relative)
	if err != nil {
		return nil, err
	}
	target.RawQuery = query.Encode()
	if err := client.validateTarget(target); err != nil {
		return nil, err
	}
	return target, nil
}

func (client *Client) validateTarget(target *url.URL) error {
	if target == nil || target.User != nil || target.Scheme != client.baseURL.Scheme || target.Fragment != "" {
		return fmt.Errorf("GitHub request target escaped the configured API origin")
	}
	base := client.baseURL
	if strings.EqualFold(target.Host, client.uploadBaseURL.Host) {
		base = client.uploadBaseURL
	} else if !strings.EqualFold(target.Host, client.baseURL.Host) {
		return fmt.Errorf("GitHub request target escaped the configured API origin")
	}
	if !strings.HasPrefix(target.Path, base.Path) {
		return fmt.Errorf("GitHub request target escaped the configured API path")
	}
	return nil
}

func (client *Client) releaseUploadEndpoint(owner, repository string, releaseID int64, name string) (*url.URL, error) {
	if releaseID < 1 || !githubOwnerPattern.MatchString(owner) || !githubNamePattern.MatchString(repository) ||
		!safeReleaseAssetName(name) {
		return nil, fmt.Errorf("GitHub release upload target is invalid")
	}
	target := *client.uploadBaseURL
	target.Path = strings.TrimSuffix(target.Path, "/") + "/repos/" + url.PathEscape(owner) + "/" +
		url.PathEscape(repository) + "/releases/" + strconv.FormatInt(releaseID, 10) + "/assets"
	query := url.Values{}
	query.Set("name", name)
	target.RawQuery = query.Encode()
	if err := client.validateTarget(&target); err != nil {
		return nil, err
	}
	return &target, nil
}

func validateToken(token InstallationToken, now time.Time) error {
	if token.Value == "" || strings.ContainsAny(token.Value, "\x00\r\n") {
		return fmt.Errorf("GitHub installation token is missing or malformed")
	}
	if !token.ExpiresAt.After(now.Add(30 * time.Second)) {
		return fmt.Errorf("GitHub installation token is expired or too close to expiry")
	}
	if err := validateEffectivePermissions(token.Permissions, token.RepositorySelection); err != nil {
		return fmt.Errorf("GitHub installation token permission evidence is invalid")
	}
	return nil
}

func (client *Client) permissionEvidence(ctx context.Context) (PermissionEvidence, error) {
	token, err := client.tokens.Token(ctx, client.installationID)
	if err != nil {
		return PermissionEvidence{}, &APIError{
			Kind:  ErrorAuthentication,
			Cause: errors.New("installation token source failed without exposing its internal error"),
		}
	}
	if err := validateToken(token, client.now()); err != nil {
		return PermissionEvidence{}, err
	}
	if err := client.permissions.Validate(token); err != nil {
		return PermissionEvidence{}, err
	}
	return client.permissions.Evidence(token), nil
}

func parseResponseMeta(header http.Header) ResponseMeta {
	meta := ResponseMeta{
		RequestID: header.Get("X-GitHub-Request-Id"), ETag: header.Get("ETag"),
	}
	if header.Get("X-RateLimit-Remaining") != "" {
		meta.Rate.Known = true
		meta.Rate.Limit, _ = strconv.Atoi(header.Get("X-RateLimit-Limit"))
		meta.Rate.Remaining, _ = strconv.Atoi(header.Get("X-RateLimit-Remaining"))
		meta.Rate.Used, _ = strconv.Atoi(header.Get("X-RateLimit-Used"))
	}
	if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
		meta.Rate.ResetAt = time.Unix(reset, 0).UTC()
	}
	if retryAfter, err := strconv.Atoi(header.Get("Retry-After")); err == nil && retryAfter > 0 {
		meta.RetryAfter = time.Duration(retryAfter) * time.Second
	} else if retryAt, err := http.ParseTime(header.Get("Retry-After")); err == nil {
		meta.RetryAfter = time.Until(retryAt)
		if meta.RetryAfter < 0 {
			meta.RetryAfter = 0
		}
	}
	return meta
}

func decodeJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("GitHub response contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing GitHub response: %w", err)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
