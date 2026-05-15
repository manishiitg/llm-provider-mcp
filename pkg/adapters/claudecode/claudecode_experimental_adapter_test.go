package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

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
	prompt, err := buildTmuxPrompt(conversation, opts, claudeResumeIDFromOptions(opts))
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

func TestBuildTmuxPromptFreshSingleTurnSendsOnlyUserText(t *testing.T) {
	prompt, err := buildTmuxPrompt(
		[]llmtypes.MessageContent{
			textPartMessage(llmtypes.ChatMessageTypeHuman, "hi"),
		},
		&llmtypes.CallOptions{},
		"",
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

	prompt, err := buildTmuxPrompt(saved.History, &llmtypes.CallOptions{}, "")
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

	prompt, err := buildTmuxPrompt(saved.History, opts, claudeResumeIDFromOptions(opts))
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

func TestSendPromptWaitsForLargePasteBeforeSubmit(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	tmuxPath := filepath.Join(tmpDir, "tmux")
	script := `#!/bin/sh
state_dir="$TMUX_TEST_STATE"
mkdir -p "$state_dir"
cmd="$1"
shift
case "$cmd" in
  load-buffer)
    cat > "$state_dir/prompt"
    echo loaded > "$state_dir/loaded"
    ;;
  paste-buffer)
    echo "$*" > "$state_dir/paste_args"
    ;;
  capture-pane)
    if [ -f "$state_dir/send_keys_args" ]; then
      echo "✶ Precipitating… (1s · ↓ 2 tokens) · esc to interrupt"
      exit 0
    fi
    count_file="$state_dir/capture_count"
    count=0
    if [ -f "$count_file" ]; then
      count="$(cat "$count_file")"
    fi
    count=$((count + 1))
    echo "$count" > "$count_file"
    if [ "$count" -lt 3 ]; then
      echo "Claude prompt ready"
    else
      echo "[Pasted text #1 +200 lines]"
    fi
    ;;
  send-keys)
    echo "$*" > "$state_dir/send_keys_args"
    ;;
  *)
    echo "unexpected tmux command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_STATE", stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sendPromptToTmux(ctx, "fake-session", strings.Repeat("line\n", 500)); err != nil {
		t.Fatalf("sendPromptToTmux error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(stateDir, "send_keys_args"))
	if err != nil {
		t.Fatalf("read send_keys_args: %v", err)
	}
	if !strings.Contains(string(raw), "C-m") {
		t.Fatalf("send keys args = %q, want C-m", string(raw))
	}
	pasteArgsRaw, err := os.ReadFile(filepath.Join(stateDir, "paste_args"))
	if err != nil {
		t.Fatalf("read paste_args: %v", err)
	}
	pasteArgs := string(pasteArgsRaw)
	for _, want := range []string{"-p", "-r"} {
		if !strings.Contains(pasteArgs, want) {
			t.Fatalf("paste args = %q, want %s to preserve multi-line prompt as bracketed paste", pasteArgs, want)
		}
	}

	countRaw, err := os.ReadFile(filepath.Join(stateDir, "capture_count"))
	if err != nil {
		t.Fatalf("read capture_count: %v", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(countRaw)))
	if err != nil {
		t.Fatalf("parse capture_count: %v", err)
	}
	if count < 3 {
		t.Fatalf("capture count = %d, want at least 3 to prove submit waited for stable pasted pane", count)
	}
	promptRaw, err := os.ReadFile(filepath.Join(stateDir, "prompt"))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if strings.Contains(string(promptRaw), "Adapter prompt-ready marker") {
		t.Fatalf("prompt contains adapter paste marker: %q", string(promptRaw))
	}
}

func TestSendPromptPreservesArbitraryUserTextInTmuxBuffer(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	tmuxPath := filepath.Join(tmpDir, "tmux")
	script := `#!/bin/sh
state_dir="$TMUX_TEST_STATE"
mkdir -p "$state_dir"
cmd="$1"
shift
case "$cmd" in
  load-buffer)
    cat > "$state_dir/prompt"
    ;;
  paste-buffer)
    echo "$*" > "$state_dir/paste_args"
    ;;
  capture-pane)
    count_file="$state_dir/capture_count"
    count=0
    if [ -f "$count_file" ]; then
      count="$(cat "$count_file")"
    fi
    count=$((count + 1))
    echo "$count" > "$count_file"
    if [ "$count" -le 2 ]; then
      echo "❯"
    else
      echo "✶ Composing… (1s · ↓ 2 tokens) · esc to interrupt"
    fi
    ;;
  send-keys)
    echo "$*" > "$state_dir/send_keys_args"
    ;;
  *)
    echo "unexpected tmux command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_STATE", stateDir)

	prompt := strings.Join([]string{
		"hi",
		"",
		"second line",
		"unicode: नमस्ते こんにちは Привет café 🚀",
		"shell-looking text: $(echo nope) && rm -rf /",
		"quotes: 'single' \"double\" `backticks`",
	}, "\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sendPromptToTmux(ctx, "fake-session", prompt); err != nil {
		t.Fatalf("sendPromptToTmux error = %v", err)
	}

	promptRaw, err := os.ReadFile(filepath.Join(stateDir, "prompt"))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if string(promptRaw) != prompt {
		t.Fatalf("tmux buffer prompt changed:\ngot  %q\nwant %q", string(promptRaw), prompt)
	}

	pasteArgsRaw, err := os.ReadFile(filepath.Join(stateDir, "paste_args"))
	if err != nil {
		t.Fatalf("read paste_args: %v", err)
	}
	pasteArgs := string(pasteArgsRaw)
	for _, want := range []string{"-p", "-r"} {
		if !strings.Contains(pasteArgs, want) {
			t.Fatalf("paste args = %q, want %s for arbitrary multi-line text", pasteArgs, want)
		}
	}
}

