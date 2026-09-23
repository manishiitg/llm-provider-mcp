package cursorcli

import (
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCursorCompletedTurnAnswer(t *testing.T) {
	user := llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "q")
	narration := llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{
		llmtypes.TextContent{Text: "I'll run it."}, llmtypes.ToolCall{ID: "t1"},
	}}
	toolResult := llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeTool}
	final := llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: " denied: X "}}}

	for name, tc := range map[string]struct {
		trail []llmtypes.MessageContent
		want  string
	}{
		"empty":                {nil, ""},
		"user only":            {[]llmtypes.MessageContent{user}, ""},
		"narration + toolcall": {[]llmtypes.MessageContent{user, narration}, ""},
		"awaiting reply":       {[]llmtypes.MessageContent{user, narration, toolResult}, ""},
		"finished":             {[]llmtypes.MessageContent{user, narration, toolResult, final}, "denied: X"},
	} {
		if got := cursorCompletedTurnAnswer(tc.trail); got != tc.want {
			t.Errorf("%s: got %q want %q", name, got, tc.want)
		}
	}
}
