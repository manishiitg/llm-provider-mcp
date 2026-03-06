package minimax

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type MockLogger struct{}

func (l *MockLogger) Infof(format string, args ...any)          { fmt.Printf("INFO: "+format+"\n", args...) }
func (l *MockLogger) Errorf(format string, args ...any)         { fmt.Printf("ERROR: "+format+"\n", args...) }
func (l *MockLogger) Debugf(format string, args ...interface{}) { fmt.Printf("DEBUG: "+format+"\n", args...) }

// ---- unit tests (no API calls) ----

func TestGetModelID(t *testing.T) {
	adapter := NewMiniMaxAdapter("fake-key", ModelMiniMaxM25, &MockLogger{})
	if adapter.GetModelID() != ModelMiniMaxM25 {
		t.Errorf("expected %s, got %s", ModelMiniMaxM25, adapter.GetModelID())
	}
}

func TestGetModelMetadata(t *testing.T) {
	adapter := NewMiniMaxAdapter("fake-key", ModelMiniMaxM25, &MockLogger{})

	meta, err := adapter.GetModelMetadata(ModelMiniMaxM25)
	if err != nil {
		t.Fatalf("GetModelMetadata() error = %v", err)
	}
	if meta.Provider != "minimax" {
		t.Errorf("expected provider 'minimax', got %q", meta.Provider)
	}
	if meta.ContextWindow != 1000000 {
		t.Errorf("expected 1M context window, got %d", meta.ContextWindow)
	}
}

func TestGetModelMetadata_Unknown(t *testing.T) {
	adapter := NewMiniMaxAdapter("fake-key", ModelMiniMaxM25, &MockLogger{})
	_, err := adapter.GetModelMetadata("unknown-model")
	if err == nil {
		t.Error("expected error for unknown model, got nil")
	}
}

func TestConvertMessages_SystemAndUser(t *testing.T) {
	messages := []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "You are helpful."}},
		},
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Hello"}},
		},
	}

	result := convertMessages(messages)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].OfSystem == nil {
		t.Error("expected first message to be system")
	}
	if result[1].OfUser == nil {
		t.Error("expected second message to be user")
	}
}

func TestConvertMessages_AssistantWithToolCalls(t *testing.T) {
	messages := []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeAI,
			Parts: []llmtypes.ContentPart{
				llmtypes.ToolCall{
					ID:   "call_123",
					Type: "function",
					FunctionCall: &llmtypes.FunctionCall{
						Name:      "get_weather",
						Arguments: `{"city":"Paris"}`,
					},
				},
			},
		},
	}

	result := convertMessages(messages)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].OfAssistant == nil {
		t.Error("expected assistant message")
	}
	if len(result[0].OfAssistant.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(result[0].OfAssistant.ToolCalls))
	}
}

func TestConvertMessages_ToolResponse(t *testing.T) {
	messages := []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeTool,
			Parts: []llmtypes.ContentPart{
				llmtypes.ToolCallResponse{
					ToolCallID: "call_123",
					Content:    `{"temperature":"22C"}`,
				},
			},
		},
	}

	result := convertMessages(messages)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].OfTool == nil {
		t.Error("expected tool message")
	}
}

func TestConvertMessages_MultiPartText(t *testing.T) {
	messages := []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Hello"},
				llmtypes.TextContent{Text: "World"},
			},
		},
	}

	result := convertMessages(messages)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].OfUser == nil {
		t.Error("expected user message")
	}
}

func TestGetAllMiniMaxModels(t *testing.T) {
	models := GetAllMiniMaxModels()
	if len(models) != 5 {
		t.Errorf("expected 5 models, got %d", len(models))
	}
	for _, m := range models {
		if m.Provider != "minimax" {
			t.Errorf("model %s has wrong provider %q", m.ModelID, m.Provider)
		}
		if m.ContextWindow != 1000000 {
			t.Errorf("model %s has unexpected context window %d", m.ModelID, m.ContextWindow)
		}
	}
}

