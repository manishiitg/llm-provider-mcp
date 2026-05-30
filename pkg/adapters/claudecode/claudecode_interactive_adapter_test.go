package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxlaunch"
)

func TestClaudeInteractiveStreamTmuxScreenFlag(t *testing.T) {
	t.Setenv(EnvClaudeExperimentalStreamTmuxScreen, "")
	if !claudeInteractiveStreamTmuxScreenEnabled() {
		t.Fatal("tmux screen streaming should be enabled by default")
	}

	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(EnvClaudeExperimentalStreamTmuxScreen, value)
		if !claudeInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be enabled for %q", value)
		}
	}

	for _, value := range []string{"0", "false", "FALSE", "no", "off"} {
		t.Setenv(EnvClaudeExperimentalStreamTmuxScreen, value)
		if claudeInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be disabled for %q", value)
		}
	}
}

func TestClaudeTerminalStreamCapturesRawScreenRows(t *testing.T) {
	fakeBin := t.TempDir()
	argsPath := fakeBin + "/capture-args.log"
	tmuxPath := fakeBin + "/tmux"
	script := `#!/bin/sh
if [ "$1" = "capture-pane" ]; then
  printf '%s\n' "$*" > "$TMUX_TEST_CAPTURE_ARGS"
  for arg in "$@"; do
    if [ "$arg" = "-J" ]; then
      echo "terminal display capture must not use -J" >&2
      exit 9
    fi
  done
  printf 'screen row one\nscreen row two\n'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_CAPTURE_ARGS", argsPath)

	stream := make(chan llmtypes.StreamChunk, 1)
	var last string
	if !streamClaudeTerminalSnapshot(context.Background(), "raw-display-session", stream, &last) {
		t.Fatal("streamClaudeTerminalSnapshot returned false")
	}
	chunk := <-stream
	if chunk.Type != llmtypes.StreamChunkTypeTerminal {
		t.Fatalf("chunk type = %q, want terminal", chunk.Type)
	}
	if !strings.Contains(chunk.Content, "screen row one\nscreen row two") {
		t.Fatalf("chunk content = %q, want raw screen rows", chunk.Content)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read capture args: %v", err)
	}
	if strings.Contains(string(args), " -J") {
		t.Fatalf("terminal display capture used joined rows: %q", string(args))
	}
}

func TestClaudeStartSessionDisablesPromptSuggestions(t *testing.T) {
	got := claudePromptSuggestionEnvArgs()
	want := []string{
		"-e", "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false",
		"-e", "ANTHROPIC_API_KEY=",
		"-e", "ANTHROPIC_BASE_URL=",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claude prompt suggestion env args = %v, want %v", got, want)
	}
}

func TestClaudeSubmitPromptKeysMoveToEndBeforeEnter(t *testing.T) {
	got := claudeSubmitPromptKeys()
	want := []string{"C-e", "Enter"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claude submit keys = %v, want %v", got, want)
	}
}

func TestClaudeInteractiveShellCommandUsesCallerWorkingDir(t *testing.T) {
	shell := writeExecutableTestShell(t, "zsh")
	t.Setenv("CODING_AGENT_LOGIN_SHELL", shell)
	t.Setenv("CODING_AGENT_SHELL_MODE", "")

	got := claudeInteractiveShellCommand([]string{"claude", "--system-prompt-file", "/tmp/sys.md"}, "/tmp/user chat")
	if !strings.HasPrefix(got, "'"+shell+"' '-ilc' ") {
		t.Fatalf("shell command = %q, want login shell prefix", got)
	}
	if !strings.Contains(got, "'/tmp/user chat'") {
		t.Fatalf("shell command = %q, want caller cwd passed to login shell", got)
	}
}

func TestClaudeInteractiveShellCommandDirectMode(t *testing.T) {
	t.Setenv("CODING_AGENT_SHELL_MODE", "direct")

	got := claudeInteractiveShellCommand([]string{"claude", "--system-prompt-file", "/tmp/sys.md"}, "/tmp/user chat")
	if !strings.HasPrefix(got, "cd '/tmp/user chat' && exec ") {
		t.Fatalf("shell command = %q, want direct cwd before exec", got)
	}
}

func writeExecutableTestShell(t *testing.T, name string) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write shell: %v", err)
	}
	return path
}

func TestTmuxBuildClaudeArgsDefaultsToNoInternalTools(t *testing.T) {
	adapter := NewClaudeCodeInteractiveAdapter("claude-code", &MockLogger{})
	args, tempFiles, err := adapter.buildClaudeArgs(&llmtypes.CallOptions{}, "", "7aa21987-0003-4d71-b887-ad73e29d2faf", "")
	if err != nil {
		t.Fatalf("buildClaudeArgs error = %v", err)
	}
	if len(tempFiles) != 0 {
		t.Fatalf("tempFiles = %v, want none", tempFiles)
	}

	if containsArg(args, "-p") || containsArg(args, "--print") {
		t.Fatalf("experimental args must not include print mode: %v", args)
	}
	if !containsArgPair(args, "--tools", "") {
		t.Fatalf("args = %v, want --tools empty string to disable internal tools", args)
	}
	if !containsArgPair(args, "--permission-mode", "dontAsk") {
		t.Fatalf("args = %v, want permission mode dontAsk", args)
	}
	if !containsArgPair(args, "--session-id", "7aa21987-0003-4d71-b887-ad73e29d2faf") {
		t.Fatalf("args = %v, want caller-provided native session id", args)
	}
	if !containsArg(args, "--name") {
		t.Fatalf("args = %v, want display name for Claude resume picker", args)
	}
	if containsArg(args, "--system-prompt") {
		t.Fatalf("args = %v, should not pass empty system prompt", args)
	}
}

func TestTmuxRejectsImageContent(t *testing.T) {
	adapter := NewClaudeCodeInteractiveAdapter("claude-code", &MockLogger{})

	_, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Describe this image."},
				llmtypes.ImageContent{SourceType: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="},
			},
		},
	})
	if err == nil {
		t.Fatal("GenerateContent() error = nil, want unsupported image content error")
	}
	if !strings.Contains(err.Error(), "does not support llmtypes.ImageContent") {
		t.Fatalf("GenerateContent() error = %v, want image content unsupported error", err)
	}
}

func TestClaudeInteractiveSessionsFromTmuxListOnlyMatchesAdapterPrefix(t *testing.T) {
	out := strings.Join([]string{
		"mlp-claude-code-exp-111-aaaa",
		"user-work",
		"mlp-claude-code-experimental-other",
		"mlp-claude-code-exp",
		"mlp-claude-code-exp2-222-bbbb",
	}, "\n")

	got := claudeInteractiveSessionsFromTmuxList(out, "mlp-claude-code-exp")
	want := []string{
		"mlp-claude-code-exp-111-aaaa",
		"mlp-claude-code-exp",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
}

func TestClaudeTmuxSessionLostErrorDetection(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil},
		{name: "no server", err: errors.New("failed to capture Claude Code tmux session: exit status 1: no server running on /private/tmp/tmux-501/default"), want: true},
		{name: "missing pane", err: errors.New("failed to capture Claude Code tmux session: exit status 1: can't find pane: mlp-claude-code-1"), want: true},
		{name: "missing session", err: errors.New("tmux kill-session failed: can't find session: mlp-claude-code-exp-1"), want: true},
		{name: "ordinary timeout", err: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClaudeTmuxSessionLostError(tt.err); got != tt.want {
				t.Fatalf("isClaudeTmuxSessionLostError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestTmuxBuildClaudeArgsPassesBridgeOptions(t *testing.T) {
	adapter := NewClaudeCodeInteractiveAdapter("claude-sonnet-4-6", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"/tmp/mcpbridge"}}}`)(opts)
	WithClaudeCodeTools("WebSearch")(opts)
	WithAllowedTools("mcp__api-bridge__*,WebSearch")(opts)
	WithResumeSessionID("7aa21987-0003-4d71-b887-ad73e29d2faf")(opts)
	WithEffort("high")(opts)

	args, tempFiles, err := adapter.buildClaudeArgs(opts, "", "7aa21987-0003-4d71-b887-ad73e29d2faf", "native system prompt")
	if err != nil {
		t.Fatalf("buildClaudeArgs error = %v", err)
	}
	defer removeFiles(tempFiles)

	if !containsArgPair(args, "--tools", "WebSearch") {
		t.Fatalf("args = %v, want --tools WebSearch", args)
	}
	if !containsArgPair(args, "--allowed-tools", "mcp__api-bridge__*,WebSearch") {
		t.Fatalf("args = %v, want allowed MCP bridge tools", args)
	}
	if !containsArg(args, "--strict-mcp-config") {
		t.Fatalf("args = %v, want --strict-mcp-config", args)
	}
	if !containsArgPair(args, "--model", "claude-sonnet-4-6") {
		t.Fatalf("args = %v, want model override", args)
	}
	if !containsArgPair(args, "--resume", "7aa21987-0003-4d71-b887-ad73e29d2faf") {
		t.Fatalf("args = %v, want resume session id", args)
	}
	if containsArg(args, "--session-id") {
		t.Fatalf("args = %v, resumed invocation must use --resume instead of starting a new session id", args)
	}
	if !containsArgPair(args, "--effort", "high") {
		t.Fatalf("args = %v, want effort", args)
	}
	systemPromptPath := argValue(args, "--system-prompt-file")
	if systemPromptPath == "" {
		t.Fatalf("args = %v, want native --system-prompt-file", args)
	}
	systemPromptBytes, err := os.ReadFile(systemPromptPath)
	if err != nil {
		t.Fatalf("read system prompt file: %v", err)
	}
	if string(systemPromptBytes) != "native system prompt" {
		t.Fatalf("system prompt file = %q, want native system prompt", string(systemPromptBytes))
	}
	if containsArg(args, "--system-prompt") {
		t.Fatalf("args = %v, system prompt must be passed by file to avoid command length limits", args)
	}
	if containsArg(args, "--append-system-prompt") {
		t.Fatalf("args = %v, tmux mode should replace with --system-prompt, not append", args)
	}
	if len(tempFiles) != 2 {
		t.Fatalf("tempFiles = %v, want MCP config and system prompt temp files", tempFiles)
	}
}

