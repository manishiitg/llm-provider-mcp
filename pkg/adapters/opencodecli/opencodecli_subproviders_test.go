package opencodecli

import (
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestOpenCodeSubProvidersAreWellFormed(t *testing.T) {
	sps := OpenCodeSubProviders()
	if len(sps) == 0 {
		t.Fatal("OpenCodeSubProviders() returned empty list; at least Kimi/DeepSeek/Qwen/MiniMax/GLM/Free are expected")
	}
	wantIDs := []string{
		"opencode-cli-kimi",
		"opencode-cli-deepseek",
		"opencode-cli-qwen",
		"opencode-cli-minimax",
		"opencode-cli-glm",
		"opencode-cli-free",
	}
	gotIDs := make(map[string]bool, len(sps))
	for _, sp := range sps {
		gotIDs[sp.ID] = true
	}
	for _, want := range wantIDs {
		if !gotIDs[want] {
			t.Errorf("sub-provider %s missing from registry", want)
		}
	}

	for _, sp := range sps {
		if sp.OpenCodeProviderID == "" {
			t.Errorf("%s: empty OpenCodeProviderID", sp.ID)
		}
		if sp.DisplayName == "" {
			t.Errorf("%s: empty DisplayName", sp.ID)
		}
		if len(sp.Models) == 0 {
			t.Errorf("%s: zero models registered", sp.ID)
		}
		if sp.RequiresAPIKey && sp.APIKeyEnvVar == "" {
			t.Errorf("%s: RequiresAPIKey=true but APIKeyEnvVar is empty", sp.ID)
		}
		if !sp.RequiresAPIKey && sp.APIKeyEnvVar != "" {
			t.Errorf("%s: RequiresAPIKey=false but APIKeyEnvVar=%q (free providers should not advertise a key)", sp.ID, sp.APIKeyEnvVar)
		}
		// DefaultModelID should resolve to one of the registered models.
		foundDefault := false
		for _, m := range sp.Models {
			if m.ID == sp.DefaultModelID {
				foundDefault = true
				break
			}
		}
		if !foundDefault && sp.DefaultModelID != "" {
			t.Errorf("%s: DefaultModelID %q does not match any registered model", sp.ID, sp.DefaultModelID)
		}
	}
}

func TestResolveOpenCodeSubProviderModelID(t *testing.T) {
	kimi, ok := FindOpenCodeSubProvider("opencode-cli-kimi")
	if !ok {
		t.Fatal("kimi sub-provider missing")
	}
	free, ok := FindOpenCodeSubProvider("opencode-cli-free")
	if !ok {
		t.Fatal("free sub-provider missing")
	}

	cases := []struct {
		name string
		sp   OpenCodeSubProvider
		in   string
		want string
	}{
		{"kimi default", kimi, "", "kimi-for-coding/kimi-k2-thinking"},
		{"kimi auto", kimi, "auto", "kimi-for-coding/kimi-k2-thinking"},
		{"kimi manifest id", kimi, "opencode-cli-kimi", "kimi-for-coding/kimi-k2-thinking"},
		{"kimi tier high", kimi, "high", "kimi-for-coding/kimi-k2-thinking"},
		{"kimi tier low", kimi, "low", "kimi-for-coding/k2p5"},
		{"kimi bare", kimi, "k2p6", "kimi-for-coding/k2p6"},
		{"kimi prefixed pass-through", kimi, "kimi-for-coding/kimi-k2-thinking", "kimi-for-coding/kimi-k2-thinking"},
		{"kimi cross-provider pass-through", kimi, "openai/gpt-5.1", "openai/gpt-5.1"},
		{"free default", free, "", "opencode/deepseek-v4-flash-free"},
		{"free tier high (missing)", free, "high", "opencode/deepseek-v4-flash-free"}, // no "high" shortcut → default
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOpenCodeSubProviderModelID(tc.sp, tc.in)
			if got != tc.want {
				t.Fatalf("resolveOpenCodeSubProviderModelID(%s, %q) = %q, want %q", tc.sp.ID, tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildOpenCodeEnvForCallInjectsOnlyActiveSubProviderKey(t *testing.T) {
	kimi, _ := FindOpenCodeSubProvider("opencode-cli-kimi")
	opts := &llmtypes.CallOptions{}
	WithOpenCodeSubProviderAPIKeys(map[string]string{
		"KIMI_API_KEY":     "sk-kimi-test",
		"DEEPSEEK_API_KEY": "sk-deepseek-test",
		"DASHSCOPE_API_KEY": "sk-qwen-test",
	})(opts)

	env := buildOpenCodeEnvForCall("opencode-shared-key", &kimi, opts)
	if !containsEnv(env, "OPENCODE_API_KEY=opencode-shared-key") {
		t.Errorf("env missing OPENCODE_API_KEY entry; got %v", filterRelevantEnv(env))
	}
	if !containsEnv(env, "KIMI_API_KEY=sk-kimi-test") {
		t.Errorf("env missing KIMI_API_KEY for active Kimi sub-provider; got %v", filterRelevantEnv(env))
	}
	if containsEnv(env, "DEEPSEEK_API_KEY=sk-deepseek-test") {
		t.Errorf("env leaked DEEPSEEK_API_KEY into a Kimi-scoped call: %v", filterRelevantEnv(env))
	}
	if containsEnv(env, "DASHSCOPE_API_KEY=sk-qwen-test") {
		t.Errorf("env leaked DASHSCOPE_API_KEY into a Kimi-scoped call: %v", filterRelevantEnv(env))
	}
}

func TestBuildOpenCodeEnvForCallSkipsKeyForFreeSubProvider(t *testing.T) {
	free, _ := FindOpenCodeSubProvider("opencode-cli-free")
	opts := &llmtypes.CallOptions{}
	WithOpenCodeSubProviderAPIKey("KIMI_API_KEY", "sk-kimi-test")(opts)

	env := buildOpenCodeEnvForCall("opencode-shared", &free, opts)
	for _, e := range env {
		if strings.HasPrefix(e, "KIMI_API_KEY=") {
			t.Errorf("free sub-provider env carries an unrelated KIMI_API_KEY: %s", e)
		}
	}
}

func TestBuildOpenCodeEnvForCallWithNoSubProviderIsBackwardCompatible(t *testing.T) {
	env := buildOpenCodeEnvForCall("legacy-shared-key", nil, nil)
	if !containsEnv(env, "OPENCODE_API_KEY=legacy-shared-key") {
		t.Errorf("legacy env missing OPENCODE_API_KEY")
	}
	// Should not export any per-sub-provider key when none is active.
	for _, e := range env {
		if strings.HasPrefix(e, "KIMI_API_KEY=") || strings.HasPrefix(e, "DEEPSEEK_API_KEY=") ||
			strings.HasPrefix(e, "DASHSCOPE_API_KEY=") || strings.HasPrefix(e, "MINIMAX_API_KEY=") ||
			strings.HasPrefix(e, "ZHIPU_API_KEY=") {
			t.Errorf("legacy env leaked per-sub-provider key: %s", e)
		}
	}
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func filterRelevantEnv(env []string) []string {
	out := []string{}
	prefixes := []string{"OPENCODE_API_KEY=", "KIMI_API_KEY=", "DEEPSEEK_API_KEY=", "DASHSCOPE_API_KEY=", "MINIMAX_API_KEY=", "ZHIPU_API_KEY="}
	for _, e := range env {
		for _, p := range prefixes {
			if strings.HasPrefix(e, p) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}
