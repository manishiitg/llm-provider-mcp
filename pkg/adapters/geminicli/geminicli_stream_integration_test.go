package geminicli

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestGeminiCLIStreaming(t *testing.T) {
	// Only run this test if we're in an environment with the gemini CLI installed
	if os.Getenv("CI") != "" {
		t.Skip("Skipping integration test in CI environment")
	}

	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	// Create a channel to receive stream chunks
	streamChan := make(chan llmtypes.StreamChunk, 100)

	// Send a simple prompt
	messages := []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Say hello in one short sentence."},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Run in a goroutine so we can consume the channel
	errChan := make(chan error, 1)
	go func() {
		_, err := adapter.GenerateContent(ctx, messages, llmtypes.WithStreamingChan(streamChan), WithApprovalMode("yolo"))
		errChan <- err
	}()

	var receivedContent bool

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
				t.Logf("Tool Call Start: %s (ID: %s)", chunk.ToolName, chunk.ToolCallID)
			case llmtypes.StreamChunkTypeToolCallEnd:
				t.Logf("Tool Call End: %s (Args: %s)", chunk.ToolName, chunk.ToolArgs)
			case llmtypes.StreamChunkTypeContent:
				receivedContent = true
				t.Logf("Content: %s", chunk.Content)
			}
		case err := <-errChan:
			if err != nil {
				t.Fatalf("GenerateContent failed: %v", err)
			}

			// Verify we received content
			if !receivedContent {
				t.Error("Did not receive any StreamChunkTypeContent")
			}

			t.Log("Test completed successfully")
			return
		case <-ctx.Done():
			t.Logf("Test timed out. Received content: %v", receivedContent)
			t.Fatal("Test timed out")
			return
		}
	}
}