func TestSplitSystemPromptRemovesSystemMessagesFromConversation(t *testing.T) {
	systemPrompt, conversation := splitSystemPrompt([]llmtypes.MessageContent{
		textPartMessage(llmtypes.ChatMessageTypeSystem, "SYSTEM ONE"),
		textPartMessage(llmtypes.ChatMessageTypeHuman, "hello"),
		textPartMessage(llmtypes.ChatMessageTypeSystem, "SYSTEM TWO"),
		textPartMessage(llmtypes.ChatMessageTypeAI, "hi"),
	})

	if systemPrompt != "SYSTEM ONE\n\nSYSTEM TWO" {
		t.Fatalf("systemPrompt = %q, want joined system prompts", systemPrompt)
	}
	if len(conversation) != 2 {
		t.Fatalf("conversation len = %d, want 2", len(conversation))
	}
	for _, msg := range conversation {
		if msg.Role == llmtypes.ChatMessageTypeSystem {
			t.Fatalf("conversation contains system message: %+v", msg)
		}
	}
}

func TestBuildTmuxPromptResumeSendsOnlyLatestHumanMessage(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithResumeSessionID("7aa21987-0003-4d71-b887-ad73e29d2faf")(opts)

	_, conversation := splitSystemPrompt([]llmtypes.MessageContent{
		textPartMessage(llmtypes.ChatMessageTypeSystem, "SYSTEM_SHOULD_BE_NATIVE"),
		textPartMessage(llmtypes.ChatMessageTypeHuman, "OLD_USER_SHOULD_NOT_BE_SENT"),
		textPartMessage(llmtypes.ChatMessageTypeAI, "OLD_ASSISTANT_SHOULD_NOT_BE_SENT"),
		textPartMessage(llmtypes.ChatMessageTypeHuman, "LATEST_USER_SHOULD_BE_SENT"),
	})
	prompt, err := buildTmuxPrompt(conversation, opts, claudeResumeIDFromOptions(opts), false)
	if err != nil {
		t.Fatalf("buildTmuxPrompt error = %v", err)
	}
	if !strings.Contains(prompt, "LATEST_USER_SHOULD_BE_SENT") {
		t.Fatalf("prompt = %q, want latest user message", prompt)
	}
	if strings.TrimSpace(prompt) != "LATEST_USER_SHOULD_BE_SENT" {
		t.Fatalf("resume prompt = %q, want only latest user message with no adapter wrapper", prompt)
	}
	if strings.Contains(prompt, "OLD_USER_SHOULD_NOT_BE_SENT") || strings.Contains(prompt, "OLD_ASSISTANT_SHOULD_NOT_BE_SENT") {
		t.Fatalf("resume prompt should not replay old conversation: %q", prompt)
	}
	if strings.Contains(prompt, "SYSTEM_SHOULD_BE_NATIVE") || strings.Contains(prompt, "SYSTEM:") {
		t.Fatalf("tmux prompt should not contain system messages; they are passed via --system-prompt: %q", prompt)
	}
	for _, forbidden := range []string{
		"tmux adapter",
		"Final answer format",
		"Start marker",
		"End marker",
		"MLP_CLAUDE_EXPERIMENTAL",
		"GitHub-flavored Markdown",
		"Markdown tables",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt contains adapter instruction %q: %q", forbidden, prompt)
		}
	}
}

func TestBuildTmuxPromptPersistentSendsOnlyLatestHumanMessage(t *testing.T) {
	_, conversation := splitSystemPrompt([]llmtypes.MessageContent{
		textPartMessage(llmtypes.ChatMessageTypeSystem, "SYSTEM_SHOULD_BE_NATIVE"),
		textPartMessage(llmtypes.ChatMessageTypeHuman, "OLD_USER_SHOULD_NOT_BE_SENT"),
		textPartMessage(llmtypes.ChatMessageTypeAI, "OLD_ASSISTANT_SHOULD_NOT_BE_SENT"),
		textPartMessage(llmtypes.ChatMessageTypeHuman, "LATEST_USER_SHOULD_BE_SENT"),
	})

	prompt, err := buildTmuxPrompt(conversation, &llmtypes.CallOptions{}, "", true)
	if err != nil {
		t.Fatalf("buildTmuxPrompt error = %v", err)
	}
	if strings.TrimSpace(prompt) != "LATEST_USER_SHOULD_BE_SENT" {
		t.Fatalf("persistent prompt = %q, want only latest user message", prompt)
	}
	if strings.Contains(prompt, "OLD_USER_SHOULD_NOT_BE_SENT") || strings.Contains(prompt, "OLD_ASSISTANT_SHOULD_NOT_BE_SENT") {
		t.Fatalf("persistent prompt should not replay old conversation: %q", prompt)
	}
	if strings.Contains(prompt, "SYSTEM_SHOULD_BE_NATIVE") || strings.Contains(prompt, "Previous conversation context") {
		t.Fatalf("persistent prompt should not contain native system/context wrappers: %q", prompt)
	}
}

