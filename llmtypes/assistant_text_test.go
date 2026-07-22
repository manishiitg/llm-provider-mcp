package llmtypes

import (
	"reflect"
	"strings"
	"testing"
)

// A realistic turn: narrate -> tool -> (result) -> narrate -> tool -> final,
// expressed once as transcript messages (non-streaming source) and once as the
// stream chunks the tailer emits for the SAME turn (with terminal snapshots
// mixed in). The two must agree on the user-facing message.
func sampleTurnMessages() []MessageContent {
	return []MessageContent{
		{Role: ChatMessageTypeAI, Parts: []ContentPart{TextContent{Text: "I'll search for the code word."}}},
		{Role: ChatMessageTypeAI, Parts: []ContentPart{ToolCall{ID: "c1", Type: "function", FunctionCall: &FunctionCall{Name: "web_search"}}}},
		{Role: ChatMessageTypeTool, Parts: []ContentPart{ToolCallResponse{ToolCallID: "c1", Content: "the code word is ZEBRA_123"}}},
		{Role: ChatMessageTypeAI, Parts: []ContentPart{TextContent{Text: "Now I'll save it to result.txt."}}},
		{Role: ChatMessageTypeAI, Parts: []ContentPart{ToolCall{ID: "c2", Type: "function", FunctionCall: &FunctionCall{Name: "write_file"}}}},
		{Role: ChatMessageTypeAI, Parts: []ContentPart{TextContent{Text: "ZEBRA_123"}}},
	}
}

func sampleTurnChunks() []StreamChunk {
	return []StreamChunk{
		{Type: StreamChunkTypeTerminal, Content: "\x1b[2J[raw pane frame 1]"},
		{Type: StreamChunkTypeContent, Content: "I'll search for the code word."},
		{Type: StreamChunkTypeToolCallStart, ToolName: "web_search", ToolCallID: "c1"},
		{Type: StreamChunkTypeTerminal, Content: "[raw pane frame 2]"},
		{Type: StreamChunkTypeContent, Content: "Now I'll save it to result.txt."},
		{Type: StreamChunkTypeToolCallStart, ToolName: "write_file", ToolCallID: "c2"},
		{Type: StreamChunkTypeContent, Content: "ZEBRA_123"},
	}
}

// Mode 1: show tmux directly to the user — the raw terminal frames are available
// and separable from the assistant text.
func TestUserMessageMode1_RawTmux(t *testing.T) {
	raw := StreamTerminalText(sampleTurnChunks())
	if !strings.Contains(raw, "[raw pane frame 1]") || !strings.Contains(raw, "[raw pane frame 2]") {
		t.Fatalf("raw tmux view missing pane frames: %q", raw)
	}
	// It must be the terminal frames, not the assistant text.
	if strings.Contains(raw, "I'll search") || strings.Contains(raw, "ZEBRA_123") {
		t.Fatalf("raw tmux view leaked assistant text: %q", raw)
	}
}

// Mode 2: non-streaming full message = narration + final answer, TOOLS REMOVED,
// narration separable so a UI can highlight it apart from the final answer.
func TestUserMessageMode2_NonStreamingNarrationFinalNoTools(t *testing.T) {
	narration, final := SplitAssistantText(sampleTurnMessages())

	wantNarration := []string{"I'll search for the code word.", "Now I'll save it to result.txt."}
	if !reflect.DeepEqual(narration, wantNarration) {
		t.Fatalf("narration = %q, want %q", narration, wantNarration)
	}
	if final != "ZEBRA_123" {
		t.Fatalf("final = %q, want %q", final, "ZEBRA_123")
	}
	// Tools removed: no tool name and no tool-result text anywhere in the message.
	full := FullAssistantText(sampleTurnMessages())
	for _, banned := range []string{"web_search", "write_file", "the code word is ZEBRA_123", "tool_use", "tool_call"} {
		if strings.Contains(full, banned) {
			t.Fatalf("tools not removed from message: found %q in %q", banned, full)
		}
	}
}

// Mode 3: streaming to the user — the text-only view (tool chunks dropped) is
// the assistant message, and it MUST equal the non-streaming full message
// (mode 2) so both delivery modes show the user the same thing.
func TestUserMessageMode3_StreamingNoTools(t *testing.T) {
	streamed := StreamAssistantText(sampleTurnChunks())

	// No tool activity leaked into the streamed text.
	for _, banned := range []string{"web_search", "write_file", "raw pane frame"} {
		if strings.Contains(streamed, banned) {
			t.Fatalf("streamed text leaked non-text content: found %q in %q", banned, streamed)
		}
	}
	// Consistency: streaming (mode 3) == non-streaming full message (mode 2).
	full := FullAssistantText(sampleTurnMessages())
	if streamed != full {
		t.Fatalf("streaming and non-streaming messages differ:\n stream=%q\n   full=%q", streamed, full)
	}
	if streamed != "I'll search for the code word.\nNow I'll save it to result.txt.\nZEBRA_123" {
		t.Fatalf("unexpected streamed message: %q", streamed)
	}
}
