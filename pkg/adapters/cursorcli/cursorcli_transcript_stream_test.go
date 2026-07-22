package cursorcli

import (
	"reflect"
	"testing"

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
	chunks := cursorMessagesToChunks(msgs, map[string]bool{})

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
	first := cursorMessagesToChunks([]llmtypes.MessageContent{
		cursorAIText("hi"),
		cursorAITool("read_file", "dup1"),
	}, seen)
	if len(first) != 2 {
		t.Fatalf("first pass: got %d chunks, want 2; %+v", len(first), first)
	}
	// Same tool id again -> not re-emitted.
	second := cursorMessagesToChunks([]llmtypes.MessageContent{
		cursorAITool("read_file", "dup1"),
		cursorAIText("more"),
	}, seen)
	if len(second) != 1 || second[0].Type != llmtypes.StreamChunkTypeContent || second[0].Content != "more" {
		t.Fatalf("second pass should drop the duplicate tool and keep only the new text; got %+v", second)
	}
}
