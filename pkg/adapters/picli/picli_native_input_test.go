package picli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestPiNativeProgressPreservesToolsWithoutLeakingResultsIntoChatHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	log := `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"read file"}]}}
{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"checking"},{"type":"toolCall","id":"call1","name":"Read","arguments":{"path":"file"}}]}}
{"type":"message","message":{"role":"toolResult","toolCallId":"call1","toolName":"Read","isError":true,"content":[{"type":"text","text":"denied"}]}}
{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"Cannot read it."}]}}
`
	if err := os.WriteFile(path, []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	summary := readPiTranscriptSummaryFile(path, time.Time{})
	var call llmtypes.ToolCall
	var result llmtypes.ToolCallResponse
	for _, message := range summary.StructuredProgressMessages {
		for _, part := range message.Parts {
			switch part := part.(type) {
			case llmtypes.ToolCall:
				call = part
			case llmtypes.ToolCallResponse:
				result = part
			}
		}
	}
	if call.ID != "call1" || result.ToolCallID != call.ID || !result.IsError || result.Content != "denied" {
		t.Fatalf("tools=%+v %+v", call, result)
	}
	transcript, ok, err := readNativeTranscriptPath(path)
	if err != nil || !ok || len(transcript.Messages) != 2 {
		t.Fatalf("tool results leaked into human chat: %+v %v", transcript, err)
	}
}
