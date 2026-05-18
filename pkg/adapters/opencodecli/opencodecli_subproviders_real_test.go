package opencodecli

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestOpenCodeSubProviderRealKimiEndToEndAdapterDefault exercises the
// adapter constructed with NewOpenCodeCLIAdapterForSubProvider: every
// call inherits the Kimi scope without per-call options.
//
// Skipped unless RUN_OPENCODE_REAL_E2E=1 and KIMI_API_KEY are both set.
func TestOpenCodeSubProviderRealKimiEndToEndAdapterDefault(t *testing.T) {
	if os.Getenv("RUN_OPENCODE_REAL_E2E") == "" {
		t.Skip("set RUN_OPENCODE_REAL_E2E=1 to run real OpenCode sub-provider e2e tests")
	}
	apiKey := strings.TrimSpace(os.Getenv("KIMI_API_KEY"))
	if apiKey == "" {
		t.Skip("set KIMI_API_KEY=sk-kimi-... to run the Kimi sub-provider e2e test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode binary not found: %v", err)
	}

	kimi, ok := FindOpenCodeSubProvider("opencode-cli-kimi")
	if !ok {
		t.Fatal("kimi sub-provider missing")
	}
	adapter := NewOpenCodeCLIAdapterForSubProvider("", "kimi-k2-thinking", kimi, apiKey, &MockLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	token := "KIMI_ADAPTER_DEFAULT_" + opencodeRandomHex(4)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly this token and nothing else: " + token},
			},
		},
	})
	if err != nil {
		t.Fatalf("adapter-default Kimi call error = %v", err)
	}
	if !strings.Contains(strings.TrimSpace(resp.Choices[0].Content), token) {
		t.Fatalf("content = %q, want token %q", resp.Choices[0].Content, token)
	}
}

// TestOpenCodeSubProviderRealKimiEndToEnd exercises the full sub-provider
// path against the real Kimi For Coding API: scope a call to the Kimi tile,
// attach a real KIMI_API_KEY, ask the model to echo a token, and assert the
// token comes back. This is the highest-signal proof that
// `WithOpenCodeSubProvider` + `WithOpenCodeSubProviderAPIKey` correctly
// route to `kimi-for-coding/<model>` and authenticate.
//
// Skipped unless RUN_OPENCODE_REAL_E2E=1 and KIMI_API_KEY are both set.
func TestOpenCodeSubProviderRealKimiEndToEnd(t *testing.T) {
	if os.Getenv("RUN_OPENCODE_REAL_E2E") == "" {
		t.Skip("set RUN_OPENCODE_REAL_E2E=1 to run real OpenCode sub-provider e2e tests")
	}
	apiKey := strings.TrimSpace(os.Getenv("KIMI_API_KEY"))
	if apiKey == "" {
		t.Skip("set KIMI_API_KEY=sk-kimi-... to run the Kimi sub-provider e2e test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode binary not found: %v", err)
	}

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	token := "KIMI_REAL_OK_" + opencodeRandomHex(4)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly this token and nothing else: " + token},
			},
		},
	},
		WithOpenCodeSubProvider("opencode-cli-kimi"),
		WithOpenCodeSubProviderAPIKey("KIMI_API_KEY", apiKey),
		// tier=high resolves to kimi-for-coding/kimi-k2-thinking through the
		// sub-provider tier shortcut, not the legacy global resolver.
		WithOpenCodeModel("high"),
	)
	if err != nil {
		t.Fatalf("Kimi sub-provider GenerateContent error = %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("Kimi sub-provider returned no choices")
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("Kimi sub-provider content = %q, want token %q", content, token)
	}

	// Verify the response identifies the OpenCode session, proving the call
	// actually went through `opencode run` and not some other path.
	if resp.Choices[0].GenerationInfo == nil {
		t.Fatal("response missing GenerationInfo")
	}
	if got, _ := resp.Choices[0].GenerationInfo.Additional["provider"].(string); got != "opencode-cli" {
		t.Errorf("GenerationInfo.provider = %q, want opencode-cli", got)
	}
}
