package opencodecli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

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

func TestOpenCodeRunningPaneWithLiveInputIsNotReady(t *testing.T) {
	pane := `OpenCode
Running Shell(python slow.py)
esc to interrupt
> ## Pre-validation failed (retry attempt 3)
Ask anything
ctrl+p commands opencode`

	if !hasOpenCodeActivity(pane) {
		t.Fatal("running pane with live input should count as active")
	}
	if hasOpenCodeReadyPrompt(pane) {
		t.Fatal("running pane with live input must not be treated as ready")
	}
}

func TestParseOpenCodeInteractiveResponseRejectsQueuedValidationEcho(t *testing.T) {
	prompt := "## Pre-validation failed (retry attempt 3)"
	baseline := "OpenCode\nAsk anything\n"
	captured := baseline + `
> ## Pre-validation failed (retry attempt 3)

❌ PRE-VALIDATION FAILED

Checks: 0 passed, 1 failed

Fix the specific issues above and re-produce the required outputs.

Ask anything
ctrl+p commands opencode`

	got := parseOpenCodeInteractiveResponse(captured, baseline, prompt, nil)
	if got != "" {
		t.Fatalf("parsed queued validation echo = %q, want empty", got)
	}
}

func TestOpenCodeCLIRealInteractiveTmuxContract(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)
	t.Cleanup(func() { _ = CleanupOpenCodeCLIInteractiveSessions(context.Background()) })

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	ownerSessionID := "opencode-real-contract-" + opencodeRandomHex(4)
	token := "REAL_OPENCODE_TMUX_" + opencodeRandomHex(4)
	workingDir := t.TempDir()
	stream := make(chan llmtypes.StreamChunk, 64)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: strings.Repeat(
				"You are testing the OpenCode CLI tmux transport. Keep exact-token replies concise.\n",
				20,
			)}},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf(`This is a real OpenCode CLI tmux contract test.

Preserve this input safely:

JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN

Reply exactly:
saved %s`, token, token)}},
		},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workingDir),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("content = %q, want token %s", content, token)
	}
	assertOpenCodeTerminalOnlyStream(t, stream)

	tmuxSession, ok := activeOpenCodeInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active OpenCode tmux session for %s", ownerSessionID)
	}
	pane, err := captureOpenCodePane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture OpenCode pane: %v", err)
	}
	if !hasOpenCodeReadyPrompt(pane) {
		t.Fatalf("real OpenCode TUI ready prompt not detected; pane:\n%s", pane)
	}
	if hasOpenCodeActivity(pane) {
		t.Fatalf("real OpenCode TUI should be idle after first turn; pane:\n%s", pane)
	}
}

func requireRealOpenCodeCLIE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_OPENCODE_CLI_REAL_E2E") == "" {
		t.Skip("set RUN_OPENCODE_CLI_REAL_E2E=1 to run real OpenCode CLI tmux contract tests")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("real OpenCode CLI tests require tmux in PATH: %v", err)
	}
	if _, err := opencodeBinaryPath(); err != nil {
		t.Fatalf("real OpenCode CLI tests require opencode: %v", err)
	}
}

func assertOpenCodeTerminalOnlyStream(t *testing.T, stream <-chan llmtypes.StreamChunk) {
	t.Helper()
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return
			}
			if chunk.Type != llmtypes.StreamChunkTypeTerminal {
				t.Fatalf("unexpected stream chunk type %q: %#v", chunk.Type, chunk)
			}
		default:
			return
		}
	}
}
