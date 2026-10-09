package picli

import "testing"

// The two provider errors Citymall's gateway returned in Pi's pane (wrapped at the terminal width): the 429 token rate limit
// after three retries, and the 400 for tools with a reasoning level. The reason must reach the chat, not "No content generated".
func TestPaneProviderErrorGivesTheStatusAndTheProvidersMessage(t *testing.T) {
	rateLimit := "some earlier output\nError: Retry failed after 3 attempts: 429: {\"message\":\"Your requests to gpt-6-luna for gpt-6-luna in eastus have\nexceeded token rate\nlimit.\",\"type\":\"too_many_requests\",\"param\":null,\"code\":\"rate_limit_exceeded\"}\n\n"
	if got, want := piPaneProviderError(rateLimit), "HTTP 429: Your requests to gpt-6-luna for gpt-6-luna in eastus have exceeded token rate limit."; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	badRequest := "Error: 400: {\"message\":\"Function tools with reasoning_effort are not supported for this model.\",\"type\":\"invalid_request_error\"}\n"
	if got := piPaneProviderError(badRequest); got != "HTTP 400: Function tools with reasoning_effort are not supported for this model." {
		t.Fatalf("got %q", got)
	}
	if got := piPaneProviderError("all good\nHi\n"); got != "" {
		t.Fatalf("a pane without an error must give nothing, got %q", got)
	}
}
