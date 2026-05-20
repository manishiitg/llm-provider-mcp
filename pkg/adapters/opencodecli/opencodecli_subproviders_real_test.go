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

// TestOpenCodeSubProviderRealGLMCodingPlanEndToEnd is the GLM
// counterpart to TestOpenCodeSubProviderRealKimiEndToEnd, targeting the
// **Z.AI coding subscription** tile (zai-coding-plan) rather than the
// BigModel commerce tile. Mirrors the kimi-for-coding pattern.
//
// Why this test exists (and why the regular opencode-cli-glm tile does
// not have an equivalent): the user-facing key may be entitled on
// either platform. Keys minted at z.ai/manage-apikey route through
// api.z.ai/api/coding/paas/v4 (this tile); keys minted at
// open.bigmodel.cn route through the BigModel commerce endpoint
// (opencode-cli-glm tile). A key entitled only on Z.AI will return
// HTTP 429 code 1113 ("insufficient balance") on the BigModel path,
// which opencode silently retries — manifesting as a "hang" in tests.
//
// Skipped unless RUN_OPENCODE_REAL_E2E=1 and ZHIPU_API_KEY (or
// ZAI_API_KEY as a convenience alias) are both set. opencode reads
// the env var literally named ZHIPU_API_KEY for both BigModel and
// Z.AI coding plan tiles.
func TestOpenCodeSubProviderRealGLMCodingPlanEndToEnd(t *testing.T) {
	if os.Getenv("RUN_OPENCODE_REAL_E2E") == "" {
		t.Skip("set RUN_OPENCODE_REAL_E2E=1 to run real OpenCode sub-provider e2e tests")
	}
	apiKey := strings.TrimSpace(os.Getenv("ZHIPU_API_KEY"))
	if apiKey == "" {
		// Accept ZAI_API_KEY as a UX alias — z.ai dashboard surfaces the
		// key under that label even though opencode reads ZHIPU_API_KEY.
		apiKey = strings.TrimSpace(os.Getenv("ZAI_API_KEY"))
	}
	if apiKey == "" {
		t.Skip("set ZHIPU_API_KEY (or ZAI_API_KEY) to run the GLM coding-plan sub-provider e2e test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode binary not found: %v", err)
	}

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	token := "GLM_REAL_OK_" + opencodeRandomHex(4)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly this token and nothing else: " + token},
			},
		},
	},
		WithOpenCodeSubProvider("opencode-cli-glm-coding-plan"),
		WithOpenCodeSubProviderAPIKey("ZHIPU_API_KEY", apiKey),
		// tier=medium resolves to zai-coding-plan/glm-4.7 through the
		// sub-provider tier shortcut.
		WithOpenCodeModel("medium"),
	)
	if err != nil {
		t.Fatalf("GLM sub-provider GenerateContent error = %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("GLM sub-provider returned no choices")
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("GLM coding-plan sub-provider content = %q, want token %q", content, token)
	}

	if resp.Choices[0].GenerationInfo == nil {
		t.Fatal("response missing GenerationInfo")
	}
	if got, _ := resp.Choices[0].GenerationInfo.Additional["provider"].(string); got != "opencode-cli" {
		t.Errorf("GenerationInfo.provider = %q, want opencode-cli", got)
	}
}