func TestBuildTmuxPromptFreshSingleTurnSendsOnlyUserText(t *testing.T) {
	prompt, err := buildTmuxPrompt(
		[]llmtypes.MessageContent{
			textPartMessage(llmtypes.ChatMessageTypeHuman, "hi"),
		},
		&llmtypes.CallOptions{},
		"",
		false,
	)
	if err != nil {
		t.Fatalf("buildTmuxPrompt error = %v", err)
	}
	if strings.TrimSpace(prompt) != "hi" {
		t.Fatalf("fresh single-turn prompt = %q, want only user text", prompt)
	}
	for _, forbidden := range []string{"Conversation:", "HUMAN:", "User message:"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("fresh single-turn prompt contains wrapper %q: %q", forbidden, prompt)
		}
	}
}

func TestBuildTmuxPromptFreshSingleTurnPreservesUserTextVerbatim(t *testing.T) {
	userText := strings.Join([]string{
		"hi",
		"",
		"line with punctuation: !@#$%^&*()_+-=[]{}|;:'\",.<>/?`~",
		"unicode: नमस्ते こんにちは Привет café 🚀",
		"shell-looking text that must not execute: $(echo nope) && rm -rf /",
		"json-looking text: {\"message\":\"hello\\nworld\",\"ok\":true}",
		"role-looking text:",
		"HUMAN:",
		"Assistant:",
	}, "\n")

	prompt, err := buildTmuxPrompt(
		[]llmtypes.MessageContent{
			textPartMessage(llmtypes.ChatMessageTypeHuman, userText),
		},
		&llmtypes.CallOptions{},
		"",
		false,
	)
	if err != nil {
		t.Fatalf("buildTmuxPrompt error = %v", err)
	}
	if strings.TrimSuffix(prompt, "\n") != userText {
		t.Fatalf("fresh single-turn prompt did not preserve user text verbatim:\ngot  %q\nwant %q", prompt, userText)
	}
}

func TestBuildTmuxPromptHandlesJSONRehydratedTextParts(t *testing.T) {
	raw := []byte(`{
		"conversation_history": [
			{"Role":"human","Parts":[{"Text":"hi"}]},
			{"Role":"ai","Parts":[{"Text":"hello"}]},
			{"Role":"human","Parts":[{"text":"next"}]}
		]
	}`)
	var saved struct {
		History []llmtypes.MessageContent `json:"conversation_history"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	if got := tmuxMessagePartsToText(saved.History[0].Parts); got != "hi" {
		t.Fatalf("tmuxMessagePartsToText saved first human = %q, want hi", got)
	}

	prompt, err := buildTmuxPrompt(saved.History, &llmtypes.CallOptions{}, "", false)
	if err != nil {
		t.Fatalf("buildTmuxPrompt error = %v", err)
	}
	for _, want := range []string{"Previous conversation context:", "User:\nhi", "Assistant:\nhello", "Current user message:\nnext"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, want %q", prompt, want)
		}
	}
	if strings.Contains(prompt, "HUMAN:") || strings.Contains(prompt, "AI:") || strings.Contains(prompt, "Conversation:") {
		t.Fatalf("prompt contains old role wrapper: %q", prompt)
	}
	if strings.Contains(prompt, "[unsupported content part") {
		t.Fatalf("prompt contains unsupported marker: %q", prompt)
	}
}

func TestBuildTmuxPromptResumeHandlesJSONRehydratedLatestHuman(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithResumeSessionID("7aa21987-0003-4d71-b887-ad73e29d2faf")(opts)

	raw := []byte(`{
		"conversation_history": [
			{"Role":"human","Parts":[{"Text":"old"}]},
			{"Role":"ai","Parts":[{"Text":"previous"}]},
			{"Role":"human","Parts":[{"Text":"hi"}]}
		]
	}`)
	var saved struct {
		History []llmtypes.MessageContent `json:"conversation_history"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}

	prompt, err := buildTmuxPrompt(saved.History, opts, claudeResumeIDFromOptions(opts), false)
	if err != nil {
		t.Fatalf("buildTmuxPrompt error = %v", err)
	}
	if strings.TrimSpace(prompt) != "hi" {
		t.Fatalf("resume prompt = %q, want only latest rehydrated human text", prompt)
	}
}