func TestWaitForPromptPasteFallsBackToStableCollapsedPaste(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	tmuxPath := filepath.Join(tmpDir, "tmux")
	script := `#!/bin/sh
state_dir="$TMUX_TEST_STATE"
mkdir -p "$state_dir"
cmd="$1"
shift
case "$cmd" in
  capture-pane)
    count_file="$state_dir/capture_count"
    count=0
    if [ -f "$count_file" ]; then
      count="$(cat "$count_file")"
    fi
    count=$((count + 1))
    echo "$count" > "$count_file"
    if [ "$count" -eq 1 ]; then
      echo "Claude prompt ready"
    elif [ "$count" -eq 2 ]; then
      echo "[Pasted text #1 +13 lines] loading"
    else
      echo "[Pasted text #1 +13 lines]"
    fi
    ;;
  *)
    echo "unexpected tmux command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_STATE", stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	submitted, err := waitForPromptPaste(ctx, "fake-session", "Claude prompt ready\n")
	if err != nil {
		t.Fatalf("waitForPromptPaste error = %v", err)
	}
	if submitted {
		t.Fatalf("waitForPromptPaste submitted = true, want false for visible paste awaiting explicit submit")
	}

	countRaw, err := os.ReadFile(filepath.Join(stateDir, "capture_count"))
	if err != nil {
		t.Fatalf("read capture_count: %v", err)
	}
	if strings.TrimSpace(string(countRaw)) == "2" {
		t.Fatalf("capture count = %q, wait returned before collapsed paste was stable", strings.TrimSpace(string(countRaw)))
	}
}

func TestWaitForPromptPasteAllowsInvisibleClaudeRedraw(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	tmuxPath := filepath.Join(tmpDir, "tmux")
	script := `#!/bin/sh
state_dir="$TMUX_TEST_STATE"
mkdir -p "$state_dir"
cmd="$1"
shift
case "$cmd" in
  capture-pane)
    count_file="$state_dir/capture_count"
    count=0
    if [ -f "$count_file" ]; then
      count="$(cat "$count_file")"
    fi
    count=$((count + 1))
    echo "$count" > "$count_file"
    cat <<'PANE'
─────────────────────────────────────────────────── mcp-agent ──
❯
────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)
PANE
    ;;
  *)
    echo "unexpected tmux command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_STATE", stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pane := "─────────────────────────────────────────────────── mcp-agent ──\n❯\n────────────────────────────────────────────────────────────────\n  ⏵⏵ don't ask on (shift+tab to cycle)\n"
	started := time.Now()
	submitted, err := waitForPromptPaste(ctx, "fake-session", pane)
	if err != nil {
		t.Fatalf("waitForPromptPaste error = %v", err)
	}
	if submitted {
		t.Fatalf("waitForPromptPaste submitted = true, want false for invisible paste awaiting explicit submit")
	}
	if elapsed := time.Since(started); elapsed < promptPasteInvisibleGrace {
		t.Fatalf("waitForPromptPaste returned after %v, want at least invisible paste grace %v", elapsed, promptPasteInvisibleGrace)
	}
}

