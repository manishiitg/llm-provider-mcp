package picli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var _ llmtypes.StatusLineProvider = (*PiCLIAdapter)(nil)

func TestStreamPiStatusLineEmitsTokenSourceChunk(t *testing.T) {
	session := &piInteractiveSession{
		ownerSessionID:    "owner-1",
		tmuxSessionName:   "tmux-pi-1",
		workingDir:        t.TempDir(),
		persistent:        true,
		modelID:           "google/gemini-3.5-flash",
		tokenUsageSource:  "transcript-file",
		transcriptPath:    filepath.Join(t.TempDir(), "session.jsonl"),
		costUSD:           0.00123,
		cacheReadTokens:   3,
		inputTokens:       11,
		outputTokens:      7,
		totalInputTokens:  31,
		totalOutputTokens: 17,
	}
	streamChan := make(chan llmtypes.StreamChunk, 1)

	if ok := streamPiStatusLine(context.Background(), session, streamChan); !ok {
		t.Fatal("streamPiStatusLine returned false; expected it to emit a chunk")
	}

	chunk := <-streamChan
	if chunk.Type != llmtypes.StreamChunkTypeStatusLine {
		t.Fatalf("chunk.Type = %q, want %q", chunk.Type, llmtypes.StreamChunkTypeStatusLine)
	}
	sl := chunk.StatusLine
	testcontracts.AssertStatusLineContract(t, sl, "pi-cli", true)
	if sl.Model != "google/gemini-3.5-flash" {
		t.Fatalf("status model = %q, want selected Pi model route", sl.Model)
	}
	if got := sl.Metadata["pi_token_usage_source"]; got != "transcript-file" {
		t.Fatalf("pi_token_usage_source = %#v, want transcript-file", got)
	}
	if got, _ := sl.Metadata["pi_transcript_file"].(string); got == "" {
		t.Fatal("expected pi_transcript_file metadata")
	}
	if sl.CostUSD != 0.00123 {
		t.Fatalf("CostUSD = %v, want 0.00123", sl.CostUSD)
	}
	if sl.CacheReadInputTokens != 3 {
		t.Fatalf("CacheReadInputTokens = %d, want 3", sl.CacheReadInputTokens)
	}
}
