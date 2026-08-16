package source

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const maxSourceResponseBytes = 4 << 20

var _, carrierGradeNAT, _ = net.ParseCIDR("100.64.0.0/10")

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Checker struct {
	client HTTPDoer
	now    func() time.Time
}

type CheckResult struct {
	ID             string `json:"id"`
	URL            string `json:"url"`
	ObservedAt     string `json:"observed_at"`
	HTTPStatus     int    `json:"http_status"`
	ContentType    string `json:"content_type,omitempty"`
	ETag           string `json:"etag,omitempty"`
	LastModified   string `json:"last_modified,omitempty"`
	Bytes          int    `json:"bytes"`
	ObservedDigest string `json:"observed_digest"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
	State          string `json:"state"`
}

type NonReproducibleError struct {
	SourceID    string
	FirstDigest string
	NextDigest  string
}

func (err *NonReproducibleError) Error() string {
	return fmt.Sprintf(
		"source %s is not reproducible: consecutive digests %s and %s differ",
		err.SourceID, err.FirstDigest, err.NextDigest,
	)
}

func NewChecker() *Checker {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext:           safeDialContext(dialer, net.DefaultResolver),
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   45 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("source redirect limit exceeded")
			}
			return validateSourceURL(request.URL)
		},
	}
	return &Checker{client: client, now: time.Now}
}

func NewCheckerForTest(client HTTPDoer, now func() time.Time) *Checker {
	return &Checker{client: client, now: now}
}

func (checker *Checker) Check(ctx context.Context, entry Entry) (CheckResult, error) {
	parsed, err := url.Parse(entry.URL)
	if err != nil {
		return CheckResult{}, fmt.Errorf("parse source URL: %w", err)
	}
	if err := validateSourceURL(parsed); err != nil {
		return CheckResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return CheckResult{}, fmt.Errorf("build source request: %w", err)
	}
	request.Header.Set(
		"Accept",
		"text/markdown,application/atom+xml;q=0.9,application/vnd.github+json;q=0.8,"+
			"application/json;q=0.7,text/plain;q=0.6,text/html;q=0.5,*/*;q=0.1",
	)
	request.Header.Set("User-Agent", "gds-source-check/0.1")
	response, err := checker.client.Do(request)
	if err != nil {
		return CheckResult{}, fmt.Errorf("fetch source %s: %w", entry.ID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return CheckResult{}, fmt.Errorf(
			"source %s returned HTTP %d", entry.ID, response.StatusCode,
		)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxSourceResponseBytes+1))
	if err != nil {
		return CheckResult{}, fmt.Errorf("read source %s: %w", entry.ID, err)
	}
	if len(content) > maxSourceResponseBytes {
		return CheckResult{}, fmt.Errorf(
			"source %s exceeds %d bytes", entry.ID, maxSourceResponseBytes,
		)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	// Freshness for HTTP sources is content-digest based, not commit-bound. A
	// remote that serves byte-identical (cached/stale) content passes as
	// "unchanged" within its review window. This is inherent to HTTP sources
	// (no commit OID); the calendar review cadence is the freshness backstop.
	state := "not-proven"
	expected := ""
	if entry.ContentDigest != nil {
		expected = *entry.ContentDigest
		if expected == digest {
			state = "unchanged"
		} else {
			state = "changed-unreviewed"
		}
	}
	return CheckResult{
		ID: entry.ID, URL: entry.URL,
		ObservedAt:   checker.now().UTC().Format(time.RFC3339),
		HTTPStatus:   response.StatusCode,
		ContentType:  response.Header.Get("Content-Type"),
		ETag:         response.Header.Get("ETag"),
		LastModified: response.Header.Get("Last-Modified"),
		Bytes:        len(content), ObservedDigest: digest,
		ExpectedDigest: expected, State: state,
	}, nil
}

// CheckReproducible proves that a source representation is stable enough to
// become a verification baseline. A single successful fetch is insufficient:
// hydration data, counters, and other request-specific HTML can change while
// the governed source facts remain unchanged.
func (checker *Checker) CheckReproducible(ctx context.Context, entry Entry) (CheckResult, error) {
	first, err := checker.Check(ctx, entry)
	if err != nil {
		return CheckResult{}, err
	}
	second, err := checker.Check(ctx, entry)
	if err != nil {
		return CheckResult{}, err
	}
	if first.ObservedDigest != second.ObservedDigest {
		return CheckResult{}, &NonReproducibleError{
			SourceID: entry.ID, FirstDigest: first.ObservedDigest,
			NextDigest: second.ObservedDigest,
		}
	}
	return first, nil
}

func validateSourceURL(value *url.URL) error {
	if value == nil || value.Scheme != "https" || value.Hostname() == "" ||
		value.User != nil || value.Fragment != "" {
		return fmt.Errorf("source URL must be an absolute credential-free HTTPS URL")
	}
	port := value.Port()
	if port != "" && port != "443" {
		return fmt.Errorf("source URL uses unsupported HTTPS port %q", port)
	}
	return nil
}

type resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

func safeDialContext(dialer *net.Dialer, resolver resolver) func(
	context.Context, string, string,
) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse source address: %w", err)
		}
		if port == "" {
			port = strconv.Itoa(443)
		}
		addresses, err := resolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("resolve source host %s: %w", host, err)
		}
		for _, address := range addresses {
			if unsafeSourceIP(address.IP) {
				return nil, fmt.Errorf("source host %s resolves to a non-public address", host)
			}
		}
		return dialer.DialContext(
			ctx, network, net.JoinHostPort(addresses[0].IP.String(), port),
		)
	}
}

func unsafeSourceIP(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		carrierGradeNAT.Contains(ip)
}
