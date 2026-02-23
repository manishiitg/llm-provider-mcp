package claudecode

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestClaudeCodeStreaming(t *testing.T) {
	// Only run this test if we're in an environment with the claude CLI installed
	if os.Getenv("CI") != "" {
		t.Skip("Skipping integration test in CI environment")
	}

	adapter := NewClaudeCodeAdapter("", "test-model", &MockLogger{})

	// Create a channel to receive stream chunks
	streamChan := make(chan llmtypes.StreamChunk, 100)

	// Send a prompt that explicitly requires a tool call
	messages := []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Use the Bash tool to run 'echo hello stream test'"},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Run in a goroutine so we can consume the channel
	errChan := make(chan error, 1)
	go func() {
		_, err := adapter.GenerateContent(ctx, messages, llmtypes.WithStreamingChan(streamChan), WithDangerouslySkipPermissions())
		errChan <- err
	}()

	var receivedToolStart bool
	var receivedToolEnd bool

	// Consume the stream
	for {
		select {
		case chunk, ok := <-streamChan:
			if !ok {
				streamChan = nil
				continue
			}
			t.Logf("Received chunk: %s", chunk.Type)
			
			switch chunk.Type {
			case llmtypes.StreamChunkTypeToolCallStart:
				receivedToolStart = true
				t.Logf("Tool Call Start: %s (ID: %s)", chunk.ToolName, chunk.ToolCallID)
			case llmtypes.StreamChunkTypeToolCallEnd:
				receivedToolEnd = true
				t.Logf("Tool Call End: %s (Args: %s)", chunk.ToolName, chunk.ToolArgs)
			case llmtypes.StreamChunkTypeContent:
				t.Logf("Content: %s", chunk.Content)
			}
		case err := <-errChan:
			if err != nil {
				t.Fatalf("GenerateContent failed: %v", err)
			}
			
			// Verify we received the expected chunks
			if !receivedToolStart {
				t.Error("Did not receive StreamChunkTypeToolCallStart")
			}
			if !receivedToolEnd {
				t.Error("Did not receive StreamChunkTypeToolCallEnd")
			}
			
			t.Log("Test completed successfully")
			return
		case <-ctx.Done():
			t.Logf("Test timed out. Received tool start: %v, end: %v", receivedToolStart, receivedToolEnd)
			t.Fatal("Test timed out")
			return
		}
	}
}
