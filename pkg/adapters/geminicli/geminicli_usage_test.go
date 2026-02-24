package geminicli

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestGeminiCLIUsageAndCost(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping integration test in CI environment")
	}

	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	streamChan := make(chan llmtypes.StreamChunk, 100)

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

	// Drain stream in background
	go func() {
		for range streamChan {}
	}()

	resp, err := adapter.GenerateContent(ctx, messages, llmtypes.WithStreamingChan(streamChan), WithApprovalMode("yolo"))
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}

	t.Logf("=== ContentResponse ===")
	t.Logf("Content: %q", resp.Choices[0].Content)

	// Check Usage
	t.Logf("=== Usage ===")
	t.Logf("InputTokens:  %d", resp.Usage.InputTokens)
	t.Logf("OutputTokens: %d", resp.Usage.OutputTokens)
	t.Logf("TotalTokens:  %d", resp.Usage.TotalTokens)

	if resp.Usage.InputTokens == 0 {
		t.Error("InputTokens is 0 — expected non-zero")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("OutputTokens is 0 — expected non-zero")
	}
	if resp.Usage.TotalTokens == 0 {
		t.Error("TotalTokens is 0 — expected non-zero")
	}

	// Check GenerationInfo
	genInfo := resp.Choices[0].GenerationInfo
	if genInfo == nil {
		t.Fatal("GenerationInfo is nil")
	}
	t.Logf("=== GenerationInfo ===")
	t.Logf("InputTokens:  %v", genInfo.InputTokens)
	t.Logf("OutputTokens: %v", genInfo.OutputTokens)
	t.Logf("TotalTokens:  %v", genInfo.TotalTokens)
	t.Logf("Additional:   %+v", genInfo.Additional)

	// Check session ID
	sid, ok := genInfo.Additional["gemini_session_id"].(string)
	t.Logf("Session ID: %q (found: %v)", sid, ok)
	if !ok || sid == "" {
		t.Error("gemini_session_id missing or empty in GenerationInfo.Additional")
	}

	// Check content is non-empty
	if resp.Choices[0].Content == "" {
		t.Error("Content is empty — accumulated text not captured")
	}
}