func TestExtractBetweenLastMarkersCleansClaudeUIPrefix(t *testing.T) {
	pane := `
❯ prompt

⏺ MLP_START
  first line
  second line
  MLP_END
`

	got, ok := extractBetweenLastMarkers(pane, "MLP_START", "MLP_END")
	if !ok {
		t.Fatal("extractBetweenLastMarkers ok = false, want true")
	}
	want := "first line\nsecond line"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestExtractBetweenLastMarkersRejectsClaudeComposerArtifact(t *testing.T) {
	pane := `
MLP_START
<final answer>

✶ Composing…


❯
MLP_END
`
	got, ok := extractBetweenLastMarkers(pane, "MLP_START", "MLP_END")
	if ok {
		t.Fatalf("extractBetweenLastMarkers returned artifact %q, want ok=false", got)
	}
}

func TestCapturedAfterPaneBaselineIgnoresStaleResumeOutput(t *testing.T) {
	baseline := `
⏺ RESPONSE_START_old
  SAVED
  RESPONSE_END_old

─────────────────────────────────────────────────── mcp-agent-old ──
❯
`
	captured := baseline + `
User:
What codeword did I ask you to remember?

⏺ RESPONSE_START_new
  RESUME_ZINC_4821
  RESPONSE_END_new
`

	delta := capturedAfterPaneBaseline(captured, baseline)
	if strings.Contains(delta, "RESPONSE_START_old") || strings.Contains(delta, "SAVED") {
		t.Fatalf("delta includes stale resume output: %q", delta)
	}
	got, ok := extractBetweenLastMarkers(delta, "RESPONSE_START_new", "RESPONSE_END_new")
	if !ok {
		t.Fatalf("extractBetweenLastMarkers delta ok = false; delta=%q", delta)
	}
	if got != "RESUME_ZINC_4821" {
		t.Fatalf("content = %q, want RESUME_ZINC_4821", got)
	}
}

func TestExtractLatestUnmarkedAssistantResponse(t *testing.T) {
	pane := `
Conversation:

HUMAN:
Reply exactly USER_MESSAGE_WRONG.

⏺ NATIVE_SYSTEM_PROMPT_OK

✻ Worked for 1s

─────────────────────────────────────────────────── mcp-agent-20260514-171717 ──
❯
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	got, ok := extractLatestUnmarkedAssistantResponse(pane)
	if !ok {
		t.Fatal("extractLatestUnmarkedAssistantResponse ok = false, want true")
	}
	if got != "NATIVE_SYSTEM_PROMPT_OK" {
		t.Fatalf("content = %q, want NATIVE_SYSTEM_PROMPT_OK", got)
	}
}

func TestExtractLatestUnmarkedAssistantResponseMultiline(t *testing.T) {
	pane := `
⏺ First answer

Called api-bridge

⏺ Here is the answer:
  - one
  - two

✻ Worked for 2s
❯
`
	got, ok := extractLatestUnmarkedAssistantResponse(pane)
	if !ok {
		t.Fatal("extractLatestUnmarkedAssistantResponse ok = false, want true")
	}
	want := "Here is the answer:\n- one\n- two"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestExtractLatestUnmarkedAssistantResponseSkipsTrailingToolBlock(t *testing.T) {
	pane := `
⏺ Here's the full summary:
  - done
  - verified

✻ Worked for 2s

⏺ api-bridge - execute_shell_command (MCP)(command: "cat file")
  ⎿  {"stdout":"ok"}

─────────────────────────────────────────────────── mcp-agent-20260519 ──
❯
`
	got, ok := extractLatestUnmarkedAssistantResponse(pane)
	if !ok {
		t.Fatal("extractLatestUnmarkedAssistantResponse ok = false, want true")
	}
	want := "Here's the full summary:\n- done\n- verified"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	testcontracts.AssertCleanFinalExtraction(t, "claude-code", got,
		[]string{"Here's the full summary:", "- done", "- verified"},
		[]string{
			"api-bridge",
			"execute_shell_command",
			"stdout",
			"ctrl+o",
			"mcp-agent-20260519",
			"❯",
		},
	)
}

func TestClaudeFinalExtractionVertexJudgeE2E(t *testing.T) {
	pane := `
⏺ Here's the full summary:
  - done
  - verified

✻ Worked for 2s

⏺ api-bridge - execute_shell_command (MCP)(command: "cat file")
  ⎿  {"stdout":"ok"}

─────────────────────────────────────────────────── mcp-agent-20260519 ──
❯
`
	got, ok := extractLatestUnmarkedAssistantResponse(pane)
	if !ok {
		t.Fatal("extractLatestUnmarkedAssistantResponse ok = false, want true")
	}
	testcontracts.AssertVertexJudgesFinalExtraction(t, testcontracts.FinalExtractionJudgeCase{
		Provider:   "claude-code",
		TmuxScreen: pane,
		Extracted:  got,
		UserGoal:   "Provide a concise summary of completed work.",
		MustContain: []string{
			"Here's the full summary:",
			"- done",
			"- verified",
		},
		Forbidden: []string{
			"api-bridge",
			"execute_shell_command",
			"stdout",
			"mcp-agent-20260519",
			"❯",
		},
		ExpectedNote: "The final answer is a heading followed by two bullet lines.",
	})
}

func TestExtractLatestUnmarkedAssistantResponseCanStillStripMarkers(t *testing.T) {
	startMarker := "RESPONSE_START_abc"
	endMarker := "RESPONSE_END_def"
	pane := `
⏺ RESPONSE_START_abc
  final text
  RESPONSE_END_def

✻ Worked for 1s
❯
`
	visible, ok := extractLatestUnmarkedAssistantResponse(pane)
	if !ok {
		t.Fatal("extractLatestUnmarkedAssistantResponse ok = false, want true")
	}
	got, ok := extractBetweenLastMarkers(visible, startMarker, endMarker)
	if !ok {
		t.Fatalf("extractBetweenLastMarkers(%q) ok = false, want true", visible)
	}
	if got != "final text" {
		t.Fatalf("content = %q, want final text", got)
	}
}

func TestTruncateClaudePaneForError(t *testing.T) {
	pane := strings.Repeat("x", 5000)
	got := truncateClaudePaneForError(pane)
	if len([]rune(got)) >= len([]rune(pane)) {
		t.Fatal("truncateClaudePaneForError did not shorten large pane")
	}
	if !strings.Contains(got, "truncated to last") {
		t.Fatalf("truncate notice missing: %q", got[:80])
	}
}

func TestExtractTrailingUnmarkedAssistantResponseFromScrolledPaneTail(t *testing.T) {
	pane := `
  │ 3        │ meme-reaction    │ 2-panel meme format) │
  └──────────┴──────────────────┴──────────────────────┘

  The images look good visually, but they're not hitting the specific format
  templates (tweet chrome, warning box, 2-panel meme).

  STATUS: COMPLETED
✻ Sautéed for 8s
─────────────────────────────────────────────────── mcp-agent-20260515-084248 ──
❯
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle) tmux focus-events off · add 'set -g focus-events on' to ~/.tmux.conf
`
	got, ok := extractTrailingUnmarkedAssistantResponse(pane)
	if !ok {
		t.Fatal("extractTrailingUnmarkedAssistantResponse ok = false, want true")
	}
	if !strings.Contains(got, "The images look good visually") || !strings.Contains(got, "STATUS: COMPLETED") {
		t.Fatalf("content = %q, want trailing assistant text", got)
	}
	if strings.Contains(got, "focus-events") || strings.Contains(got, "mcp-agent-20260515") || strings.Contains(got, "❯") {
		t.Fatalf("content leaked TUI footer: %q", got)
	}
}

func TestExtractTailAssistantTextFallbackSkipsClaudeChrome(t *testing.T) {
	pane := `╭─── Claude Code v2.1.144 ───╮
❯ user prompt echo
  Called api-bridge 2 times (ctrl+o to expand)
  This is the recovered final answer.
  It spans two lines.
✻ Churned for 2s
❯`

	got, ok := extractTailAssistantTextFallback(pane, 24)
	if !ok {
		t.Fatal("extractTailAssistantTextFallback ok = false, want true")
	}
	want := "This is the recovered final answer.\nIt spans two lines."
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestParseClaudeResponseFromCapturedFallbackPreservesLongFinal(t *testing.T) {
	finalLines := make([]string, 0, 40)
	for i := 1; i <= 40; i++ {
		finalLines = append(finalLines, fmt.Sprintf("LONG_FINAL_LINE_%02d CUT_OFF_SENTINEL_%02d", i, i))
	}
	pane := `╭─── Claude Code v2.1.144 ───╮
❯ user prompt echo
  Called api-bridge 2 times (ctrl+o to expand)
  ` + strings.Join(finalLines, "\n  ") + `
⎿ Compacted history (ctrl+o for full summary)
─────────────────────────────────────────────────── mcp-agent-20260526 ──
❯`

	got, ok := parseClaudeResponseFromCaptured(pane, "", "", "")
	if !ok {
		t.Fatal("parseClaudeResponseFromCaptured ok = false, want true")
	}
	for _, want := range finalLines {
		if !strings.Contains(got, want) {
			t.Fatalf("content missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "api-bridge") || strings.Contains(got, "ctrl+o") || strings.Contains(got, "mcp-agent-20260526") || strings.Contains(got, "❯") {
		t.Fatalf("content leaked Claude chrome/tool output:\n%s", got)
	}
	if gotLines := strings.Split(got, "\n"); len(gotLines) != len(finalLines) {
		t.Fatalf("line count = %d, want %d; content:\n%s", len(gotLines), len(finalLines), got)
	}
}

func TestParseClaudeResumeSessionID(t *testing.T) {
	pane := `
