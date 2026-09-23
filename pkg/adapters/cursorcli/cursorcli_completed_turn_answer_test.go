package cursorcli

import (
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCursorTurnTrailState(t *testing.T) {
	user := llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "q")
	narrationCall := llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{
		llmtypes.TextContent{Text: "I'll run it."}, llmtypes.ToolCall{ID: "t1"},
	}}
	toolResult := llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeTool}
	final := llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: " denied: X "}}}

	for name, tc := range map[string]struct {
		trail      []llmtypes.MessageContent
		wantState  cursorTrailState
		wantAnswer string
	}{
		"empty":          {nil, cursorTrailNotTaken, ""},
		"user only":      {[]llmtypes.MessageContent{user}, cursorTrailTaken, ""},
		"tool running":   {[]llmtypes.MessageContent{user, narrationCall}, cursorTrailPendingTool, ""},
		"awaiting reply": {[]llmtypes.MessageContent{user, narrationCall, toolResult}, cursorTrailAwaitingReply, ""},
		"finished":       {[]llmtypes.MessageContent{user, narrationCall, toolResult, final}, cursorTrailFinished, "denied: X"},
	} {
		answer, state := cursorTurnTrailState(tc.trail)
		if state != tc.wantState || answer != tc.wantAnswer {
			t.Errorf("%s: got (%q, %d) want (%q, %d)", name, answer, state, tc.wantAnswer, tc.wantState)
		}
	}
}
