package codexcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type MockLogger struct{}

func (l *MockLogger) Infof(format string, args ...any)  { fmt.Printf("INFO: "+format+"\n", args...) }
func (l *MockLogger) Errorf(format string, args ...any) { fmt.Printf("ERROR: "+format+"\n", args...) }
func (l *MockLogger) Debugf(format string, args ...interface{}) {
	fmt.Printf("DEBUG: "+format+"\n", args...)
}

func TestCodexCLIAdapterImplementsWebSearchModel(t *testing.T) {
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})
	if _, ok := interface{}(adapter).(llmtypes.WebSearchModel); !ok {
		t.Fatal("CodexCLIAdapter should implement llmtypes.WebSearchModel")
	}
}

func TestLooksLikeCodexRateLimit(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{line: "error: 429 Too Many Requests", want: true},
		{line: "service unavailable from upstream", want: true},
		{line: "you hit your usage limit, try again later", want: true},
		{line: `WARN codex_core::shell_snapshot: Failed to delete shell snapshot at "/tmp/x": No such file or directory`, want: false},
		{line: "migration 21 was previously applied but is missing in the resolved migrations", want: false},
	}

	for _, tt := range tests {
		if got := looksLikeCodexRateLimit(tt.line); got != tt.want {
			t.Fatalf("looksLikeCodexRateLimit(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestCodexStringConfigOverrideEscapesDeveloperInstructions(t *testing.T) {
	got, err := codexStringConfigOverride("developer_instructions", "Line \"one\"\nPath C:\\tmp")
	if err != nil {
		t.Fatalf("codexStringConfigOverride returned error: %v", err)
	}

	want := `developer_instructions="Line \"one\"\nPath C:\\tmp"`
	if got != want {
		t.Fatalf("override = %q, want %q", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("override contains a raw newline: %q", got)
	}
}

func TestGenerateContentReturnsNestedCodexErrorMessage(t *testing.T) {
	tmpDir := t.TempDir()
	fakeCodexPath := filepath.Join(tmpDir, "codex")
	fakeCodex := `#!/bin/sh
printf '%s\n' '{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The gpt-5.5 model requires a newer version of Codex."}}'
exit 1
`
	if err := os.WriteFile(fakeCodexPath, []byte(fakeCodex), 0755); err != nil {
		t.Fatalf("failed to write fake codex binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	adapter := NewCodexCLIAdapter("", "gpt-5.5", &MockLogger{})
	_, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say ok"}}},
	})
	if err == nil {
		t.Fatal("GenerateContent returned nil error for failing CLI")
	}
	if !strings.Contains(err.Error(), "requires a newer version of Codex") {
		t.Fatalf("error = %q, want nested Codex error message", err.Error())
	}
	if strings.Contains(err.Error(), `"type":"error"`) {
		t.Fatalf("error = %q, want extracted message instead of raw event JSON", err.Error())
	}
}

func TestGenerateContentFullAutoUsesCodexBypassFlag(t *testing.T) {
	tmpDir := t.TempDir()
	fakeCodexPath := filepath.Join(tmpDir, "codex")
	argsPath := filepath.Join(tmpDir, "args.txt")
	fakeCodex := `#!/bin/sh
printf '%s\n' "$@" > "$CODEX_FAKE_ARGS_FILE"
printf '%s\n' '{"type":"thread.started","thread_id":"thread_test"}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"ok"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`
	if err := os.WriteFile(fakeCodexPath, []byte(fakeCodex), 0755); err != nil {
		t.Fatalf("failed to write fake codex binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_FAKE_ARGS_FILE", argsPath)

	adapter := NewCodexCLIAdapter("", "gpt-5.5", &MockLogger{})
	resp, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say ok"}}},
	})
	if err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("response choices = %d, want 1", len(resp.Choices))
	}
	if got := strings.TrimSpace(resp.Choices[0].Content); got != "ok" {
		t.Fatalf("response text = %q, want ok", got)
	}

	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("failed to read fake codex args: %v", err)
	}
	args := string(argsBytes)
	if !strings.Contains(args, "--dangerously-bypass-approvals-and-sandbox\n") {
		t.Fatalf("args = %q, want Codex 0.128 bypass flag", args)
	}
	if strings.Contains(args, "--full-auto") {
		t.Fatalf("args = %q, deprecated --full-auto should not be used", args)
	}
}

func TestGenerateContentStreamsMCPToolErrorMessage(t *testing.T) {
	tmpDir := t.TempDir()
	fakeCodexPath := filepath.Join(tmpDir, "codex")
	fakeCodex := `#!/bin/sh
printf '%s\n' '{"type":"thread.started","thread_id":"thread_test"}'
printf '%s\n' '{"type":"item.started","item":{"id":"item_0","type":"mcp_tool_call","server":"probe","tool":"ping","arguments":{},"result":null,"error":null,"status":"in_progress"}}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_0","type":"mcp_tool_call","server":"probe","tool":"ping","arguments":{},"result":null,"error":{"message":"user cancelled MCP tool call"},"status":"failed"}}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`
	if err := os.WriteFile(fakeCodexPath, []byte(fakeCodex), 0755); err != nil {
		t.Fatalf("failed to write fake codex binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stream := make(chan llmtypes.StreamChunk, 10)
	adapter := NewCodexCLIAdapter("", "gpt-5.5", &MockLogger{})
	_, err := adapter.GenerateContent(
		context.Background(),
		[]llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use tool"}}},
		},
		func(opts *llmtypes.CallOptions) {
			opts.StreamChan = stream
		},
	)
	if err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}

	var gotToolError bool
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				if !gotToolError {
					t.Fatalf("stream did not include MCP tool error result")
				}
				return
			}
			if chunk.Type == llmtypes.StreamChunkTypeToolCallEnd &&
				strings.Contains(chunk.ToolResult, "user cancelled MCP tool call") {
				gotToolError = true
			}
		default:
			if !gotToolError {
				t.Fatalf("stream did not include MCP tool error result")
			}
			return
		}
	}
}
