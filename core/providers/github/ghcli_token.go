package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// cliTokenRefreshWindow bounds how long a PAT is cached before it is
	// re-read from the gh CLI. A PAT has no expiry, so a short cache keeps GDS
	// responsive to a rotated or revoked token while avoiding a subprocess
	// invocation on every request.
	cliTokenRefreshWindow = 5 * time.Minute
	// cliIdentityResponseLimit bounds the /user response used to inspect the
	// token's live scopes and identity.
	cliIdentityResponseLimit = int64(64 << 10)
	// ghCLITokenLifetime is the synthetic lifetime assigned to a PAT-derived
	// installation token. GitHub does not report an expiry for OAuth tokens;
	// this keeps the token inside the standard 30-second floor enforced by
	// validateToken while the refresh window controls re-validation.
	ghCLITokenLifetime = time.Hour
)

// CLITokenConfig binds one estate installation account to a gh CLI credential
// source. The gh CLI holds one personal access token (OAuth) for one GitHub
// account; its coarse scopes are a superset of the fine-grained installation
// permission map declared by the estate.
type CLITokenConfig struct {
	// AccountLogin is the GitHub account this source serves (organization or
	// user login), resolved from the estate installation declaration.
	AccountLogin string
	// AccountType is "organization" or "user", resolved from the estate
	// installation declaration. It selects the inventory endpoint.
	AccountType string
	BaseURL     string
	HTTPClient  *http.Client
	APIVersion  string
	UserAgent   string
	Now         func() time.Time
	// AllowInsecureLoopback permits http loopback base URLs for isolated tests.
	AllowInsecureLoopback bool
	// Runner executes `gh auth token`. Defaults to a sandboxed ExecRunner that
	// resolves the gh binary from a bounded path set and a minimal environment.
	Runner CommandRunner
}

// CommandRunner runs an external credential helper with a sandboxed
// environment and bounded output. It mirrors core/secrets.CommandRunner so the
// gh CLI invocation stays inside the same security envelope as the macOS
// Keychain and Linux Secret Service helpers.
type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

// ExecRunner resolves an executable from a bounded candidate list and runs it
// with a sanitized environment that ignores ambient secrets.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
	path, err := resolveExecutable(executable)
	if err != nil {
		return nil, err
	}
	commandContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, path, arguments...)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin:/usr/sbin:/sbin"}
	for _, name := range []string{"HOME", "LANG", "LC_ALL", "LOGNAME", "USER", "XDG_RUNTIME_DIR"} {
		if value, found := os.LookupEnv(name); found && value != "" &&
			!strings.ContainsAny(value, "\x00\r\n") {
			command.Env = append(command.Env, name+"="+value)
		}
	}
	var output bytes.Buffer
	command.Stdout = &boundedBuffer{limit: 8192, buffer: &output}
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, &APIError{Kind: ErrorTransient}
		}
		return nil, &APIError{Kind: ErrorAuthentication}
	}
	return normalizeTokenOutput(output.Bytes())
}

func resolveExecutable(name string) (string, error) {
	candidates := []string{filepath.Join("/usr/local/bin", name), filepath.Join("/usr/bin", name)}
	if runtime.GOOS == "darwin" {
		candidates = []string{filepath.Join("/opt/homebrew/bin", name), filepath.Join("/usr/local/bin", name), filepath.Join("/usr/bin", name)}
	}
	for _, candidate := range candidates {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		info, err := os.Lstat(resolved)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 &&
			info.Mode()&os.ModeSymlink == 0 {
			return resolved, nil
		}
	}
	return "", &APIError{Kind: ErrorAuthentication, Cause: fmt.Errorf("gh CLI was not found")}
}

func normalizeTokenOutput(value []byte) ([]byte, error) {
	result := bytes.TrimSpace(value)
	if len(result) == 0 || len(result) > 4096 {
		return nil, &APIError{Kind: ErrorAuthentication, Cause: fmt.Errorf("gh CLI returned no token")}
	}
	return result, nil
}

type boundedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	if buffer.buffer.Len()+len(value) > buffer.limit {
		return 0, &APIError{Kind: ErrorResponse, Cause: fmt.Errorf("gh CLI output exceeded bound")}
	}
	return buffer.buffer.Write(value)
}

// CLITokenSource is a TokenSource backed by the gh CLI personal access token.
// One gh account can serve both an organization and a personal installation,
// so the logical installation id is used only for cache isolation.
type CLITokenSource struct {
	accountLogin string
	accountType  string
	baseURL      *url.URL
	httpClient   *http.Client
	apiVersion   string
	userAgent    string
	now          func() time.Time
	runner       CommandRunner
	cache        *cliTokenCache
}

type cliTokenCache struct {
	mu    sync.Mutex
	token InstallationToken
}