func TestWaitForPromptPasteDetectsPromptAlreadySubmitted(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	tmuxPath := filepath.Join(tmpDir, "tmux")
	script := `#!/bin/sh
cmd="$1"
shift
case "$cmd" in
  capture-pane)
    cat <<'PANE'
❯ run step
✽ Pontificating… (1s · ↓ 10 tokens)
──────────────────────────────────────────────────
❯
──────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt
PANE
    ;;
  *)
    echo "unexpected tmux command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_STATE", stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	submitted, err := waitForPromptPaste(ctx, "fake-session", "❯\n")
	if err != nil {
		t.Fatalf("waitForPromptPaste error = %v", err)
	}
	if !submitted {
		t.Fatal("waitForPromptPaste submitted = false, want true when Claude activity has already started")
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

func TestHasClaudeActivityDetectsRunningStatus(t *testing.T) {
	pane := `
✶ Precipitating… (1s · ↓ 2 tokens) · esc to interrupt
`
	if !hasClaudeActivity(pane) {
		t.Fatal("hasClaudeActivity = false for running Claude status")
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

func TestWaitForClaudeIdleAfterActivityWaitsForStablePrompt(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	tmuxPath := filepath.Join(tmpDir, "tmux")
	script := `#!/bin/sh
state_dir="$TMUX_TEST_STATE"
mkdir -p "$state_dir"
cmd="$1"
shift
case "$cmd" in
  capture-pane)
    count_file="$state_dir/capture_count"
    count=0
    if [ -f "$count_file" ]; then
      count="$(cat "$count_file")"
    fi
    count=$((count + 1))
    echo "$count" > "$count_file"
    if [ "$count" -le 2 ]; then
      echo "✶ Precipitating… (1s · ↓ 2 tokens) · esc to interrupt"
    else
      echo "⏺ MLP_START"
      echo "  done"
      echo "  MLP_END"
      echo
      echo "✻ Worked for 1s · ↓ 2 tokens"
      echo "❯"
    fi
    ;;
  *)
    echo "unexpected tmux command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_STATE", stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	captured, _, err := waitForClaudeIdleAfterActivity(ctx, "fake-session", false, "", "", "", nil)
	if err != nil {
		t.Fatalf("waitForClaudeIdleAfterActivity error = %v", err)
	}
	if !strings.Contains(captured, "done") {
		t.Fatalf("captured = %q, want final response", captured)
	}

	countRaw, err := os.ReadFile(filepath.Join(stateDir, "capture_count"))
	if err != nil {
		t.Fatalf("read capture_count: %v", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(countRaw)))
	if err != nil {
		t.Fatalf("parse capture_count: %v", err)
	}
	if count < 6 {
		t.Fatalf("capture count = %d, wait returned before idle pane was stable", count)
	}
}

func TestWaitForClaudeIdleAfterActivityAcceptsAlreadySeenActivity(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	tmuxPath := filepath.Join(tmpDir, "tmux")
	script := `#!/bin/sh
state_dir="$TMUX_TEST_STATE"
mkdir -p "$state_dir"
cmd="$1"
shift
case "$cmd" in
  capture-pane)
    count_file="$state_dir/capture_count"
    count=0
    if [ -f "$count_file" ]; then
      count="$(cat "$count_file")"
    fi
    count=$((count + 1))
    echo "$count" > "$count_file"
    echo "⏺ instant answer"
    echo "❯"
    ;;
  *)
    echo "unexpected tmux command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_STATE", stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	captured, _, err := waitForClaudeIdleAfterActivity(ctx, "fake-session", true, "", "", "", nil)
	if err != nil {
		t.Fatalf("waitForClaudeIdleAfterActivity error = %v", err)
	}
	if !strings.Contains(captured, "instant answer") {
		t.Fatalf("captured = %q, want instant answer", captured)
	}
}

func TestWaitForMarkedResponseReturnsCapturedAssistantOnTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	tmuxPath := filepath.Join(tmpDir, "tmux")
	script := `#!/bin/sh
cmd="$1"
shift
case "$cmd" in
  capture-pane)
    echo "⏺ Partial but useful answer"
    echo "  STATUS: COMPLETED"
    echo "✳ Envisioning… (8m 32s · ↓ 9.8k tokens) · esc to interrupt"
    ;;
  *)
    echo "unexpected tmux command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	content, _, err := waitForMarkedResponse(ctx, "fake-session", "MLP_START", "MLP_END", "", nil)
	if err != nil {
		t.Fatalf("waitForMarkedResponse error = %v", err)
	}
	want := "Partial but useful answer\nSTATUS: COMPLETED"
	if content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
}

