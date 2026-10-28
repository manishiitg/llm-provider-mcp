package claudecode

import (
	"encoding/json"
	"testing"
)

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
