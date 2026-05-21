package opencodecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type MockLogger struct{}

func (m *MockLogger) Infof(format string, args ...interface{})  {}
func (m *MockLogger) Errorf(format string, args ...interface{}) {}
func (m *MockLogger) Debugf(format string, args ...interface{}) {}

func TestResolveOpenCodeCLIModelID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "default", input: "", want: ""},
		{name: "provider default", input: "opencode-cli", want: ""},
		{name: "auto", input: "auto", want: ""},
		{name: "provider model", input: "anthropic/claude-sonnet-4-5", want: "anthropic/claude-sonnet-4-5"},
		{name: "high alias", input: "high", want: "openai/gpt-5.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveOpenCodeCLIModelID(tt.input); got != tt.want {
				t.Fatalf("resolveOpenCodeCLIModelID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestOpenCodeCLIModelMetadata(t *testing.T) {
	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	meta, err := adapter.GetModelMetadata("")
	if err != nil {
		t.Fatalf("GetModelMetadata() error = %v", err)
	}
	if meta.Provider != "opencode-cli" {
		t.Fatalf("Provider = %q, want opencode-cli", meta.Provider)
	}
	if meta.ModelID != "opencode-cli" {
		t.Fatalf("ModelID = %q, want opencode-cli", meta.ModelID)
	}
}

func TestOpenCodeCLIStructuredStreamMirrorsAssistantTextToTerminal(t *testing.T) {
	fakeBin := t.TempDir()
	opencodePath := filepath.Join(fakeBin, "opencode")
	script := `#!/bin/sh
printf '%s\n' '{"type":"text","sessionID":"opencode-structured","part":{"type":"text","text":"assistant terminal mirror ok"}}'
printf '%s\n' '{"type":"step_finish","sessionID":"opencode-structured","part":{"reason":"stop","tokens":{"total":3,"input":1,"output":2,"reasoning":0,"cache":{"write":0,"read":0}},"cost":0}}'
`
	if err := os.WriteFile(opencodePath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENCODE_BIN", opencodePath)

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	streamChan := make(chan llmtypes.StreamChunk, 32)
	resp, err := adapter.GenerateContent(context.Background(),
		[]llmtypes.MessageContent{llmtypes.TextParts(llmtypes.ChatMessageTypeHuman, "route this")},
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 || !strings.Contains(resp.Choices[0].Content, "assistant terminal mirror ok") {
		t.Fatalf("unexpected response: %#v", resp)
	}

	var assistantContent strings.Builder
	var terminalContent strings.Builder
	for chunk := range streamChan {
		switch chunk.Type {
		case llmtypes.StreamChunkTypeContent:
			assistantContent.WriteString(chunk.Content)
		case llmtypes.StreamChunkTypeTerminal:
			terminalContent.WriteString(chunk.Content)
			terminalContent.WriteString("\n")
		}
	}
	if !strings.Contains(assistantContent.String(), "assistant terminal mirror ok") {
		t.Fatalf("assistant stream missing final text: %q", assistantContent.String())
	}
	if !strings.Contains(terminalContent.String(), "assistant terminal mirror ok") {
		t.Fatalf("terminal stream missing assistant text:\n%s", terminalContent.String())
	}
}
