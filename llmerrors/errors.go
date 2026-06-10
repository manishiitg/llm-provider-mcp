// Package llmerrors defines typed errors for LLM provider failures so
// consumers can branch on cause (rate limit vs auth vs outage) instead of
// string-matching error text.
//
// ProviderAwareLLM wraps every adapter error via Classify, so any error
// returned from GenerateContent can be inspected with KindOf / errors.As.
// Classification is heuristic (status codes and well-known provider message
// fragments); adapters may also construct *Error directly with exact status
// codes, which Classify passes through untouched.
package llmerrors

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind is the failure category of an LLM call.
type Kind string

const (
	// KindRateLimit is transient throttling (429, overloaded). Retry with
	// backoff or fall back to another model; recovers within seconds/minutes.
	KindRateLimit Kind = "rate_limit"
	// KindQuotaExhausted is a daily/monthly usage limit. Will NOT recover
	// within minutes — skip same-model retries and go straight to fallback.
	KindQuotaExhausted Kind = "quota_exhausted"
	// KindAuth is an invalid/expired credential or permission failure (401/403).
	// Retrying is pointless until configuration changes.
	KindAuth Kind = "auth"
	// KindContextTooLong means the input exceeded the model's context window.
	// Retrying the same payload is pointless; summarize or truncate first.
	KindContextTooLong Kind = "context_too_long"
	// KindModelNotFound means the model ID is unknown/unavailable (404).
	KindModelNotFound Kind = "model_not_found"
	// KindInvalidRequest is a 400-class rejection of the request shape.
	KindInvalidRequest Kind = "invalid_request"
	// KindServerError is a provider-side 5xx/outage. Retryable with backoff.
	KindServerError Kind = "server_error"
	// KindNetwork is a transport failure (refused/reset/dial). Retryable.
	KindNetwork Kind = "network"
	// KindTimeout is a deadline exceeded while waiting on the provider. Retryable.
	KindTimeout Kind = "timeout"
	// KindCanceled is an explicit caller cancellation. Never retry.
	KindCanceled Kind = "canceled"
	// KindUnknown means classification failed; treat conservatively.
	KindUnknown Kind = "unknown"
)

// Error is a classified LLM provider failure.
type Error struct {
	Kind       Kind
	Provider   string
	Model      string
	Status     int           // HTTP status when known, else 0
	RetryAfter time.Duration // provider-suggested wait when known, else 0
	Err        error         // underlying error, always non-nil
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s/%s [%s]: %v", e.Provider, e.Model, e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf extracts the Kind from an error chain, or KindUnknown.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindUnknown
}

// IsRateLimit reports transient throttling (NOT permanent quota exhaustion).
func IsRateLimit(err error) bool { return KindOf(err) == KindRateLimit }

// IsAuth reports credential/permission failures.
func IsAuth(err error) bool { return KindOf(err) == KindAuth }

// IsRetryable reports whether retrying the same model with backoff can help.
func IsRetryable(err error) bool {
	switch KindOf(err) {
	case KindRateLimit, KindServerError, KindNetwork, KindTimeout:
		return true
	default:
		return false
	}
}

// Classify wraps err with its best-effort classification. Returns nil for nil
// input and passes through errors that already carry an *Error.
func Classify(provider, model string, err error) error {
	if err == nil {
		return nil
	}
	var already *Error
	if errors.As(err, &already) {
		return err
	}
	return &Error{
		Kind:     classifyKind(err),
		Provider: provider,
		Model:    model,
		Status:   extractStatusCode(err.Error()),
		Err:      err,
	}
}

var statusCodeRe = regexp.MustCompile(`(?i)status(?:\s*code)?[:\s]+(\d{3})\b`)

// extractStatusCode pulls an HTTP status out of error text when present
// ("status code: 429", "StatusCode: 500", "status 503").
func extractStatusCode(msg string) int {
	m := statusCodeRe.FindStringSubmatch(msg)
	if len(m) != 2 {
		return 0
	}
	code, _ := strconv.Atoi(m[1])
	return code
}

func containsAny(msg string, patterns ...string) bool {
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// classifyKind applies ordered heuristics. Order matters: cancellation first
// (its text can look like anything), then permanent quota (its text often
// also matches rate-limit patterns), then the rest.
func classifyKind(err error) Kind {
	if errors.Is(err, context.Canceled) {
		return KindCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}

	msg := strings.ToLower(err.Error())
	status := extractStatusCode(msg)

	if containsAny(msg, "context canceled", "request canceled", "operation was canceled") {
		return KindCanceled
	}

	// Permanent quota exhaustion — checked before rate limit because these
	// messages frequently also mention 429/quota.
	if containsAny(msg,
		"per_day", "per_month", "generaterequestsperday",
		"resource_exhausted", "quota exceeded for metric", "exceeded your current quota",
		"hit your usage limit", "hit your limit", "usage limit",
		"billing", "insufficient credit", "insufficient_quota",
	) {
		return KindQuotaExhausted
	}

	if status == 429 || containsAny(msg,
		"throttlingexception", "rate limit", "rate_limit", "ratelimit",
		"too many requests", "too many tokens", "throttled",
		"overloaded", "overloaded_error",
	) {
		return KindRateLimit
	}

	if status == 401 || status == 403 || containsAny(msg,
		"invalid api key", "invalid x-api-key", "incorrect api key", "api key not valid",
		"authentication", "unauthorized", "unauthenticated",
		"permission denied", "permission_denied", "forbidden",
		"accessdeniedexception", "credential",
	) {
		return KindAuth
	}

	if containsAny(msg,
		"context window", "context length", "context_length_exceeded",
		"input is too long", "prompt is too long", "too long",
		"maximum context", "max_token", "max tokens exceeded",
	) {
		return KindContextTooLong
	}

	if status == 404 || containsAny(msg,
		"model not found", "model_not_found", "no such model",
		"unknown model", "does not exist or you do not have access",
	) {
		return KindModelNotFound
	}

	if status >= 500 && status < 600 {
		return KindServerError
	}
	if containsAny(msg,
		"internal server error", "internal_error", "bad gateway",
		"service unavailable", "gateway timeout", "server error",
		"unavailable",
	) {
		return KindServerError
	}

	if containsAny(msg,
		"connection refused", "connection reset", "broken pipe",
		"dial tcp", "no such host", "unexpected eof", "eof",
		"connection closed", "connection lost", "use of closed network connection",
	) {
		return KindNetwork
	}

	if containsAny(msg, "deadline exceeded", "timeout", "timed out") {
		return KindTimeout
	}

	if status >= 400 && status < 500 {
		return KindInvalidRequest
	}

	return KindUnknown
}
