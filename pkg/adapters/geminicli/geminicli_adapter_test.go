package geminicli

import (
	"fmt"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type MockLogger struct{}

func (l *MockLogger) Infof(format string, args ...any)        { fmt.Printf("INFO: "+format+"\n", args...) }
func (l *MockLogger) Errorf(format string, args ...any)       { fmt.Printf("ERROR: "+format+"\n", args...) }
func (l *MockLogger) Debugf(format string, args ...interface{}) { fmt.Printf("DEBUG: "+format+"\n", args...) }

func TestExtractTextFromMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    llmtypes.MessageContent
		expected string
	}{
		{
			name: "Single text part",
			input: llmtypes.MessageContent{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: "Hello"},
				},
			},
			expected: "Hello",
		},
		{
			name: "Multiple text parts",
			input: llmtypes.MessageContent{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: "Hello"},
					llmtypes.TextContent{Text: "World"},
				},
			},
			expected: "Hello\nWorld",
		},
		{
			name: "Empty message",
			input: llmtypes.MessageContent{
				Role:  llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTextFromMessage(tt.input)
			if result != tt.expected {
				t.Errorf("extractTextFromMessage() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestMapResultToContentResponse(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	raw := map[string]interface{}{
		"type":       "result",
		"session_id": "test-session-123",
		"result":     "Hello world",
		"stats": map[string]interface{}{
			"input_tokens":  float64(100),
			"output_tokens": float64(50),
			"total_tokens":  float64(150),
		},
	}

	resp := adapter.mapResultToContentResponse(raw, "test-session-123", "", "Hello world", "")

	// Verify Content
	if len(resp.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Content != "Hello world" {
		t.Errorf("Expected content 'Hello world', got '%s'", resp.Choices[0].Content)
	}

	// Verify Usage
	if resp.Usage.InputTokens != 100 {
		t.Errorf("Expected 100 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("Expected 50 output tokens, got %d", resp.Usage.OutputTokens)
	}
	if resp.Usage.TotalTokens != 150 {
		t.Errorf("Expected 150 total tokens, got %d", resp.Usage.TotalTokens)
	}

	// Verify GenerationInfo / Additional
	genInfo := resp.Choices[0].GenerationInfo
	if genInfo == nil {
		t.Fatal("GenerationInfo is nil")
	}
	if sid, ok := genInfo.Additional["gemini_session_id"].(string); !ok || sid != "test-session-123" {
		t.Errorf("Expected session ID 'test-session-123', got %v", genInfo.Additional["gemini_session_id"])
	}
}

func TestMapResultToContentResponse_UsageField(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	// Test with "usage" field instead of "stats"
	raw := map[string]interface{}{
		"type":   "result",
		"result": "Test response",
		"usage": map[string]interface{}{
			"input_tokens":  float64(200),
			"output_tokens": float64(100),
		},
	}

	resp := adapter.mapResultToContentResponse(raw, "session-456", "", "Test response", "")

	if resp.Usage.InputTokens != 200 {
		t.Errorf("Expected 200 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 100 {
		t.Errorf("Expected 100 output tokens, got %d", resp.Usage.OutputTokens)
	}
	// total_tokens should be computed as input + output when not provided
	if resp.Usage.TotalTokens != 300 {
		t.Errorf("Expected 300 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestMapResultToContentResponse_EmptyResult(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	raw := map[string]interface{}{
		"type":     "result",
		"response": "Fallback response text",
	}

	resp := adapter.mapResultToContentResponse(raw, "", "", "Fallback response text", "")

	if resp.Choices[0].Content != "Fallback response text" {
		t.Errorf("Expected fallback to 'response' field, got '%s'", resp.Choices[0].Content)
	}
}

func TestGetModelMetadata(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	meta, err := adapter.GetModelMetadata("gemini-2.5-flash")
	if err != nil {
		t.Fatalf("GetModelMetadata() error = %v", err)
	}

	if meta.Provider != "gemini-cli" {
		t.Errorf("Expected provider 'gemini-cli', got '%s'", meta.Provider)
	}
	if meta.InputCostPer1MTokens != 0 {
		t.Errorf("Expected zero input cost, got %f", meta.InputCostPer1MTokens)
	}
	if meta.OutputCostPer1MTokens != 0 {
		t.Errorf("Expected zero output cost, got %f", meta.OutputCostPer1MTokens)
	}
}

func TestGetModelID(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-pro", &MockLogger{})
	if adapter.GetModelID() != "gemini-2.5-pro" {
		t.Errorf("Expected model ID 'gemini-2.5-pro', got '%s'", adapter.GetModelID())
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}