// ---- integration tests (require MINIMAX_API_KEY) ----

func TestMiniMaxIntegration_SimpleGeneration(t *testing.T) {
	apiKey := os.Getenv("MINIMAX_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: MINIMAX_API_KEY not set")
	}

	adapter := NewMiniMaxAdapter(apiKey, ModelMiniMaxM25, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	messages := []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply with exactly: pong"}},
		},
	}

	resp, err := adapter.GenerateContent(ctx, messages)
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if resp.Choices[0].Content == "" {
		t.Error("expected non-empty content")
	}
	if resp.Choices[0].StopReason == "" {
		t.Error("expected non-empty stop reason")
	}

	gi := resp.Choices[0].GenerationInfo
	if gi == nil {
		t.Fatal("expected GenerationInfo")
	}
	if gi.InputTokens == nil || *gi.InputTokens == 0 {
		t.Error("expected non-zero input tokens")
	}
	if gi.OutputTokens == nil || *gi.OutputTokens == 0 {
		t.Error("expected non-zero output tokens")
	}

	t.Logf("Response: %s", resp.Choices[0].Content)
	t.Logf("Tokens: input=%d output=%d total=%d", *gi.InputTokens, *gi.OutputTokens, *gi.TotalTokens)
}

func TestMiniMaxIntegration_Streaming(t *testing.T) {
	apiKey := os.Getenv("MINIMAX_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: MINIMAX_API_KEY not set")
	}

	adapter := NewMiniMaxAdapter(apiKey, ModelMiniMaxM25, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	messages := []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Count from 1 to 3."}},
		},
	}

	streamChan := make(chan llmtypes.StreamChunk, 100)
	var resp *llmtypes.ContentResponse
	errChan := make(chan error, 1)
	go func() {
		var err error
		resp, err = adapter.GenerateContent(ctx, messages, llmtypes.WithStreamingChan(streamChan))
		errChan <- err
	}()

	var streamed string
	for chunk := range streamChan {
		if chunk.Type == llmtypes.StreamChunkTypeContent {
			streamed += chunk.Content
		}
	}
	if err := <-errChan; err != nil {
		t.Fatalf("streaming failed: %v", err)
	}
	if streamed == "" {
		t.Error("expected streamed content")
	}

	if resp != nil && len(resp.Choices) > 0 && resp.Choices[0].GenerationInfo != nil {
		gi := resp.Choices[0].GenerationInfo
		t.Logf("Streamed: %s", streamed)
		if gi.InputTokens != nil {
			t.Logf("Tokens: input=%d output=%d total=%d", *gi.InputTokens, *gi.OutputTokens, *gi.TotalTokens)
		}
	}
}

func TestMiniMaxIntegration_AllModels(t *testing.T) {
	apiKey := os.Getenv("MINIMAX_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: MINIMAX_API_KEY not set")
	}

	models := []string{
		ModelMiniMaxM25,
		ModelMiniMaxM25HighSpeed,
		ModelMiniMaxM21,
		ModelMiniMaxM21HighSpeed,
		ModelMiniMaxM2,
	}

	for _, modelID := range models {
		t.Run(modelID, func(t *testing.T) {
			adapter := NewMiniMaxAdapter(apiKey, modelID, &MockLogger{})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			messages := []llmtypes.MessageContent{
				{
					Role:  llmtypes.ChatMessageTypeHuman,
					Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say hi."}},
				},
			}

			resp, err := adapter.GenerateContent(ctx, messages)
			if err != nil {
				t.Fatalf("[%s] failed: %v", modelID, err)
			}
			if len(resp.Choices) == 0 || resp.Choices[0].Content == "" {
				t.Errorf("[%s] empty response", modelID)
			}
			gi := resp.Choices[0].GenerationInfo
			if gi != nil && gi.InputTokens != nil {
				t.Logf("[%s] tokens: input=%d output=%d total=%d", modelID, *gi.InputTokens, *gi.OutputTokens, *gi.TotalTokens)
			}
		})
	}
}