Resume this session with:
claude --resume 7aa21987-0003-4d71-b887-ad73e29d2faf
Pane is dead (status 0)
`
	got := parseClaudeResumeSessionID(pane)
	want := "7aa21987-0003-4d71-b887-ad73e29d2faf"
	if got != want {
		t.Fatalf("parseClaudeResumeSessionID = %q, want %q", got, want)
	}
}

func TestIsClaudeResumeCompressionPrompt(t *testing.T) {
	pane := `
This resumed conversation is large.
Would you like to compact the conversation or continue without compacting?
`
	if !isClaudeResumeCompressionPrompt(pane) {
		t.Fatal("isClaudeResumeCompressionPrompt = false, want true")
	}
	got := claudeResumeCompressionPromptSubmitKeys(pane)
	want := []string{"continue", "C-m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claudeResumeCompressionPromptSubmitKeys = %#v, want %#v", got, want)
	}
}

func TestClaudeResumeSummaryMenuSubmitsDefaultChoice(t *testing.T) {
	pane := `
────────────────────────────────────────────────────────────────────────────────────────────────── mcp-agent ──

────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  This session is 2h 17m old and 167.9k tokens.

  Resuming the full session will consume a substantial portion of your usage limits. We recommend resuming from a summary.

  ❯ 1. Resume from summary (recommended)
    2. Resume full session as-is
    3. Don't ask me again

  Enter to confirm · Esc to cancel
`
	if !isClaudeResumeSummaryMenu(pane) {
		t.Fatal("isClaudeResumeSummaryMenu = false, want true")
	}
	got := claudeResumeCompressionPromptSubmitKeys(pane)
	want := []string{"C-m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claudeResumeCompressionPromptSubmitKeys = %#v, want %#v", got, want)
	}
}

func TestIsClaudeResumeCompressionPromptIgnoresNormalPrompt(t *testing.T) {
	pane := `
─────────────────────────────────────────────────── mcp-agent ──
❯
`
	if isClaudeResumeCompressionPrompt(pane) {
		t.Fatal("isClaudeResumeCompressionPrompt = true for normal prompt")
	}
	if isClaudeResumeSummaryMenu(pane) {
		t.Fatal("isClaudeResumeSummaryMenu = true for normal prompt")
	}
	if got := claudeResumeCompressionPromptSubmitKeys(pane); got != nil {
		t.Fatalf("claudeResumeCompressionPromptSubmitKeys = %#v, want nil", got)
	}
}

func TestIsClaudeResumeSelectMenuCursorOnCompact(t *testing.T) {
	// ❯ is on the compact option — adapter should navigate down to "run as is".
	pane := `
──────────────────────────────────────────────────────── mcp-agent ──

  Your conversation history is using a substantial portion of the context window.

  ❯ Compact conversation (recommended)
    Continue without compacting

  ↑↓ to navigate · Enter to select
`
	if !isClaudeResumeSelectMenu(pane) {
		t.Fatal("isClaudeResumeSelectMenu = false, want true")
	}
	got := claudeResumeCompressionPromptSubmitKeys(pane)
	want := []string{"Down", "C-m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claudeResumeCompressionPromptSubmitKeys = %#v, want %#v", got, want)
	}
}

func TestIsClaudeResumeSelectMenuCursorOnRunAsIs(t *testing.T) {
	// ❯ is already on the "run as is" option — Enter is enough.
	pane := `
──────────────────────────────────────────────────────── mcp-agent ──

  Your conversation history is using a substantial portion of the context window.

    Compact conversation
  ❯ Continue without compacting

  ↑↓ to navigate · Enter to select
`
	if !isClaudeResumeSelectMenu(pane) {
		t.Fatal("isClaudeResumeSelectMenu = false, want true")
	}
	got := claudeResumeCompressionPromptSubmitKeys(pane)
	want := []string{"C-m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claudeResumeCompressionPromptSubmitKeys = %#v, want %#v", got, want)
	}
}

func TestIsClaudeResumeSelectMenuRunAsIsVariant(t *testing.T) {
	// "run as is" phrasing instead of "continue without compacting".
	pane := `
──────────────────────────────────────────────────────── mcp-agent ──

  ❯ Compact and summarize
    Run as is

  ↑↓ to navigate · Enter to select
`
	if !isClaudeResumeSelectMenu(pane) {
		t.Fatal("isClaudeResumeSelectMenu = false for run-as-is variant")
	}
	got := claudeResumeCompressionPromptSubmitKeys(pane)
	want := []string{"Down", "C-m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claudeResumeCompressionPromptSubmitKeys = %#v, want %#v", got, want)
	}
}

func TestIsClaudeResumeSelectMenuDoesNotMatchNormalPrompt(t *testing.T) {
	pane := `
─────────────────────────────────────────────────── mcp-agent ──
❯
`
	if isClaudeResumeSelectMenu(pane) {
		t.Fatal("isClaudeResumeSelectMenu = true for normal ready prompt")
	}
}

func TestIsClaudeResumeSelectMenuDoesNotMatchSummaryMenu(t *testing.T) {
	// The old summary menu has ❯ but no compact/compress option — must not match.
	pane := `
────────────────────────────────────────────────────────────────────────────────────────────────── mcp-agent ──

  This session is 2h 17m old and 167.9k tokens.

  Resuming the full session will consume a substantial portion of your usage limits. We recommend resuming from a summary.

  ❯ 1. Resume from summary (recommended)
    2. Resume full session as-is
    3. Don't ask me again

  Enter to confirm · Esc to cancel
`
	if isClaudeResumeSelectMenu(pane) {
		t.Fatal("isClaudeResumeSelectMenu = true for summary menu (no compact option)")
	}
}

func TestOldTextBasedCompressionPromptStillHandled(t *testing.T) {
	// The older text-based format (no ❯ cursor) must still route to the
	// text-input path, not the new TUI select-menu path.
	pane := `
