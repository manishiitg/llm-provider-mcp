package llmerrors

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestClassifyKinds(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Kind
	}{
		// Cancellation / timeout
		{"context canceled", context.Canceled, KindCanceled},
		{"wrapped canceled", fmt.Errorf("call failed: %w", context.Canceled), KindCanceled},
		{"deadline exceeded", context.DeadlineExceeded, KindTimeout},
		{"timeout text", errors.New("request timed out after 30s"), KindTimeout},

		// Quota exhaustion (must win over rate-limit patterns in the same message)
		{"gemini daily quota", errors.New("googleapi: Error 429: Quota exceeded for metric generate_requests_per_model_per_day"), KindQuotaExhausted},
		{"openai quota", errors.New("429: You exceeded your current quota, please check your plan and billing details"), KindQuotaExhausted},
		{"claude code usage", errors.New("you've hit your usage limit, resets at 3pm"), KindQuotaExhausted},
		{"resource exhausted", errors.New("rpc error: code = ResourceExhausted desc = RESOURCE_EXHAUSTED"), KindQuotaExhausted},

		// Rate limit
		{"bedrock throttle", errors.New("operation error Bedrock Runtime: ThrottlingException"), KindRateLimit},
		{"status 429", errors.New("API returned unexpected status code: 429"), KindRateLimit},
		{"anthropic overloaded", errors.New("overloaded_error: Anthropic is temporarily overloaded"), KindRateLimit},
		{"rate limit text", errors.New("rate limit exceeded, retry after 30s"), KindRateLimit},

		// Auth
		{"invalid key", errors.New("invalid x-api-key"), KindAuth},
		{"status 401", errors.New("status code: 401 Unauthorized"), KindAuth},
		{"bedrock denied", errors.New("AccessDeniedException: not authorized to invoke this model"), KindAuth},

		// Context too long
		{"anthropic long", errors.New("prompt is too long: 250000 tokens > 200000 maximum"), KindContextTooLong},
		{"openai context", errors.New("context_length_exceeded: this model's maximum context length is 128000"), KindContextTooLong},

		// Model not found
		{"openai 404", errors.New("status code: 404 The model `gpt-9` does not exist or you do not have access to it"), KindModelNotFound},

		// Server / outage
		{"status 500", errors.New("status code: 500 internal server error"), KindServerError},
		{"bad gateway", errors.New("502 Bad Gateway"), KindServerError},
		{"service unavailable", errors.New("Service Unavailable"), KindServerError},

		// Network
		{"conn refused", errors.New("dial tcp 1.2.3.4:443: connection refused"), KindNetwork},
		{"reset", errors.New("read: connection reset by peer"), KindNetwork},
		{"eof", errors.New("unexpected EOF"), KindNetwork},

		// Invalid request (4xx with extractable status, no other match)
		{"bad request", errors.New("status code: 400 malformed JSON body"), KindInvalidRequest},

		// Unknown
		{"tool message", errors.New("tool call rejected by policy"), KindUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := Classify("test-provider", "test-model", tt.err)
			if got := KindOf(wrapped); got != tt.want {
				t.Errorf("KindOf(%q) = %s, want %s", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassifyPreservesChainAndIdempotent(t *testing.T) {
	if Classify("p", "m", nil) != nil {
		t.Fatal("Classify(nil) must be nil")
	}

	base := errors.New("rate limit exceeded")
	once := Classify("anthropic", "claude-x", base)

	// Original error stays reachable (string-based handling keeps working).
	if !errors.Is(once, base) {
		t.Error("classified error must wrap the original")
	}
	var typed *Error
	if !errors.As(once, &typed) {
		t.Fatal("expected *Error in chain")
	}
	if typed.Provider != "anthropic" || typed.Model != "claude-x" {
		t.Errorf("provider/model not carried: %+v", typed)
	}

	// Re-classifying (e.g., a second wrapper layer) must not double-wrap.
	twice := Classify("other", "other", fmt.Errorf("outer: %w", once))
	var inner *Error
	if !errors.As(twice, &inner) || inner.Provider != "anthropic" {
		t.Error("re-classification must pass through the existing *Error")
	}
}

func TestStatusCodeExtraction(t *testing.T) {
	tests := []struct {
		msg  string
		want int
	}{
		{"status code: 429", 429},
		{"StatusCode: 500", 500},
		{"API returned unexpected status code: 503", 503},
		{"status 404", 404},
		{"model claude-500 failed", 0}, // model names must not match
		{"no status here", 0},
	}
	for _, tt := range tests {
		if got := extractStatusCode(tt.msg); got != tt.want {
			t.Errorf("extractStatusCode(%q) = %d, want %d", tt.msg, got, tt.want)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	retryable := []Kind{KindRateLimit, KindServerError, KindNetwork, KindTimeout}
	for _, k := range retryable {
		if !IsRetryable(&Error{Kind: k, Err: errors.New("x")}) {
			t.Errorf("kind %s should be retryable", k)
		}
	}
	terminal := []Kind{KindQuotaExhausted, KindAuth, KindContextTooLong, KindModelNotFound, KindInvalidRequest, KindCanceled, KindUnknown}
	for _, k := range terminal {
		if IsRetryable(&Error{Kind: k, Err: errors.New("x")}) {
			t.Errorf("kind %s should NOT be retryable", k)
		}
	}
	if IsRetryable(errors.New("unclassified")) {
		t.Error("unclassified errors should not be considered retryable")
	}
}
