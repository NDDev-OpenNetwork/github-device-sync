package github

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ErrorKind string

const (
	ErrorAuthentication         ErrorKind = "authentication"
	ErrorAuthorization          ErrorKind = "authorization"
	ErrorPermissionContract     ErrorKind = "permission-contract"
	ErrorNotFoundOrInaccessible ErrorKind = "not-found-or-inaccessible"
	ErrorRateLimited            ErrorKind = "rate-limited"
	ErrorConflict               ErrorKind = "conflict"
	ErrorValidation             ErrorKind = "validation"
	ErrorTransient              ErrorKind = "transient"
	ErrorResponse               ErrorKind = "response-invalid"
)

type APIError struct {
	Kind       ErrorKind
	StatusCode int
	RequestID  string
	RetryAfter time.Duration
	Cause      error
}

func (apiError *APIError) Error() string {
	message := fmt.Sprintf("GitHub API request failed with status %d (%s)", apiError.StatusCode, apiError.Kind)
	if apiError.RequestID != "" {
		message += ", request id " + apiError.RequestID
	}
	if apiError.Cause != nil {
		message += ": " + apiError.Cause.Error()
	}
	return message
}

func (apiError *APIError) Unwrap() error { return apiError.Cause }

// SafeOperationFailureEvidence exposes only bounded provider metadata that is
// already safe for logs and journals. In particular it excludes the response
// body, token, request URL, repository identity, and wrapped cause text.
func (apiError *APIError) SafeOperationFailureEvidence() map[string]any {
	evidence := map[string]any{
		"provider":    "github",
		"kind":        string(apiError.Kind),
		"status_code": apiError.StatusCode,
	}
	if apiError.RequestID != "" {
		evidence["request_id"] = apiError.RequestID
	}
	if apiError.RetryAfter > 0 {
		evidence["retry_after_ms"] = apiError.RetryAfter.Milliseconds()
	}
	var detail interface{ SafeProviderFailureCode() string }
	if errors.As(apiError.Cause, &detail) {
		evidence["detail_code"] = detail.SafeProviderFailureCode()
	}
	return evidence
}

type responseContractError struct{ code string }

func (responseContractError) Error() string { return "GitHub provider response is invalid" }

func (failure responseContractError) SafeProviderFailureCode() string { return failure.code }

func classifyStatus(status int, body []byte, meta ResponseMeta) ErrorKind {
	switch {
	case status == 401:
		return ErrorAuthentication
	case status == 403 && (meta.RetryAfter > 0 || (meta.Rate.Known && meta.Rate.Remaining == 0) || isSecondaryRateLimitBody(body)):
		return ErrorRateLimited
	case status == 403:
		return ErrorAuthorization
	case status == 404:
		return ErrorNotFoundOrInaccessible
	case status == 409:
		return ErrorConflict
	case status == 422:
		return ErrorValidation
	case status == 429:
		return ErrorRateLimited
	case status >= 500:
		return ErrorTransient
	default:
		return ErrorResponse
	}
}

// isSecondaryRateLimitBody reports whether the response body indicates a
// GitHub "secondary rate limit" (an abuse-detection throttle), which is
// returned as a 403 without the standard remaining-quota metadata.
func isSecondaryRateLimitBody(body []byte) bool {
	return bytes.Contains(bytes.ToLower(body), []byte("secondary rate limit")) ||
		strings.Contains(strings.ToLower(string(body)), "secondary rate limit")
}