This resumed conversation is large.
Would you like to compact the conversation or continue without compacting?
`
	if isClaudeResumeSelectMenu(pane) {
		t.Fatal("isClaudeResumeSelectMenu = true for old text-based prompt (no ❯)")
	}
	if !isClaudeResumeCompressionPrompt(pane) {
		t.Fatal("isClaudeResumeCompressionPrompt = false for old text-based prompt")
	}
	got := claudeResumeCompressionPromptSubmitKeys(pane)
	want := []string{"continue", "C-m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claudeResumeCompressionPromptSubmitKeys = %#v, want %#v", got, want)
	}
}

func TestTmuxTimeoutEnv(t *testing.T) {
	t.Setenv(EnvClaudeExperimentalTimeoutSeconds, "")
	if got := tmuxTimeout(); got != 0 {
		t.Fatalf("tmuxTimeout default = %v, want 0", got)
	}

	t.Setenv(EnvClaudeExperimentalTimeoutSeconds, "0")
	if got := tmuxTimeout(); got != 0 {
		t.Fatalf("tmuxTimeout zero env = %v, want 0", got)
	}

	t.Setenv(EnvClaudeExperimentalTimeoutSeconds, "2")
	if got := tmuxTimeout(); got != 2*time.Second {
		t.Fatalf("tmuxTimeout = %v, want 2s", got)
	}

	t.Setenv(EnvClaudeExperimentalTimeoutSeconds, "bad")
	if got := tmuxTimeout(); got != defaultTmuxTimeout {
		t.Fatalf("tmuxTimeout invalid env = %v, want default %v", got, defaultTmuxTimeout)
	}
}

func TestTmuxPromptWaitEnv(t *testing.T) {
	t.Setenv(tmuxlaunch.EnvPromptWaitSeconds, "")
	t.Setenv(EnvClaudeExperimentalPromptWaitSeconds, "")
	if got := promptReadyTimeout(); got != 120*time.Second {
		t.Fatalf("promptReadyTimeout default = %v, want 120s", got)
	}

	t.Setenv(tmuxlaunch.EnvPromptWaitSeconds, "3")
	t.Setenv(EnvClaudeExperimentalPromptWaitSeconds, "")
	if got := promptReadyTimeout(); got != 3*time.Second {
		t.Fatalf("promptReadyTimeout global env = %v, want 3s", got)
	}

	t.Setenv(EnvClaudeExperimentalPromptWaitSeconds, "2")
	if got := promptReadyTimeout(); got != 2*time.Second {
		t.Fatalf("promptReadyTimeout provider env = %v, want 2s", got)
	}

	t.Setenv(EnvClaudeExperimentalPromptWaitSeconds, "0")
	if got := promptReadyTimeout(); got != 3*time.Second {
		t.Fatalf("promptReadyTimeout zero provider env = %v, want global 3s", got)
	}

	t.Setenv(EnvClaudeExperimentalPromptWaitSeconds, "bad")
	if got := promptReadyTimeout(); got != 3*time.Second {
		t.Fatalf("promptReadyTimeout invalid provider env = %v, want global 3s", got)
	}
}

func TestClaudeCallContextIgnoresParentDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelParent()

	callCtx, cancelCall := newClaudeCallContext(parent, time.Second)
	defer cancelCall()

	time.Sleep(60 * time.Millisecond)
	select {
	case <-callCtx.Done():
		t.Fatalf("call context ended from parent deadline: %v", callCtx.Err())
	default:
	}
}

func TestClaudeCallContextHonorsExplicitParentCancel(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	callCtx, cancelCall := newClaudeCallContext(parent, time.Second)
	defer cancelCall()

	cancelParent()
	select {
	case <-callCtx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("call context did not end after explicit parent cancel")
	}
}

func TestTmuxDoesNotAddVerboseFlagByDefault(t *testing.T) {
	adapter := NewClaudeCodeInteractiveAdapter("claude-code", &MockLogger{})
	args, tempFiles, err := adapter.buildClaudeArgs(&llmtypes.CallOptions{}, "", "7aa21987-0003-4d71-b887-ad73e29d2faf", "")
	if err != nil {
		t.Fatalf("buildClaudeArgs error = %v", err)
	}
	if len(tempFiles) != 0 {
		t.Fatalf("tempFiles = %v, want none", tempFiles)
	}
	if containsArg(args, "--verbose") {
		t.Fatalf("args = %v, want no --verbose by default", args)
	}
}

func TestHasReadyInputPromptRejectsRunningStatus(t *testing.T) {
	pane := `
⏺ Calling api-bridge…
✶ Precipitating… (1s · ↓ 2 tokens)
──────────────────────────────────────────────────
❯
──────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt
`
	if hasReadyInputPrompt(pane) {
		t.Fatal("hasReadyInputPrompt = true while Claude is still running")
	}
}

func TestHasReadyInputPromptAcceptsPromptWithDraftText(t *testing.T) {
	pane := `
⏺ Done
─────────────────────────────────────────────────── mcp-agent ──
❯ run the workflow
────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	if !hasReadyInputPrompt(pane) {
		t.Fatal("hasReadyInputPrompt = false for idle prompt with draft text")
	}
	if hasReadyEmptyInputPrompt(pane) {
		t.Fatal("hasReadyEmptyInputPrompt = true for idle prompt with unsubmitted draft text")
	}
}

func TestHasReadyInputPromptAcceptsIdlePromptWithEscFooter(t *testing.T) {
	pane := `
⏺ Done
─────────────────────────────────────────────────── mcp-agent ──
❯
────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt
`
	if !hasReadyInputPrompt(pane) {
		t.Fatal("hasReadyInputPrompt = false for idle prompt with esc footer")
	}
}

// TestHasReadyInputPromptAcceptsIdlePromptWithUpgradeNotice locks in
// a fix for a real production hang. Claude Code CLI shows an upgrade
// notice line at the very bottom of the TUI when a newer release is
// available ("current: 2.1.149 · latest: 2.1.150"). Before the fix,
// hasReadyInputPrompt walked up from the last line, hit this line
// BEFORE finding ❯, and returned false — so every chat turn hung
// indefinitely (terminal view showed claude was done, but mcpagent's
// wait loop never saw the agent as ready and never returned). The
// fix recognizes the "current:/latest:" pair as an ignorable footer.
func TestHasReadyInputPromptAcceptsIdlePromptWithUpgradeNotice(t *testing.T) {
	pane := `
⏺ Hi! How can I help you today?

✻ Baked for 2s

─────────────────────────────────────────────────── mcp-agent ──
❯
────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle) · ← for agents            14048 tokens
                                                                                              current: 2.1.149 · latest: 2.1.150
`
	if !hasReadyInputPrompt(pane) {
		t.Fatal("hasReadyInputPrompt = false when CLI shows upgrade notice — wait loop will hang indefinitely")
	}
}

func TestHasReadyInputPromptAcceptsClaudeSuggestionWithTrailingBlankFill(t *testing.T) {
	pane := `
 ▐▛███▜▌   Claude Code v2.1.141
▝▜█████▛▘  Sonnet 4.6 with high effort · Claude Max

─────────────────────────────────────────────────── mcp-agent ──
❯ Try "how do I log an error?"
────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
















`
	if !hasReadyInputPrompt(pane) {
		t.Fatal("hasReadyInputPrompt = false for idle prompt with Claude suggestion text and trailing blank fill")
	}
	if !hasReadyEmptyInputPrompt(pane) {
		t.Fatal("hasReadyEmptyInputPrompt = false for Claude suggestion placeholder")
	}
}