func TestExtractClaudeTerminalProgressFiltersPromptAndFinalAnswer(t *testing.T) {
	pane := `
Conversation:
HUMAN:
Use tool and answer.
✶ Precipitating… (1s · ↓ 2 tokens) · esc to interrupt
⏺ Calling api-bridge ping…
⏺ MLP_START
  final answer should not stream as progress
  MLP_END
✻ Worked for 1s
`
	got := extractClaudeTerminalProgress(pane, "MLP_START", "MLP_END")
	if strings.Contains(got, "HUMAN:") ||
		strings.Contains(got, "prompt-ready") ||
		strings.Contains(got, "final answer should not stream") ||
		strings.Contains(got, "Worked for") {
		t.Fatalf("progress leaked prompt/final/chrome text: %q", got)
	}
	if strings.Contains(got, "Claude Code is working...") || got != "Calling api-bridge..." {
		t.Fatalf("progress = %q, want normalized tool line only", got)
	}
}

func TestExtractClaudeTerminalProgressCollapsesRepeatedClaudeStatus(t *testing.T) {
	pane := `
✳ Julienning…
⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt ● high · /effort
✶ Julienning…
⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt ● high · /effort
Calling api-bridge… (ctrl+o to expand)
✻ Julienning… (3s · ↓ 92 tokens · thinking with high effort)
⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt ● high · /effort
Calling api-bridge 2 times… (ctrl+o to expand)
✽ Julienning… (5s · ↓ 142 tokens · thinking with high effort)
Called api-bridge 2 times (ctrl+o to expand)
`
	got := extractClaudeTerminalProgress(pane, "", "")
	want := "Called api-bridge 2 times"
	if got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
	if strings.Contains(got, "Julienning") || strings.Contains(got, "effort") || strings.Contains(got, "esc to interrupt") {
		t.Fatalf("progress leaked repeated TUI status: %q", got)
	}
}

func TestExtractClaudeTerminalProgressDoesNotEmitGenericWorkingStatus(t *testing.T) {
	pane := `
✳ Julienning…
⏵⏵ don't ask on (shift+tab to cycle) · esc to interrupt ● high · /effort
`
	if got := extractClaudeTerminalProgress(pane, "", ""); got != "" {
		t.Fatalf("progress = %q, want no generic working status", got)
	}
}

func TestExtractClaudeVisibleAssistantTextStreamsTextBlocksOnly(t *testing.T) {
	pane := `
❯ user prompt should not stream
✶ Precipitating… (1s · ↓ 2 tokens) · esc to interrupt
⏺ I am checking the available files first.

⏺ Calling api-bridge…

⏺ The simplest step is prepare-test-fixtures.
  It only writes a small fixture JSON file.
`
	got := extractClaudeVisibleAssistantText(pane, "", "")
	want := "I am checking the available files first.\n\nThe simplest step is prepare-test-fixtures.\nIt only writes a small fixture JSON file."
	if got != want {
		t.Fatalf("assistant text = %q, want %q", got, want)
	}
	if strings.Contains(got, "Calling api-bridge") || strings.Contains(got, "user prompt") {
		t.Fatalf("assistant text leaked non-assistant content: %q", got)
	}
}

func TestExtractClaudeVisibleAssistantTextSkipsTUIRepaintProgress(t *testing.T) {
	pane := `
⏺ Let me check the current state of saved jobs and then run the bidding
  workflow.

Calling api-bridge… (ctrl+o to expand)
· Vibing… (9s · ↑ 363 tokens · thinking with high effort)
⏺ Let me check the current state of saved jobs and then run the bidding
  workflow.

Called api-bridge 2 times (ctrl+o to expand)
⎿ Tip: Use /btw to ask a quick side question without interrupting Claude's current work
⏺ Now let me check the variables and then run the bidding workflow:

Calling api-bridge… (ctrl+o to expand)
· Vibing… (28s · ↓ 1.1k tokens · thinking more with high effort)
`
	got := extractClaudeVisibleAssistantText(pane, "", "")
	want := "Let me check the current state of saved jobs and then run the bidding\nworkflow.\n\nNow let me check the variables and then run the bidding workflow:"
	if got != want {
		t.Fatalf("assistant text = %q, want %q", got, want)
	}
	for _, leaked := range []string{"Calling api-bridge", "Called api-bridge", "Vibing", "Tip:"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("assistant text leaked %q from TUI repaint: %q", leaked, got)
		}
	}
}

