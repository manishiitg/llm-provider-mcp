package claudecode

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type MockLogger struct{}

func (l *MockLogger) Infof(format string, args ...any)  { fmt.Printf("INFO: "+format+"\n", args...) }
func (l *MockLogger) Errorf(format string, args ...any) { fmt.Printf("ERROR: "+format+"\n", args...) }
func (l *MockLogger) Debugf(format string, args ...interface{}) {
	fmt.Printf("DEBUG: "+format+"\n", args...)
}

func TestConvertMessageToStreamJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    llmtypes.MessageContent
		expected string
	}{
		{
			name: "User Message",
			input: llmtypes.MessageContent{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: "Hello"},
				},
			},
			expected: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Hello"}]}}`,
		},
		{
			name: "Assistant Message",
			input: llmtypes.MessageContent{
				Role: llmtypes.ChatMessageTypeAI,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: "Hi there"},
				},
			},
			expected: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi there"}]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertMessageToStreamJSON(tt.input)
			if err != nil {
				t.Fatalf("convertMessageToStreamJSON() error = %v", err)
			}

			// Marshal to string for comparison
			jsonBytes, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			jsonString := string(jsonBytes)

			if jsonString != tt.expected {
				t.Errorf("convertMessageToStreamJSON() = %v, want %v", jsonString, tt.expected)
			}
		})
	}
}

func TestMapResponseToContentResponse(t *testing.T) {
	adapter := NewClaudeCodeAdapter("", "test-model", &MockLogger{})

	// Mock Claude CLI Output
	cliOutput := &ClaudeCodeResponse{
		Result:       "Hello world",
		TotalCostUSD: 0.005,
		Usage: ClaudeUsage{
			InputTokens:              100,
			OutputTokens:             50,
			CacheReadInputTokens:     20,
			CacheCreationInputTokens: 10,
		},
		PermissionDenials: []PermissionDenial{
			{
				ToolName:  "bash",
				ToolUseID: "toolu_123",
				ToolInput: map[string]interface{}{"command": "rm -rf /"},
			},
		},
	}

	resp, err := adapter.mapResponseToContentResponse(cliOutput)
	if err != nil {
		t.Fatalf("mapResponseToContentResponse() error = %v", err)
	}

	// Verify Content
	if len(resp.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Content != "Hello world" {
		t.Errorf("Expected content 'Hello world', got '%s'", resp.Choices[0].Content)
	}

	// Verify Usage
	// InputTokens = raw input (100) + cache_read (20) = 120
	if resp.Usage.InputTokens != 120 {
		t.Errorf("Expected 120 input tokens (100 + 20 cache_read), got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("Expected 50 output tokens, got %d", resp.Usage.OutputTokens)
	}
	if *resp.Usage.CacheTokens != 20 {
		t.Errorf("Expected 20 cache tokens, got %d", *resp.Usage.CacheTokens)
	}

	// Verify GenerationInfo / Additional
	genInfo := resp.Choices[0].GenerationInfo
	if genInfo == nil {
		t.Fatal("GenerationInfo is nil")
	}
	if cost, ok := genInfo.Additional["cost_usd"].(float64); !ok || cost != 0.005 {
		t.Errorf("Expected cost 0.005, got %v", genInfo.Additional["cost_usd"])
	}

	// Verify Permission Denials
	denials, ok := genInfo.Additional["permission_denials"].([]PermissionDenial)
	if !ok {
		t.Fatal("permission_denials missing or wrong type in Additional")
	}
	if len(denials) != 1 {
		t.Errorf("Expected 1 denial, got %d", len(denials))
	}
	if denials[0].ToolName != "bash" {
		t.Errorf("Expected tool name 'bash', got '%s'", denials[0].ToolName)
	}
}

func TestClaudeCodeAdapterImplementsWebSearchModel(t *testing.T) {
	adapter := NewClaudeCodeAdapter("", "test-model", &MockLogger{})
	if _, ok := interface{}(adapter).(llmtypes.WebSearchModel); !ok {
		t.Fatal("ClaudeCodeAdapter should implement llmtypes.WebSearchModel")
	}
}