func TestWaitForTmuxPromptAcceptsNormalPromptWithoutResumePrompt(t *testing.T) {
	fakeBin := t.TempDir()
	capturePath := fakeBin + "/capture.txt"
	sendKeysPath := fakeBin + "/send-keys.log"
	tmuxPath := fakeBin + "/tmux"
	script := `#!/bin/sh
if [ "$1" = "capture-pane" ]; then
  cat "$TMUX_TEST_CAPTURE"
  exit 0
fi
if [ "$1" = "send-keys" ]; then
  printf '%s\n' "$*" >> "$TMUX_TEST_SEND_KEYS"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	pane := `
 ▐▛███▜▌   Claude Code v2.1.144
▝▜█████▛▘  Opus 4.6 with high effort · Claude Max
  ▘▘ ▝▝    ~/ai-work/mcp-agent-builder-go/workspace-docs/Workflow/instagram

─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────── mcp-agent-20260519-220956 ──
❯ Try "write a test for Perform Sheet Update Verification_code.go"
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle) · ← for agents                                                                                                 0 tokens
`
	if err := os.WriteFile(capturePath, []byte(pane), 0o644); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_CAPTURE", capturePath)
	t.Setenv("TMUX_TEST_SEND_KEYS", sendKeysPath)

	stream := make(chan llmtypes.StreamChunk, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := waitForTmuxPrompt(ctx, "test-session", stream); err != nil {
		t.Fatalf("waitForTmuxPrompt returned error for normal ready prompt: %v", err)
	}
	if got, err := os.ReadFile(sendKeysPath); err == nil && strings.TrimSpace(string(got)) != "" {
		t.Fatalf("waitForTmuxPrompt sent resume keys for normal prompt: %q", string(got))
	}
	select {
	case chunk := <-stream:
		if chunk.Type != llmtypes.StreamChunkTypeTerminal {
			t.Fatalf("streamed chunk type = %q, want terminal", chunk.Type)
		}
		if chunk.Content == "" {
			t.Fatal("streamed terminal content is empty")
		}
		if chunk.Metadata["tmux_session"] != "test-session" {
			t.Fatalf("tmux_session metadata = %#v, want test-session", chunk.Metadata["tmux_session"])
		}
	default:
		t.Fatal("waitForTmuxPrompt did not stream the startup pane")
	}
}

func TestHasReadyInputPromptAcceptsCompletedStatusAfterBackgroundAgents(t *testing.T) {
	pane := `
⏺ Both agents running in parallel now:

  ┌────────────────────────────────────────┬───────────────────────────────────────────────────────────┬────────────┐
  │                 Agent                  │                           Task                            │   Status   │
  ├────────────────────────────────────────┼───────────────────────────────────────────────────────────┼────────────┤
  │ bg-sentry-login-&-token-creation-79000 │ Browser login → create token in Sentry UI                 │ 🔄 Running │
  ├────────────────────────────────────────┼───────────────────────────────────────────────────────────┼────────────┤
  │ bg-github-.env-file-search-19000       │ Search repos for .env / Sentry vars                       │ 🔄 Running │
  └────────────────────────────────────────┴───────────────────────────────────────────────────────────┴────────────┘

  Will wait for both notifications.

✻ Baked for 27s

─────────────────────────────────────────────────────────────────── mcp-agent-20260517-143605 ──
❯ wait for the results
────────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	if !hasReadyInputPrompt(pane) {
		t.Fatal("hasReadyInputPrompt = false for idle prompt after Claude-created background agents")
	}
}

func TestHasClaudeActivityIgnoresCompletedAssistantOutput(t *testing.T) {
	pane := `
⏺ Final answer

✻ Worked for 1s · ↓ 2 tokens
──────────────────────────────────────────────────
❯
`
	if hasClaudeActivity(pane) {
		t.Fatal("hasClaudeActivity = true for completed assistant output")
	}
}

func TestClaudeCompletedWorkStatusIsNotRunningProgress(t *testing.T) {
	completed := []string{
		"✻ Baked for 27s",
		"✻ Cogitated for 1m 2s",
		"✻ Worked for 1s · ↓ 2 tokens",
		"✶ Sautéed for 8s",
	}
	for _, line := range completed {
		if isClaudeRunningProgressLine(line) {
			t.Fatalf("isClaudeRunningProgressLine(%q) = true, want false for completed status", line)
		}
		if cleaned := cleanClaudeTerminalProgressLine(line); cleaned != "" {
			t.Fatalf("cleanClaudeTerminalProgressLine(%q) = %q, want empty completed status", line, cleaned)
		}
	}
}

func TestHasClaudeActivityDetectsRunningStatus(t *testing.T) {
	pane := `
✶ Precipitating… (1s · ↓ 2 tokens) · esc to interrupt
`
	if !hasClaudeActivity(pane) {
		t.Fatal("hasClaudeActivity = false for running Claude status")
	}
}

func TestHasClaudeActivityDetectsRenamedRunningStatus(t *testing.T) {
	pane := `
✢ Flibbertigibbeting… (10m 46s · ↑ 19.1k tokens · thought for 3s)
  ⎿  Tip: Use /btw to ask a quick side question without interrupting Claude's current work
`
	if !hasClaudeActivity(pane) {
		t.Fatal("hasClaudeActivity = false for renamed Claude running status")
	}
}

func TestClaudeRunningPaneWithLiveInputIsNotReady(t *testing.T) {
	pane := `
⏺ Calling api-bridge…
✶ Precipitating… (1s · ↓ 2 tokens) · esc to interrupt
──────────────────────────────────────────────────
❯ ## Pre-validation failed (retry attempt 3)
──────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt
`
	if !hasClaudeActivity(pane) {
		t.Fatal("running pane with live input should count as active")
	}
	if hasReadyInputPrompt(pane) {
		t.Fatal("running pane with live input must not be treated as ready")
	}
}

func TestClaudeCompletedPaneWithStaleToolCallsIsNotActive(t *testing.T) {
	pane := `
⏺ Calling api-bridge 4 times… (ctrl+o to expand)

⏺ All 29 validation checks pass. Here's a summary of what was produced:

  STATUS: COMPLETED

✻ Sautéed for 7m 10s

────────────────────────────────────────────────────────────────────────────────
❯ run the next step in the workflow
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	if hasClaudeActivity(pane) {
		t.Fatal("completed pane with stale tool-call text above the prompt should not count as active")
	}
}

func TestHasNewAssistantOutput(t *testing.T) {
	delta := `
⏺ MLP_START
  instant answer
`
	if !hasNewAssistantOutput(delta) {
		t.Fatal("hasNewAssistantOutput = false, want true")
	}
}

func TestClaudeCompactionStatusIsNotAssistantResponse(t *testing.T) {
	pane := `
which stategry was run ?
⎿  Compacted (ctrl+o to see full summary)
❯
`
	if got, ok := extractTrailingUnmarkedAssistantResponse(pane); ok {
		t.Fatalf("trailing response = %q, want no assistant response", got)
	}
}

func TestClaudeQueuedValidationEchoIsNotAssistantResponse(t *testing.T) {
	pane := `
❯ ## Pre-validation failed (retry attempt 3)

❌ PRE-VALIDATION FAILED

Checks: 0 passed, 1 failed

Fix the specific issues above and re-produce the required outputs.
`
	if got, ok := extractTrailingUnmarkedAssistantResponse(pane); ok {
		t.Fatalf("trailing queued validation echo = %q, want no assistant response", got)
	}
	if !isClaudeTUIArtifact(`❌ PRE-VALIDATION FAILED
Checks: 0 passed, 1 failed
Fix the specific issues above and re-produce the required outputs.`) {
		t.Fatal("validation feedback echo should be treated as TUI/user artifact")
	}
	if !isClaudeTUIArtifact("Fix the specific issue above and re-produce the required outputs.") {
		t.Fatal("truncated validation feedback echo should be treated as TUI/user artifact")
	}
}

func TestLatestClaudePromptDraftUsesLastPromptLine(t *testing.T) {
	pane := `
⏺ Done

─────────────────────────────────────────────────────────────────── mcp-agent ──
❯ yes fix?

⏺ api-bridge - execute_shell_command (MCP)

─────────────────────────────────────────────────────────────────── mcp-agent ──
❯
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt
`
	got, ok := latestClaudePromptDraft(pane)
	if !ok {
		t.Fatal("latestClaudePromptDraft ok = false, want true")
	}
	if got != "" {
		t.Fatalf("latestClaudePromptDraft = %q, want blank current draft", got)
	}
}

func TestClaudePromptDraftToClearBeforePaste(t *testing.T) {
	pane := `
⏺ What do you think?

✻ Cogitated for 30s

─────────────────────────────────────────────────────────────────── mcp-agent ──
❯ go with option B
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	draft, ok := claudePromptDraftToClearBeforePaste(pane)
	if !ok {
		t.Fatal("claudePromptDraftToClearBeforePaste ok = false, want true for stale idle draft")
	}
	if draft != "go with option B" {
		t.Fatalf("draft = %q, want stale draft text", draft)
	}
}

