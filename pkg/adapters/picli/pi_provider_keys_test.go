package picli

import (
	"strings"
	"testing"
)

func TestResolvePiProviderModelPreservesNestedOpenRouterModel(t *testing.T) {
	gotProvider, gotModel := resolvePiProviderModel("openrouter/minimax/minimax-m3-20260531", "")
	if gotProvider != "openrouter" || gotModel != "minimax/minimax-m3-20260531" {
		t.Fatalf("resolvePiProviderModel() = %q/%q, want openrouter/minimax/minimax-m3-20260531", gotProvider, gotModel)
	}
}

func TestPiAPIKeyEnvSupportsProviderSpecificKeys(t *testing.T) {
	tests := []struct {
		provider string
		want     []string
	}{
		{provider: "google", want: []string{"GEMINI_API_KEY=test-key", "GOOGLE_API_KEY=test-key", "PI_API_KEY=test-key"}},
		{provider: "openrouter", want: []string{"OPENROUTER_API_KEY=test-key"}},
		{provider: "zai", want: []string{"ZAI_API_KEY=test-key"}},
		{provider: "zai-coding-cn", want: []string{"ZAI_CODING_CN_API_KEY=test-key"}},
		{provider: "kimi-coding", want: []string{"KIMI_API_KEY=test-key"}},
		{provider: "minimax-cn", want: []string{"MINIMAX_CN_API_KEY=test-key"}},
		{provider: "deepseek", want: []string{"DEEPSEEK_API_KEY=test-key"}},
		{provider: "opencode-go", want: []string{"OPENCODE_API_KEY=test-key"}},
		{provider: "custom-provider", want: []string{"CUSTOM_PROVIDER_API_KEY=test-key"}},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := piAPIKeyEnv(tt.provider, "test-key")
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("piAPIKeyEnv(%q) = %#v, want %#v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestPiRedactArgsCoversProviderSpecificKeys(t *testing.T) {
	got := piRedactArgs([]string{
		"OPENROUTER_API_KEY=openrouter-secret",
		"ZAI_API_KEY=zai-secret",
		"ZAI_CODING_CN_API_KEY=zai-cn-secret",
		"KIMI_API_KEY=kimi-secret",
		"MINIMAX_CN_API_KEY=minimax-secret",
		"DEEPSEEK_API_KEY=deepseek-secret",
		"OPENCODE_API_KEY=opencode-secret",
	})
	for _, secret := range []string{"openrouter-secret", "zai-secret", "zai-cn-secret", "kimi-secret", "minimax-secret", "deepseek-secret", "opencode-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("piRedactArgs leaked %q in %q", secret, got)
		}
	}
}
