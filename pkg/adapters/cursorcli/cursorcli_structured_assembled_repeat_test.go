package cursorcli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	llmtypes "github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Replays the shape probed from real cursor-agent 2026.09.02 on 2026-09-03:
// under --stream-partial-output each fragment is an "assistant" event with
// timestamp_ms and no subtype, and the assembled span follows as an
// "assistant" event with NO subtype and NO timestamp_ms. The adapter keyed on
// subtype alone, forwarded the assembled span as one more delta, and every
// sentence rendered twice in the product UI.
func TestCursorStructuredDropsAssembledSpanRepeat(t *testing.T) {
	fakeBin := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s1","model":"auto"}'
printf '%s\n' '{"type":"thinking","subtype":"delta","text":"Plan","timestamp_ms":1}'
printf '%s\n' '{"type":"thinking","subtype":"delta","text":"ning.","timestamp_ms":2}'
printf '%s\n' '{"type":"thinking","subtype":"completed","timestamp_ms":3}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Two"}]},"timestamp_ms":4}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":" short"}]},"timestamp_ms":5}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":" sentences."}]},"timestamp_ms":6}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Two short sentences."}]}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"Two short sentences.","session_id":"s1","usage":{"inputTokens":3,"outputTokens":2}}'
`
	if err := os.WriteFile(filepath.Join(fakeBin, "cursor-agent"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake cursor-agent: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	stream := make(chan llmtypes.StreamChunk, 32)
	resp, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say two sentences."}}},
	}, WithCursorStructuredTransport(true), llmtypes.WithStreamingChan(stream))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if resp == nil || len(resp.Choices) != 1 || resp.Choices[0].Content != "Two short sentences." {
		t.Fatalf("response = %#v, want the span once", resp)
	}

	var content, reasoning string
	var contentChunks int
	for chunk := range stream {
		switch chunk.Type {
		case llmtypes.StreamChunkTypeContent:
			contentChunks++
			if !contentChunkIsDelta(chunk) {
				t.Fatalf("content fragment %q must be marked as a delta", chunk.Content)
			}
			content += chunk.Content
		case llmtypes.StreamChunkTypeReasoning:
			if !contentChunkIsDelta(chunk) {
				t.Fatalf("thinking fragment %q must be marked as a delta", chunk.Content)
			}
			reasoning += chunk.Content
		}
	}
	if content != "Two short sentences." {
		t.Fatalf("streamed content = %q, want the span exactly once", content)
	}
	if contentChunks != 3 {
		t.Fatalf("content chunks = %d, want the 3 fragments only", contentChunks)
	}
	if reasoning != "Planning." {
		t.Fatalf("streamed thinking = %q", reasoning)
	}
}

func contentChunkIsDelta(chunk llmtypes.StreamChunk) bool {
	v, ok := chunk.Metadata[llmtypes.ContentDeltaMetadataKey].(bool)
	return ok && v
}
