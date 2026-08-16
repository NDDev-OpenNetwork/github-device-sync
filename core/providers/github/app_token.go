package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/secrets"
)

const (
	appTokenResponseLimit = int64(64 << 10)
	appJWTLifetime        = 9 * time.Minute
	appJWTClockSkew       = time.Minute
	tokenRefreshWindow    = 5 * time.Minute
)

var providerNumericIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

type AppTokenConfig struct {
	AppID                 string
	InstallationIDs       map[string]string
	PrivateKeyReference   string
	Secrets               secrets.Store
	BaseURL               string
	HTTPClient            *http.Client
	APIVersion            string
	UserAgent             string
	Now                   func() time.Time
	AllowInsecureLoopback bool
	Scheduler             *Scheduler
}

type AppTokenSource struct {
	appID         string
	installations map[string]string
	privateKeyRef string
	secrets       secrets.Store
	baseURL       *url.URL
	httpClient    *http.Client
	apiVersion    string
	userAgent     string
	now           func() time.Time
	cache         map[string]*installationTokenCache
	scheduler     *Scheduler
}

type installationTokenCache struct {
	mu    sync.Mutex
	token InstallationToken
}

func NewAppTokenSource(config AppTokenConfig) (*AppTokenSource, error) {
	if !providerNumericIDPattern.MatchString(config.AppID) || config.Secrets == nil ||
		config.PrivateKeyReference == "" || len(config.InstallationIDs) == 0 {
		return nil, fmt.Errorf("GitHub App token source configuration is incomplete")
	}
	installations := make(map[string]string, len(config.InstallationIDs))
	for logical, providerID := range config.InstallationIDs {
		if logical == "" || strings.ContainsAny(logical, "\x00\r\n") ||
			!providerNumericIDPattern.MatchString(providerID) {
			return nil, fmt.Errorf("GitHub App installation mapping is invalid")
		}
		installations[logical] = providerID
	}
	base := config.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid GitHub App API base URL")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	if parsed.Scheme != "https" && (!config.AllowInsecureLoopback ||
		parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
		return nil, fmt.Errorf("GitHub App API base URL must use HTTPS")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	if httpClient.Timeout <= 0 {
		return nil, fmt.Errorf("GitHub App HTTP client requires a positive timeout")
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
		return nil, fmt.Errorf("invalid GitHub App User-Agent")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	cache := make(map[string]*installationTokenCache, len(installations))
	for logical := range installations {
		cache[logical] = &installationTokenCache{}
	}
	scheduler := config.Scheduler
	if scheduler == nil {
		defaultScheduler, err := NewScheduler(1, now)
		if err != nil {
			return nil, err
		}
		scheduler = defaultScheduler
	}
	return &AppTokenSource{
		appID: config.AppID, installations: installations,
		privateKeyRef: config.PrivateKeyReference, secrets: config.Secrets,
		baseURL: parsed, httpClient: httpClient, apiVersion: apiVersion,
		userAgent: userAgent, now: now, cache: cache, scheduler: scheduler,
	}, nil
}

func (source *AppTokenSource) Token(
	ctx context.Context,
	installationID string,
) (InstallationToken, error) {
	providerID, found := source.installations[installationID]
	if !found {
		return InstallationToken{}, &APIError{Kind: ErrorAuthorization}
	}
	cache := source.cache[installationID]
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := source.now().UTC()
	if cache.token.ExpiresAt.After(now.Add(tokenRefreshWindow)) {
		return cloneToken(cache.token), nil
	}
	privateKey, err := source.secrets.Get(ctx, source.privateKeyRef)
	if err != nil {
		return InstallationToken{}, &APIError{
			Kind:  ErrorAuthentication,
			Cause: errors.New("GitHub App private key is unavailable"),
		}
	}
	defer clear(privateKey)
	key, err := parseAppPrivateKey(privateKey)
	if err != nil {
		return InstallationToken{}, &APIError{
			Kind:  ErrorAuthentication,
			Cause: errors.New("GitHub App private key is invalid"),
		}
	}
	jwt, err := signAppJWT(source.appID, key, now)
	if err != nil {
		return InstallationToken{}, &APIError{
			Kind:  ErrorAuthentication,
			Cause: errors.New("GitHub App authentication could not be signed"),
		}
	}
	token, err := source.requestInstallationToken(ctx, installationID, providerID, jwt, now)
	if err != nil {
		return InstallationToken{}, err
	}
	cache.token = cloneToken(token)
	return cloneToken(token), nil
}

func (source *AppTokenSource) requestInstallationToken(
	ctx context.Context,
	installationID string,
	providerID string,
	jwt string,
	now time.Time,
) (InstallationToken, error) {
	target, err := source.baseURL.Parse("app/installations/" + providerID + "/access_tokens")
	if err != nil || target.User != nil || target.Scheme != source.baseURL.Scheme ||
		!strings.EqualFold(target.Host, source.baseURL.Host) || target.Fragment != "" ||
		!strings.HasPrefix(target.Path, source.baseURL.Path) {
		return InstallationToken{}, fmt.Errorf("GitHub App token endpoint escaped the configured origin")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), nil)
	if err != nil {
		return InstallationToken{}, fmt.Errorf("build GitHub App token request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("X-GitHub-Api-Version", source.apiVersion)
	request.Header.Set("User-Agent", source.userAgent)
	release, err := source.scheduler.Acquire(ctx, installationID)
	if err != nil {
		return InstallationToken{}, err
	}
	defer release()
	response, err := source.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return InstallationToken{}, ctx.Err()
		}
		return InstallationToken{}, &APIError{Kind: ErrorTransient}
	}
	defer response.Body.Close()
	meta := parseResponseMeta(response.Header)
	source.scheduler.Observe(installationID, response.StatusCode, meta)
	body, readErr := io.ReadAll(io.LimitReader(response.Body, appTokenResponseLimit+1))
	if readErr != nil || int64(len(body)) > appTokenResponseLimit {
		return InstallationToken{}, &APIError{
			Kind: ErrorResponse, StatusCode: response.StatusCode, RequestID: meta.RequestID,
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return InstallationToken{}, &APIError{
			Kind: classifyStatus(response.StatusCode, body, meta), StatusCode: response.StatusCode,
			RequestID: meta.RequestID, RetryAfter: meta.RetryAfter,
		}
	}
	var payload struct {
		Token               string            `json:"token"`
		ExpiresAt           time.Time         `json:"expires_at"`
		Permissions         map[string]string `json:"permissions"`
		RepositorySelection string            `json:"repository_selection"`
	}
	if err := decodeJSON(body, &payload); err != nil || len(payload.Token) > 4096 {
		return InstallationToken{}, &APIError{
			Kind: ErrorResponse, StatusCode: response.StatusCode, RequestID: meta.RequestID,
		}
	}
	token := InstallationToken{
		Value: payload.Token, ExpiresAt: payload.ExpiresAt.UTC(),
		Permissions: payload.Permissions, RepositorySelection: payload.RepositorySelection,
	}
	if err := validateToken(token, now); err != nil {
		return InstallationToken{}, &APIError{
			Kind: ErrorResponse, StatusCode: response.StatusCode, RequestID: meta.RequestID,
		}
	}
	return token, nil
}

func parseAppPrivateKey(value []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(value)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("PEM payload is invalid")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, key.Validate()
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, key.Validate()
}

func signAppJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{
		"iat": now.Add(-appJWTClockSkew).Unix(), "exp": now.Add(appJWTLifetime).Unix(), "iss": appID,
	})
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