func TestClaudePromptDraftToClearBeforePasteIgnoresBlankPrompt(t *testing.T) {
	pane := `
⏺ Done

─────────────────────────────────────────────────────────────────── mcp-agent ──
❯
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	if draft, ok := claudePromptDraftToClearBeforePaste(pane); ok {
		t.Fatalf("claudePromptDraftToClearBeforePaste = (%q, true), want no clear for blank prompt", draft)
	}
}

func TestClaudePromptDraftToClearBeforePasteIgnoresSuggestedPlaceholder(t *testing.T) {
	pane := `
⏺ What do you think?

✻ Cogitated for 30s

─────────────────────────────────────────────────────────────────── mcp-agent ──
❯ Try "go with option B"
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	if draft, ok := claudePromptDraftToClearBeforePaste(pane); ok {
		t.Fatalf("claudePromptDraftToClearBeforePaste = (%q, true), want no clear for Claude suggestion placeholder", draft)
	}
}

func TestClaudePromptDraftToClearBeforePasteIgnoresContextualSuggestion(t *testing.T) {
	pane := `
⏺ Workflow finished.

─────────────────────────────────────────────────────────────────── mcp-agent ──
❯ show me what it found
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	if draft, ok := claudePromptDraftToClearBeforePaste(pane); ok {
		t.Fatalf("claudePromptDraftToClearBeforePaste = (%q, true), want no clear for Claude contextual suggestion", draft)
	}
	if !hasReadyEmptyInputPrompt(pane) {
		t.Fatal("hasReadyEmptyInputPrompt = false for Claude contextual suggestion")
	}
}

func TestClaudePromptDraftToClearBeforePasteIgnoresQueuedMessagesHint(t *testing.T) {
	pane := `
⏺ Done

─────────────────────────────────────────────────────────────────── mcp-agent ──
❯ Press up to edit queued messages
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	if draft, ok := claudePromptDraftToClearBeforePaste(pane); ok {
		t.Fatalf("claudePromptDraftToClearBeforePaste = (%q, true), want no clear for Claude queued-message hint", draft)
	}
	if !hasReadyEmptyInputPrompt(pane) {
		t.Fatal("hasReadyEmptyInputPrompt = false for Claude queued-message hint")
	}
}

func TestClaudePromptDraftToClearBeforePasteClearsNBSPDraft(t *testing.T) {
	pane := `
⏺ Done

─────────────────────────────────────────────────────────────────── mcp-agent ──
❯ continue hwere we left
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
`
	draft, ok := claudePromptDraftToClearBeforePaste(pane)
	if !ok {
		t.Fatal("claudePromptDraftToClearBeforePaste ok = false, want true for non-breaking-space draft")
	}
	if draft != "continue hwere we left" {
		t.Fatalf("draft = %q, want pasted user draft", draft)
	}
	if hasReadyEmptyInputPrompt(pane) {
		t.Fatal("hasReadyEmptyInputPrompt = true for non-breaking-space user draft")
	}
}

func TestClaudePromptDraftToClearBeforePasteIgnoresRunningPrompt(t *testing.T) {
	pane := `
⏺ Calling api-bridge…
✶ Working… (1s)
─────────────────────────────────────────────────────────────────── mcp-agent ──
❯ go with option B
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt
`
	if draft, ok := claudePromptDraftToClearBeforePaste(pane); ok {
		t.Fatalf("claudePromptDraftToClearBeforePaste = (%q, true), want no clear while Claude is active", draft)
	}
}

func TestClaudePromptDraftToClearBeforePasteIgnoresRunningCreatingPrompt(t *testing.T) {
	pane := `
⏺ Acknowledged — all review pipeline sub-agents and the eval step are running.

✻ Churned for 12s

❯ continue

⏺ api-bridge - execute_shell_command (MCP)

✢ Creating… (4s · ↓ 15 tokens)

❯ continue
`
	if draft, ok := claudePromptDraftToClearBeforePaste(pane); ok {
		t.Fatalf("claudePromptDraftToClearBeforePaste = (%q, true), want no clear while Claude is creating", draft)
	}
}

func TestClaudePromptDraftStillMatchesMessage(t *testing.T) {
	if !claudePromptDraftStillMatchesMessage("yes fix", "yes fix") {
		t.Fatal("exact live input draft should match message")
	}
	if !claudePromptDraftStillMatchesMessage("[Pasted Text: 61 lines]", strings.Repeat("large prompt\n", 61)) {
		t.Fatal("pasted text marker should be treated as an uncleared draft")
	}
	if claudePromptDraftStillMatchesMessage("", "yes fix") {
		t.Fatal("blank prompt draft should be treated as submitted")
	}
	if claudePromptDraftStillMatchesMessage("new unrelated draft", "yes fix") {
		t.Fatal("different draft should not block submit verification")
	}
}

func TestClaudeLatestAssistantResponseRejectsToolProgress(t *testing.T) {
	pane := `
⏺ Calling api-bridge… (ctrl+o to expand)
`
	if got, ok := extractLatestUnmarkedAssistantResponse(pane); ok {
		t.Fatalf("latest tool progress = %q, want no assistant response", got)
	}
}

func TestParseTmuxMajorVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    int
		wantOK  bool
	}{
		{name: "modern", version: "tmux 3.6a", want: 3, wantOK: true},
		{name: "old", version: "tmux 2.9", want: 2, wantOK: true},
		{name: "two digits", version: "tmux 10.1", want: 10, wantOK: true},
		{name: "invalid", version: "not tmux", wantOK: false},
		{name: "empty", version: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTmuxMajorVersion(tt.version)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("parseTmuxMajorVersion(%q) = (%d, %t), want (%d, %t)", tt.version, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestIsContextCanceledErrorHandlesNil(t *testing.T) {
	if isContextCanceledError(nil) {
		t.Fatalf("nil error should not be treated as context canceled")
	}
	if !isContextCanceledError(context.Canceled) {
		t.Fatalf("context.Canceled should be detected")
	}
}

func textPartMessage(role llmtypes.ChatMessageType, text string) llmtypes.MessageContent {
	return llmtypes.MessageContent{
		Role:  role,
		Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: text}},
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func argValue(args []string, key string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}
