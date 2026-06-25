package picli

import (
	"context"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var _ llmtypes.StatusLineProvider = (*PiCLIAdapter)(nil)

func TestStreamPiStatusLineEmitsEstimatedChunk(t *testing.T) {
	session := &piInteractiveSession{
		ownerSessionID:    "owner-1",
		tmuxSessionName:   "tmux-pi-1",
		workingDir:        t.TempDir(),
		persistent:        true,
		modelID:           "google/gemini-3.5-flash",
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
	if got := sl.Metadata["pi_token_usage_source"]; got != "estimated" {
		t.Fatalf("pi_token_usage_source = %#v, want estimated", got)
	}
}
