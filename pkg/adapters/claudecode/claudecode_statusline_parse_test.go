package claudecode

import "testing"

// TestParseClaudeStatusLineJSON_NativeModelAndCost covers Claude Code's native
// statusLine input shape: a nested model object and a cost object. Both must be
// surfaced (real display name + cost), overriding the passed-in default model.
func TestParseClaudeStatusLineJSON_NativeModelAndCost(t *testing.T) {
	raw := []byte(`{
		"model": {"id": "claude-opus-4-8", "display_name": "Opus 4.8"},
		"cost": {"total_cost_usd": 0.0421},
		"input_tokens": 15000,
		"output_tokens": 273,
		"cache_read_input_tokens": 48000
	}`)

	status, err := parseClaudeStatusLineJSON(raw, "fallback-model")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if status.Model != "Opus 4.8" {
		t.Errorf("Model = %q, want 'Opus 4.8' (display_name overrides default)", status.Model)
	}
	if status.CostUSD != 0.0421 {
		t.Errorf("CostUSD = %v, want 0.0421", status.CostUSD)
	}
	if status.InputTokens != 15000 || status.OutputTokens != 273 || status.CacheReadInputTokens != 48000 {
		t.Errorf("tokens not parsed: %+v", status)
	}
	if status.Provider != "claudecode" {
		t.Errorf("Provider = %q, want claudecode", status.Provider)
	}
}

// TestParseClaudeStatusLineJSON_PlaceholderModelStripped ensures the
// "claude-code" placeholder (equal to the provider name) is dropped so the label
// doesn't render a duplicate "claudecode · claude-code".
func TestParseClaudeStatusLineJSON_PlaceholderModelStripped(t *testing.T) {
	// No model in the JSON; default is the placeholder id.
	status, err := parseClaudeStatusLineJSON([]byte(`{"input_tokens": 10}`), "claude-code")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if status.Model != "" {
		t.Errorf("Model = %q, want empty (placeholder stripped)", status.Model)
	}
}

// TestParseClaudeStatusLineJSON_CustomTokenFormat ensures the older token-only
// format (no model/cost objects) still parses with no regression.
func TestParseClaudeStatusLineJSON_CustomTokenFormat(t *testing.T) {
	raw := []byte(`{"inputTokens": 100, "outputTokens": 50, "cacheReadInputTokens": 200}`)
	status, err := parseClaudeStatusLineJSON(raw, "")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if status.InputTokens != 100 || status.OutputTokens != 50 || status.CacheReadInputTokens != 200 {
		t.Errorf("camelCase tokens not parsed: %+v", status)
	}
	if status.Model != "" {
		t.Errorf("Model = %q, want empty (no model in payload, no default)", status.Model)
	}
	if status.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 (no cost in payload)", status.CostUSD)
	}
}

// TestParseClaudeStatusLineJSON_ModelAsString covers the variant where "model"
// is a bare string rather than an object.
func TestParseClaudeStatusLineJSON_ModelAsString(t *testing.T) {
	status, err := parseClaudeStatusLineJSON([]byte(`{"model": "claude-sonnet-4-6"}`), "fallback")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if status.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want claude-sonnet-4-6", status.Model)
	}
}
