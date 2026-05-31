package agycli

import (
	"context"
	"os"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Compile-time conformance: the agy adapter must satisfy the statusline contract
// interface so the cross-provider e2e harness can drive it.
var _ llmtypes.StatusLineProvider = (*AgyCLIAdapter)(nil)

// TestStreamAgyStatusLineEmitsFullChunk is a producer-side e2e: it writes a real
// agy statusline JSON file to the path the streamer reads, runs streamAgyStatusLine,
// and asserts the emitted StreamChunk carries the full telemetry — every token
// field, the canonical "agy-cli" provider name, the owning tmux session in
// metadata, and NO placeholder "agy-cli" model (which would render a duplicate
// "agy-cli · agy-cli" label).
func TestStreamAgyStatusLineEmitsFullChunk(t *testing.T) {
	sessionName := "agy-statusline-e2e-" + agyRandomHex(6)
	path := agyStatuslinePath(sessionName)

	// Mirror the shape agy writes: current-usage tokens plus cumulative totals.
	payload := `{
		"total_input_tokens": 63000,
		"total_output_tokens": 900,
		"context_window": {
			"current_usage": {
				"input_tokens": 15000,
				"output_tokens": 273,
				"cache_creation_input_tokens": 1200,
				"cache_read_input_tokens": 48000
			}
		}
	}`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write statusline file: %v", err)
	}
	defer os.Remove(path)

	streamChan := make(chan llmtypes.StreamChunk, 1)
	if ok := streamAgyStatusLine(context.Background(), sessionName, streamChan); !ok {
		t.Fatal("streamAgyStatusLine returned false; expected it to emit a chunk")
	}

	select {
	case chunk := <-streamChan:
		if chunk.Type != llmtypes.StreamChunkTypeStatusLine {
			t.Fatalf("chunk.Type = %q, want status_line", chunk.Type)
		}
		sl := chunk.StatusLine
		if sl == nil {
			t.Fatal("chunk.StatusLine is nil")
		}
		// Shared cross-provider contract: canonical provider name, no placeholder
		// model duplication, real tokens, valid tmux tag.
		testcontracts.AssertStatusLineContract(t, sl, "agy-cli", true)
		if sl.Model != "" {
			t.Errorf("Model = %q, want empty (no placeholder that duplicates the provider)", sl.Model)
		}
		if sl.InputTokens != 15000 {
			t.Errorf("InputTokens = %d, want 15000", sl.InputTokens)
		}
		if sl.OutputTokens != 273 {
			t.Errorf("OutputTokens = %d, want 273", sl.OutputTokens)
		}
		if sl.CacheCreationInputTokens != 1200 {
			t.Errorf("CacheCreationInputTokens = %d, want 1200", sl.CacheCreationInputTokens)
		}
		if sl.CacheReadInputTokens != 48000 {
			t.Errorf("CacheReadInputTokens = %d, want 48000", sl.CacheReadInputTokens)
		}
		if sl.TotalInputTokens != 63000 {
			t.Errorf("TotalInputTokens = %d, want 63000", sl.TotalInputTokens)
		}
		if sl.TotalOutputTokens != 900 {
			t.Errorf("TotalOutputTokens = %d, want 900", sl.TotalOutputTokens)
		}
		if got, _ := sl.Metadata["tmux_session"].(string); got != sessionName {
			t.Errorf("Metadata[tmux_session] = %q, want %q", got, sessionName)
		}
	default:
		t.Fatal("no chunk emitted on streamChan")
	}
}