func TestStreamClaudeAssistantDeltaDeduplicatesCumulativePaneText(t *testing.T) {
	streamChan := make(chan llmtypes.StreamChunk, 4)
	last := ""

	streamClaudeAssistantDelta(streamChan, "First line", &last)
	streamClaudeAssistantDelta(streamChan, "First line\nSecond line", &last)
	streamClaudeAssistantDelta(streamChan, "First line\nSecond line", &last)
	close(streamChan)

	var chunks []string
	for chunk := range streamChan {
		chunks = append(chunks, chunk.Content)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %#v, want 2 chunks", chunks)
	}
	if chunks[0] != "First line\n" || chunks[1] != "Second line\n" {
		t.Fatalf("chunks = %#v, want assistant deltas", chunks)
	}
}

func TestRemainingFinalContentForStreamSkipsAlreadyStreamedFinal(t *testing.T) {
	if got := remainingFinalContentForStream("Final answer", "Thinking\n\nFinal answer"); got != "" {
		t.Fatalf("remaining final = %q, want empty", got)
	}
	if got := remainingFinalContentForStream("Final answer", "Thinking"); got != "Final answer" {
		t.Fatalf("remaining final = %q, want full final", got)
	}
}

func TestExtractClaudeTerminalProgressNormalizesRepeatedToolCounts(t *testing.T) {
	first := extractClaudeTerminalProgress("Calling api-bridge… (ctrl+o to expand)", "", "")
	second := extractClaudeTerminalProgress("Calling api-bridge 6 times… (ctrl+o to expand)", "", "")
	if first != "Calling api-bridge..." || second != first {
		t.Fatalf("calling progress = (%q, %q), want both normalized to first-call state", first, second)
	}

	done := extractClaudeTerminalProgress("Called api-bridge 6 times (ctrl+o to expand)", "", "")
	if done != "Called api-bridge 6 times" {
		t.Fatalf("done progress = %q, want called summary", done)
	}
}

func TestWaitForClaudeIdleAfterActivityStreamsProgressNonBlocking(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	tmuxPath := filepath.Join(tmpDir, "tmux")
	script := `#!/bin/sh
state_dir="$TMUX_TEST_STATE"
mkdir -p "$state_dir"
cmd="$1"
shift
case "$cmd" in
  capture-pane)
    count_file="$state_dir/capture_count"
    count=0
    if [ -f "$count_file" ]; then
      count="$(cat "$count_file")"
    fi
    count=$((count + 1))
    echo "$count" > "$count_file"
    if [ "$count" -le 2 ]; then
      echo "Conversation:"
      echo "HUMAN:"
      echo "secret prompt should not stream"
      echo "✶ Precipitating… (${count}s · ↓ 2 tokens) · esc to interrupt"
      echo "⏺ Calling api-bridge ping…"
    else
      echo "⏺ MLP_START"
      echo "  final text"
      echo "  MLP_END"
      echo "❯"
    fi
    ;;
  *)
    echo "unexpected tmux command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_STATE", stateDir)

	streamChan := make(chan llmtypes.StreamChunk, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, _, err := waitForClaudeIdleAfterActivity(ctx, "fake-session", false, "", "MLP_START", "MLP_END", streamChan); err != nil {
		t.Fatalf("waitForClaudeIdleAfterActivity error = %v", err)
	}
	close(streamChan)

	var streamed strings.Builder
	for chunk := range streamChan {
		if chunk.Type == llmtypes.StreamChunkTypeContent {
			streamed.WriteString(chunk.Content)
		}
	}
	got := streamed.String()
	if strings.Contains(got, "Claude Code is working...") || !strings.Contains(got, "Calling api-bridge...") {
		t.Fatalf("streamed progress = %q, want normalized tool progress", got)
	}
	if strings.Contains(got, "secret prompt") || strings.Contains(got, "final text") {
		t.Fatalf("streamed progress leaked prompt/final answer: %q", got)
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
