package opencodecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
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
printf '%s\n' '{"type":"tool_use","sessionID":"opencode-structured","part":{"type":"tool_use","tool":"execute_shell_command","callID":"call-1","state":{"status":"completed","input":{"command":"echo noisy"},"output":"api-bridge stdout should not be final"}}}'
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
	testcontracts.AssertCleanFinalExtraction(t, "opencode-cli", resp.Choices[0].Content,
		[]string{"assistant terminal mirror ok"},
		[]string{"execute_shell_command", "api-bridge", "stdout", "echo noisy"},
	)

	var assistantContent strings.Builder
	var terminalContent strings.Builder
	var toolChunkCount int
	for chunk := range streamChan {
		switch chunk.Type {
		case llmtypes.StreamChunkTypeContent:
			assistantContent.WriteString(chunk.Content)
		case llmtypes.StreamChunkTypeTerminal:
			terminalContent.WriteString(chunk.Content)
			terminalContent.WriteString("\n")
		case llmtypes.StreamChunkTypeToolCallStart, llmtypes.StreamChunkTypeToolCallEnd:
			toolChunkCount++
		}
	}
	if !strings.Contains(assistantContent.String(), "assistant terminal mirror ok") {
		t.Fatalf("assistant stream missing final text: %q", assistantContent.String())
	}
	testcontracts.AssertCleanFinalExtraction(t, "opencode-cli stream", assistantContent.String(),
		[]string{"assistant terminal mirror ok"},
		[]string{"execute_shell_command", "api-bridge", "stdout", "echo noisy"},
	)
	if !strings.Contains(terminalContent.String(), "assistant terminal mirror ok") {
		t.Fatalf("terminal stream missing assistant text:\n%s", terminalContent.String())
	}
	if toolChunkCount != 2 {
		t.Fatalf("tool chunks = %d, want start and end chunks for structured tool event", toolChunkCount)
	}
}

func TestOpenCodeFinalExtractionVertexJudgeE2E(t *testing.T) {
	rawEvents := strings.Join([]string{
		`{"type":"tool_use","sessionID":"opencode-structured","part":{"type":"tool_use","tool":"execute_shell_command","callID":"call-1","state":{"status":"completed","input":{"command":"echo noisy"},"output":"api-bridge stdout should not be final"}}}`,
		`{"type":"text","sessionID":"opencode-structured","part":{"type":"text","text":"assistant terminal mirror ok"}}`,
		`{"type":"step_finish","sessionID":"opencode-structured","part":{"reason":"stop","tokens":{"total":3,"input":1,"output":2,"reasoning":0,"cache":{"write":0,"read":0}},"cost":0}}`,
	}, "\n")

	fakeBin := t.TempDir()
	opencodePath := filepath.Join(fakeBin, "opencode")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	for _, line := range strings.Split(rawEvents, "\n") {
		script.WriteString("printf '%s\\n' '")
		script.WriteString(line)
		script.WriteString("'\n")
	}
	if err := os.WriteFile(opencodePath, []byte(script.String()), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENCODE_BIN", opencodePath)

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	resp, err := adapter.GenerateContent(context.Background(),
		[]llmtypes.MessageContent{llmtypes.TextParts(llmtypes.ChatMessageTypeHuman, "route this")},
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0] == nil {
		t.Fatalf("unexpected response: %#v", resp)
	}
	testcontracts.AssertVertexJudgesFinalExtraction(t, testcontracts.FinalExtractionJudgeCase{
		Provider:   "opencode-cli",
		TmuxScreen: rawEvents,
		Extracted:  resp.Choices[0].Content,
		UserGoal:   "Return only the structured text event as final assistant content.",
		MustContain: []string{
			"assistant terminal mirror ok",
		},
		Forbidden: []string{
			"execute_shell_command",
			"api-bridge",
			"stdout",
			"echo noisy",
			"step_finish",
		},
		ExpectedNote: "OpenCode is structured-only, so the raw provider output is JSON events rather than a tmux pane.",
	})
}
