package cursorcli

import (
	"reflect"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func cursorAIText(s string) llmtypes.MessageContent {
	return llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: s}}}
}
func cursorAITool(name, id string) llmtypes.MessageContent {
	return llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{
		llmtypes.ToolCall{ID: id, Type: "function", FunctionCall: &llmtypes.FunctionCall{Name: name}},
	}}
}
func cursorToolResult(id, name, content string) llmtypes.MessageContent {
	return llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeTool, Parts: []llmtypes.ContentPart{
		llmtypes.ToolCallResponse{ToolCallID: id, Name: name, Content: content},
	}}
}

// TestCursorMessagesToChunksInterleavedOrder proves assistant text and tool_use
// map to Content / ToolCallStart chunks in the correct order.
func TestCursorMessagesToChunksInterleavedOrder(t *testing.T) {
	msgs := []llmtypes.MessageContent{
		cursorAIText("Let me check the first file."),
		cursorAITool("read_file", "c1"),
		cursorAIText("Now the second file."),
		cursorAITool("read_file", "c2"),
		cursorAIText("Done. FINAL."),
	}
	chunks := cursorMessagesToChunks(msgs, map[string]bool{}, map[string]time.Time{})

	var got []string
	for _, c := range chunks {
		switch c.Type {
		case llmtypes.StreamChunkTypeContent:
			got = append(got, "text:"+c.Content)
		case llmtypes.StreamChunkTypeToolCallStart:
			got = append(got, "tool:"+c.ToolName)
		}
	}
	want := []string{
		"text:Let me check the first file.",
		"tool:read_file",
		"text:Now the second file.",
		"tool:read_file",
		"text:Done. FINAL.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interleaved order wrong:\n got=%v\nwant=%v", got, want)
	}
	if chunks[0].Metadata["cursor_cli_stream_source"] != "transcript" {
		t.Fatalf("missing stream-source metadata: %+v", chunks[0].Metadata)
	}
}

// TestCursorMessagesToChunksToolDedup proves a tool call with an already-seen
// call id is not re-emitted (cursor's cumulative root can resurface a blob
// across polls; the seenTool set guards it), while text is emitted as-is.
func TestCursorMessagesToChunksToolDedup(t *testing.T) {
	seen := map[string]bool{}
	started := map[string]time.Time{}
	first := cursorMessagesToChunks([]llmtypes.MessageContent{
		cursorAIText("hi"),
		cursorAITool("read_file", "dup1"),
	}, seen, started)
	if len(first) != 2 {
		t.Fatalf("first pass: got %d chunks, want 2; %+v", len(first), first)
	}
	// Same tool id again -> not re-emitted.
	second := cursorMessagesToChunks([]llmtypes.MessageContent{
		cursorAITool("read_file", "dup1"),
		cursorAIText("more"),
	}, seen, started)
	if len(second) != 1 || second[0].Type != llmtypes.StreamChunkTypeContent || second[0].Content != "more" {
		t.Fatalf("second pass should drop the duplicate tool and keep only the new text; got %+v", second)
	}
}

// TestCursorMessagesToChunksToolResultProducesEnd proves the actual bug fix:
// a tool-result message used to be silently ignored entirely (only
// TextContent and ToolCall were handled), so a tmux-transport cursor tool
// call had a start and nothing else — no result, no duration. It now yields
// a proper ToolCallEnd carrying the real result text.
func TestCursorMessagesToChunksToolResultProducesEnd(t *testing.T) {
	seen := map[string]bool{}
	started := map[string]time.Time{}
	chunks := cursorMessagesToChunks([]llmtypes.MessageContent{
		cursorAITool("read_file", "c1"),
		cursorToolResult("c1", "read_file", "file contents here"),
	}, seen, started)

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (start, end); %+v", len(chunks), chunks)
	}
	if chunks[0].Type != llmtypes.StreamChunkTypeToolCallStart {
		t.Fatalf("chunks[0] = %+v, want ToolCallStart", chunks[0])
	}
	end := chunks[1]
	if end.Type != llmtypes.StreamChunkTypeToolCallEnd || end.ToolCallID != "c1" {
		t.Fatalf("chunks[1] = %+v, want ToolCallEnd for c1", end)
	}
	if end.ToolResult != "file contents here" {
		t.Fatalf("ToolResult = %q, want %q", end.ToolResult, "file contents here")
	}
	if end.ToolDuration <= 0 {
		t.Fatalf("ToolDuration = %v, want > 0 (measured from the start observed just above)", end.ToolDuration)
	}
}

// TestCursorMessagesToChunksToolResultSpansPolls proves duration measurement
// survives across two separate cursorMessagesToChunks calls sharing one
// toolStartedAt map — cursor commits the call and its result as separate
// store.db blobs, which can land in different polls.
func TestCursorMessagesToChunksToolResultSpansPolls(t *testing.T) {
	seen := map[string]bool{}
	started := map[string]time.Time{}

	first := cursorMessagesToChunks([]llmtypes.MessageContent{
		cursorAITool("read_file", "c1"),
	}, seen, started)
	if len(first) != 1 || first[0].Type != llmtypes.StreamChunkTypeToolCallStart {
		t.Fatalf("poll 1: got %+v, want a single start", first)
	}
	if len(started) != 1 {
		t.Fatalf("poll 1: toolStartedAt = %+v, want 1 entry", started)
	}

	time.Sleep(5 * time.Millisecond)
	second := cursorMessagesToChunks([]llmtypes.MessageContent{
		cursorToolResult("c1", "read_file", "done"),
	}, seen, started)
	if len(second) != 1 || second[0].Type != llmtypes.StreamChunkTypeToolCallEnd {
		t.Fatalf("poll 2: got %+v, want a single end", second)
	}
	if second[0].ToolDuration <= 0 {
		t.Fatalf("poll 2: ToolDuration = %v, want > 0", second[0].ToolDuration)
	}
	if len(started) != 0 {
		t.Fatalf("poll 2: toolStartedAt did not drain: %+v", started)
	}
}

func TestCursorMessagesToChunksCarriesToolArguments(t *testing.T) {
	chunks := cursorMessagesToChunks([]llmtypes.MessageContent{{
		Role: llmtypes.ChatMessageTypeAI,
		Parts: []llmtypes.ContentPart{llmtypes.ToolCall{
			ID:   "call-with-args",
			Type: "function",
			FunctionCall: &llmtypes.FunctionCall{
				Name:      "mcp__api-bridge__execute_shell_command",
				Arguments: `{"command":"pwd"}`,
			},
		}},
	}}, map[string]bool{}, map[string]time.Time{})
	if len(chunks) != 1 || chunks[0].Type != llmtypes.StreamChunkTypeToolCallStart {
		t.Fatalf("chunks = %+v, want one tool-call start", chunks)
	}
	if chunks[0].ToolName != "mcp__api-bridge__execute_shell_command" || chunks[0].ToolArgs != `{"command":"pwd"}` {
		t.Fatalf("tool start = %+v, want concrete name and args", chunks[0])
	}
}
