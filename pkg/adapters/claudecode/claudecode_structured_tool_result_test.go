package claudecode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-872-66: separate SDK assistant messages ran together, especially after
// colons; adding whitespace to every delta would corrupt split words and code.
func TestClaudeStructuredTextPreservesMessageBoundaries(t *testing.T) {
	frames := []string{
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Checking dep"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"endencies:"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Checking dependencies:"},{"type":"tool_use","id":"read-1","name":"Read"}]}}`,
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		// A tool-only message must not consume or duplicate the separator.
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"read-2","name":"Read"}]}}`,
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Ready.\n\n| Name | Value |\n| --- | --- |\n| A | 1 |"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Ready.\n\n| Name | Value |\n| --- | --- |\n| A | 1 |"}]}}`,
	}
	want := "Checking dependencies:\n\nReady.\n\n| Name | Value |\n| --- | --- |\n| A | 1 |"
	var text claudeStructuredText
	var live strings.Builder
	for _, frame := range frames {
		var event claudeStreamEvent
		if err := json.Unmarshal([]byte(frame), &event); err != nil {
			t.Fatal(err)
		}
		text.observe(event, func(chunk llmtypes.StreamChunk) {
			if chunk.Metadata[llmtypes.ContentDeltaMetadataKey] != true {
				t.Fatal("SDK delta lost its marker")
			}
			live.WriteString(chunk.Content)
		})
	}
	if live.String() != want || text.completed.String() != want {
		t.Fatalf("message boundaries differ: live=%q saved=%q", live.String(), text.completed.String())
	}
	// Structured transport supplies completed blocks instead of token deltas.
	var blocks claudeStructuredText
	var emitted []string
	for _, body := range []string{"Checking dependencies:", "Ready."} {
		blocks.observe(claudeStreamEvent{Type: "assistant", Message: &claudeStreamMessage{Content: []claudeStreamContentBlock{{Type: "text", Text: body}}}}, func(chunk llmtypes.StreamChunk) {
			if chunk.Metadata[llmtypes.ContentDeltaMetadataKey] == true {
				t.Fatal("complete block mislabeled as a delta")
			}
			emitted = append(emitted, chunk.Content)
		})
	}
	if strings.Join(emitted, "\n\n") != blocks.completed.String() || blocks.completed.String() != "Checking dependencies:\n\nReady." {
		t.Fatalf("complete blocks changed: %q", blocks.completed.String())
	}
}

func TestClaudeToolResultTextPreservesNonTextBlocks(t *testing.T) {
	raw := json.RawMessage(`[{"type":"tool_reference","tool_name":"Monitor"}]`)
	if got := claudeToolResultText(raw); got != string(raw) {
		t.Fatalf("result = %q, want structured tool reference %q", got, raw)
	}
}

// PLAT-872-47: the earlier streamed answer was overwritten by the result
// answering a steer or a background-tool completion in the same host turn.
func TestClaudeSDKContinuedResultsKeepEarlierAnswer(t *testing.T) {
	frames := []string{
		`{"type":"result","result":"I changed direction.","sdk_continues":true}`,
		// A segment that ended on a steered message: no answer to show.
		`{"type":"result","result":"[ede_diagnostic] result_type=user last_content_type=n/a stop_reason=null","sdk_continues":true}`,
		`{"type":"result","result":"The background tool completed."}`,
	}
	var text string
	var continued bool
	for _, frame := range frames {
		var event claudeStreamEvent
		if err := json.Unmarshal([]byte(frame), &event); err != nil {
			t.Fatal(err)
		}
		text = claudeContinuedResultText(text, event, continued)
		continued = event.SDKContinues
	}
	if text != "I changed direction.\n\nThe background tool completed." {
		t.Fatalf("lost native answer: %q", text)
	}
}