// NewCLITokenSource validates the gh CLI configuration and returns a token
// source bound to one estate installation account.
func NewCLITokenSource(config CLITokenConfig) (*CLITokenSource, error) {
	if config.AccountLogin == "" {
		return nil, fmt.Errorf("gh CLI token source requires an account login")
	}
	if config.AccountType != "organization" && config.AccountType != "user" {
		return nil, fmt.Errorf("gh CLI token source account type must be organization or user")
	}
	base := config.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid gh CLI API base URL")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	if parsed.Scheme != "https" && (!config.AllowInsecureLoopback ||
		parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
		return nil, fmt.Errorf("gh CLI API base URL must use HTTPS")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	if httpClient.Timeout <= 0 {
		return nil, fmt.Errorf("gh CLI HTTP client requires a positive timeout")
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
		return nil, fmt.Errorf("invalid gh CLI User-Agent")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	runner := config.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	return &CLITokenSource{
		accountLogin: config.AccountLogin, accountType: config.AccountType,
		baseURL: parsed, httpClient: httpClient, apiVersion: apiVersion,
		userAgent: userAgent, now: now, runner: runner,
		cache: &cliTokenCache{},
	}, nil
}

func (source *CLITokenSource) Token(
	ctx context.Context,
	installationID string,
) (InstallationToken, error) {
	cache := source.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := source.now().UTC()
	if cache.token.Value != "" && cache.token.ExpiresAt.After(now.Add(cliTokenRefreshWindow)) {
		return cloneToken(cache.token), nil
	}
	token, err := source.mint(ctx, now)
	if err != nil {
		return InstallationToken{}, err
	}
	cache.token = cloneToken(token)
	return cloneToken(token), nil
}

func (source *CLITokenSource) mint(ctx context.Context, now time.Time) (InstallationToken, error) {
	rawToken, err := source.runner.Run(ctx, "gh", "auth", "token")
	if err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) {
			return InstallationToken{}, apiError
		}
		return InstallationToken{}, &APIError{
			Kind:  ErrorAuthentication,
			Cause: errors.New("gh CLI token source is unavailable"),
		}
	}
	// Normalize here rather than trusting the runner to do it. ExecRunner
	// normalizes its own output, but Runner is an injection point: a runner that
	// returns the raw `gh auth token` line - which ends in a newline - would
	// otherwise reach the Authorization header, where Go rejects the header
	// value and the failure surfaces as an opaque transport error instead of a
	// credential problem. normalizeTokenOutput is idempotent, so the sandboxed
	// runner keeps behaving exactly as before.
	normalized, err := normalizeTokenOutput(rawToken)
	if err != nil {
		clear(rawToken)
		return InstallationToken{}, err
	}
	// The PAT bytes are secret material. Capture the value before clearing the
	// raw buffer so the secret never lingers outside the token, mirroring the
	// App token source's defer-clear of the private key (ADR 0019).
	tokenValue := string(normalized)
	clear(rawToken)
	scopes, err := source.inspectScopes(ctx, tokenValue)
	if err != nil {
		return InstallationToken{}, err
	}
	permissions := scopesToPermissions(scopes)
	if permissions["metadata"] != "read" {
		// The repo scope is the only OAuth scope that grants the metadata:read
		// permission GitHub requires for every repository read. A PAT without it
		// cannot enumerate repositories and must fail closed.
		return InstallationToken{}, &APIError{
			Kind:  ErrorAuthentication,
			Cause: errors.New("gh CLI token is missing the required repo scope"),
		}
	}
	return InstallationToken{
		Value: tokenValue, ExpiresAt: now.Add(ghCLITokenLifetime).UTC(),
		Permissions: permissions, RepositorySelection: "all",
	}, nil
}

func (source *CLITokenSource) inspectScopes(ctx context.Context, token string) ([]string, error) {
	target, err := source.baseURL.Parse("user")
	if err != nil || target.User != nil || target.Scheme != source.baseURL.Scheme ||
		!strings.EqualFold(target.Host, source.baseURL.Host) ||
		target.Fragment != "" || !strings.HasPrefix(target.Path, source.baseURL.Path) {
		return nil, fmt.Errorf("gh CLI identity endpoint escaped the configured origin")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build gh CLI identity request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", source.apiVersion)
	request.Header.Set("User-Agent", source.userAgent)
	response, err := source.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &APIError{Kind: ErrorTransient}
	}
	defer response.Body.Close()
	meta := parseResponseMeta(response.Header)
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, cliIdentityResponseLimit+1))
	if readErr != nil {
		return nil, &APIError{Kind: ErrorResponse, RequestID: meta.RequestID}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &APIError{
			Kind: classifyStatus(response.StatusCode, responseBody, meta), StatusCode: response.StatusCode,
			RequestID: meta.RequestID,
		}
	}
	scopesHeader := response.Header.Get("X-OAuth-Scopes")
	if scopesHeader == "" {
		return nil, &APIError{
			Kind:  ErrorAuthentication,
			Cause: errors.New("gh CLI token did not report OAuth scopes"),
		}
	}
	return parseScopes(scopesHeader), nil
}

// scopesToPermissions maps coarse GitHub OAuth scopes to the fine-grained
// repository permission vocabulary used by the installation token contract.
// The mapping is intentionally conservative: each scope grants exactly the
// permissions GitHub documents for it.
func scopesToPermissions(scopes []string) map[string]string {
	permissions := make(map[string]string, 8)
	for _, scope := range scopes {
		switch strings.TrimSpace(scope) {
		case "repo":
			permissions["actions"] = "write"
			permissions["administration"] = "write"
			permissions["checks"] = "write"
			permissions["contents"] = "write"
			permissions["custom_properties"] = "write"
			permissions["metadata"] = "read"
			permissions["pull_requests"] = "write"
			permissions["statuses"] = "read"
		case "read:org":
			permissions["organization_administration"] = "read"
		case "workflow":
			permissions["workflows"] = "write"
		}
	}
	return permissions
}

func parseScopes(header string) []string {
	var scopes []string
	for _, scope := range strings.Split(header, ",") {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}
