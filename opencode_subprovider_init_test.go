package llmproviders

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestInitializeLLMOpenCodeSubProviderRequiresAPIKey verifies that
// InitializeLLM rejects opencode-cli-kimi calls when no Kimi key is
// reachable. No CLI invocation happens — this exercises only the routing
// layer.
func TestInitializeLLMOpenCodeSubProviderRequiresAPIKey(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "")
	cfg := Config{
		Provider: ProviderOpenCodeCLIKimi,
		ModelID:  "auto",
	}
	_, err := InitializeLLM(cfg)
	if err == nil {
		t.Fatal("expected InitializeLLM to fail without a Kimi key; got nil error")
	}
	if !strings.Contains(err.Error(), "KIMI_API_KEY") {
		t.Errorf("error should mention KIMI_API_KEY; got %q", err.Error())
	}
}

// TestInitializeLLMOpenCodeFreeTileWorksWithoutKey verifies that the
// free tile constructs successfully even without a sub-provider key.
func TestInitializeLLMOpenCodeFreeTileWorksWithoutKey(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "")
	cfg := Config{
		Provider: ProviderOpenCodeCLIFree,
		ModelID:  "auto",
	}
	model, err := InitializeLLM(cfg)
	if err != nil {
		t.Fatalf("expected free tile to construct without keys; got %v", err)
	}
	if model == nil {
		t.Fatal("InitializeLLM returned a nil model")
	}
}

// TestInitializeLLMOpenCodeSubProviderReadsFromAPIKeysMap proves the
// sub-key map on Config.APIKeys is the canonical credential source so
// the server (which loads keys from its workspace-encrypted store) can
// pass them straight through without env-var manipulation.
func TestInitializeLLMOpenCodeSubProviderReadsFromAPIKeysMap(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "")
	cfg := Config{
		Provider: ProviderOpenCodeCLIKimi,
		ModelID:  "auto",
		APIKeys: &ProviderAPIKeys{
			OpenCodeCLISubKeys: map[string]string{
				"KIMI_API_KEY": "sk-kimi-fromapikeysmap",
			},
		},
	}
	model, err := InitializeLLM(cfg)
	if err != nil {
		t.Fatalf("expected InitializeLLM to accept Kimi key from APIKeys map; got %v", err)
	}
	if model == nil {
		t.Fatal("InitializeLLM returned a nil model")
	}
}

// TestInitializeLLMOpenCodeKimiRealCall runs an end-to-end call through
// InitializeLLM with provider=opencode-cli-kimi and a real KIMI_API_KEY,
// proving the chat-time routing path produces a working Kimi adapter
// without any per-call sub-provider options.
//
// Skipped unless RUN_OPENCODE_REAL_E2E=1 and KIMI_API_KEY are set.
func TestInitializeLLMOpenCodeKimiRealCall(t *testing.T) {
	if os.Getenv("RUN_OPENCODE_REAL_E2E") == "" {
		t.Skip("set RUN_OPENCODE_REAL_E2E=1 to run real OpenCode sub-provider e2e tests")
	}
	apiKey := strings.TrimSpace(os.Getenv("KIMI_API_KEY"))
	if apiKey == "" {
		t.Skip("set KIMI_API_KEY to run the Kimi InitializeLLM e2e test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode binary not found: %v", err)
	}

	cfg := Config{
		Provider: ProviderOpenCodeCLIKimi,
		ModelID:  "kimi-k2-thinking",
		APIKeys: &ProviderAPIKeys{
			OpenCodeCLISubKeys: map[string]string{
				"KIMI_API_KEY": apiKey,
			},
		},
	}
	model, err := InitializeLLM(cfg)
	if err != nil {
		t.Fatalf("InitializeLLM error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	token := "INIT_LLM_KIMI_OK_" + randomHexForTest(4)
	resp, err := model.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly this token and nothing else: " + token},
			},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	if !strings.Contains(strings.TrimSpace(resp.Choices[0].Content), token) {
		t.Fatalf("content = %q, want token %q", resp.Choices[0].Content, token)
	}
}

func randomHexForTest(n int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, n*2)
	for i := range out {
		out[i] = hex[time.Now().UnixNano()%16]
	}
	return string(out)
}
