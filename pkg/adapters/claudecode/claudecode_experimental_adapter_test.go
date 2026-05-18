package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestClaudeExperimentalStreamTmuxScreenFlag(t *testing.T) {
	t.Setenv(EnvClaudeExperimentalStreamTmuxScreen, "")
	if !claudeExperimentalStreamTmuxScreenEnabled() {
		t.Fatal("tmux screen streaming should be enabled by default")
	}

	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(EnvClaudeExperimentalStreamTmuxScreen, value)
		if !claudeExperimentalStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be enabled for %q", value)
		}
	}

	for _, value := range []string{"0", "false", "FALSE", "no", "off"} {
		t.Setenv(EnvClaudeExperimentalStreamTmuxScreen, value)
		if claudeExperimentalStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be disabled for %q", value)
		}
	}
}

func TestClaudeExperimentalShellCommandUsesCallerWorkingDir(t *testing.T) {
	shell := writeExecutableTestShell(t, "zsh")
	t.Setenv("CODING_AGENT_LOGIN_SHELL", shell)
	t.Setenv("CODING_AGENT_SHELL_MODE", "")

	got := claudeExperimentalShellCommand([]string{"claude", "--system-prompt-file", "/tmp/sys.md"}, "/tmp/user chat")
	if !strings.HasPrefix(got, "'"+shell+"' '-ilc' ") {
		t.Fatalf("shell command = %q, want login shell prefix", got)
	}
	if !strings.Contains(got, "'/tmp/user chat'") {
		t.Fatalf("shell command = %q, want caller cwd passed to login shell", got)
	}
}

func TestClaudeExperimentalShellCommandDirectMode(t *testing.T) {
	t.Setenv("CODING_AGENT_SHELL_MODE", "direct")

	got := claudeExperimentalShellCommand([]string{"claude", "--system-prompt-file", "/tmp/sys.md"}, "/tmp/user chat")
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

func TestExperimentalBuildClaudeArgsDefaultsToNoInternalTools(t *testing.T) {
	adapter := NewClaudeCodeExperimentalAdapter("claude-code", &MockLogger{})
	args, tempFiles, err := adapter.buildClaudeArgs(&llmtypes.CallOptions{}, "7aa21987-0003-4d71-b887-ad73e29d2faf", "")
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

func TestExperimentalRejectsImageContent(t *testing.T) {
	adapter := NewClaudeCodeExperimentalAdapter("claude-code", &MockLogger{})

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

func TestClaudeExperimentalSessionsFromTmuxListOnlyMatchesAdapterPrefix(t *testing.T) {
	out := strings.Join([]string{
		"mlp-claude-code-exp-111-aaaa",
		"user-work",
		"mlp-claude-code-experimental-other",
		"mlp-claude-code-exp",
		"mlp-claude-code-exp2-222-bbbb",
	}, "\n")

	got := claudeExperimentalSessionsFromTmuxList(out, "mlp-claude-code-exp")
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
		{name: "no server", err: errors.New("failed to capture Claude Code experimental session: exit status 1: no server running on /private/tmp/tmux-501/default"), want: true},
		{name: "missing pane", err: errors.New("failed to capture Claude Code experimental session: exit status 1: can't find pane: mlp-claude-code-exp-1"), want: true},
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

func TestExperimentalBuildClaudeArgsPassesBridgeOptions(t *testing.T) {
	adapter := NewClaudeCodeExperimentalAdapter("claude-sonnet-4-6", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"/tmp/mcpbridge"}}}`)(opts)
	WithClaudeCodeTools("WebSearch")(opts)
	WithAllowedTools("mcp__api-bridge__*,WebSearch")(opts)
	WithResumeSessionID("7aa21987-0003-4d71-b887-ad73e29d2faf")(opts)
	WithEffort("high")(opts)

	args, tempFiles, err := adapter.buildClaudeArgs(opts, "7aa21987-0003-4d71-b887-ad73e29d2faf", "native system prompt")
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
		t.Fatalf("args = %v, experimental mode should replace with --system-prompt, not append", args)
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
		"experimental adapter",
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
}

func TestIsClaudeResumeCompressionPromptIgnoresNormalPrompt(t *testing.T) {
	pane := `
─────────────────────────────────────────────────── mcp-agent ──
❯
`
	if isClaudeResumeCompressionPrompt(pane) {
		t.Fatal("isClaudeResumeCompressionPrompt = true for normal prompt")
	}
}

func TestExperimentalTimeoutEnv(t *testing.T) {
	t.Setenv(EnvClaudeExperimentalTimeoutSeconds, "2")
	if got := tmuxTimeout(); got != 2*time.Second {
		t.Fatalf("tmuxTimeout = %v, want 2s", got)
	}

	t.Setenv(EnvClaudeExperimentalTimeoutSeconds, "bad")
	if got := tmuxTimeout(); got != defaultTmuxTimeout {
		t.Fatalf("tmuxTimeout invalid env = %v, want default %v", got, defaultTmuxTimeout)
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

func TestExperimentalVerboseEnvAddsVerboseFlag(t *testing.T) {
	t.Setenv(EnvClaudeExperimentalVerbose, "true")

	adapter := NewClaudeCodeExperimentalAdapter("claude-code", &MockLogger{})
	args, tempFiles, err := adapter.buildClaudeArgs(&llmtypes.CallOptions{}, "7aa21987-0003-4d71-b887-ad73e29d2faf", "")
	if err != nil {
		t.Fatalf("buildClaudeArgs error = %v", err)
	}
	if len(tempFiles) != 0 {
		t.Fatalf("tempFiles = %v, want none", tempFiles)
	}
	if !containsArg(args, "--verbose") {
		t.Fatalf("args = %v, want --verbose when %s=true", args, EnvClaudeExperimentalVerbose)
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

func TestClaudeLatestAssistantResponseRejectsToolProgress(t *testing.T) {
	pane := `
⏺ Calling api-bridge… (ctrl+o to expand)
`
	if got, ok := extractLatestUnmarkedAssistantResponse(pane); ok {
		t.Fatalf("latest tool progress = %q, want no assistant response", got)
	}
}

func TestHasClaudeExpandableToolSummary(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want bool
	}{
		{
			name: "calling summary",
			pane: "⏺ Calling api-bridge 2 times… (ctrl+o to expand)",
			want: true,
		},
		{
			name: "called summary",
			pane: "Called api-bridge 6 times (ctrl+o to expand)",
			want: true,
		},
		{
			name: "compaction summary",
			pane: "⎿  Compacted (ctrl+o to see full summary)",
			want: false,
		},
		{
			name: "generic footer",
			pane: "Tip: ctrl+o opens history",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasClaudeExpandableToolSummary(tt.pane); got != tt.want {
				t.Fatalf("hasClaudeExpandableToolSummary(%q) = %t, want %t", tt.pane, got, tt.want)
			}
		})
	}
}

func TestClaudeExperimentalAutoExpandToolSummaryDisabledByDefault(t *testing.T) {
	t.Setenv(EnvClaudeExperimentalAutoExpandTools, "")
	if claudeExperimentalAutoExpandToolSummaryEnabled() {
		t.Fatal("auto expand tool summary enabled by default; want disabled")
	}

	t.Setenv(EnvClaudeExperimentalAutoExpandTools, "true")
	if !claudeExperimentalAutoExpandToolSummaryEnabled() {
		t.Fatal("auto expand tool summary disabled with explicit true; want enabled")
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
