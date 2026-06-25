package codexcli

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxexec"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxlaunch"
)

type MockLogger struct{}

func (l *MockLogger) Infof(format string, args ...any)  { fmt.Printf("INFO: "+format+"\n", args...) }
func (l *MockLogger) Errorf(format string, args ...any) { fmt.Printf("ERROR: "+format+"\n", args...) }
func (l *MockLogger) Debugf(format string, args ...interface{}) {
	fmt.Printf("DEBUG: "+format+"\n", args...)
}

// Keep the real CLI transport contract model configurable because the live CLI
// can report model capacity or upgrade state independently of the tmux
// transport. Model-tier defaults are tested separately from the protocol.
var codexCLIRealContractModel = codexCLIRealContractModelFromEnv()

func codexCLIRealContractModelFromEnv() string {
	if model := strings.TrimSpace(os.Getenv("CODEX_CLI_REAL_CONTRACT_MODEL")); model != "" {
		return model
	}
	return "gpt-5.4-mini"
}

func TestCodexCLIAdapterImplementsWebSearchModel(t *testing.T) {
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})
	if _, ok := interface{}(adapter).(llmtypes.WebSearchModel); !ok {
		t.Fatal("CodexCLIAdapter should implement llmtypes.WebSearchModel")
	}
}

func TestCodexInteractiveStreamTmuxScreenFlag(t *testing.T) {
	t.Setenv(EnvCodexInteractiveStreamTmuxScreen, "")
	if !codexInteractiveStreamTmuxScreenEnabled() {
		t.Fatal("tmux screen streaming should be enabled by default")
	}

	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(EnvCodexInteractiveStreamTmuxScreen, value)
		if !codexInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be enabled for %q", value)
		}
	}

	for _, value := range []string{"0", "false", "FALSE", "no", "off"} {
		t.Setenv(EnvCodexInteractiveStreamTmuxScreen, value)
		if codexInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be disabled for %q", value)
		}
	}
}

func TestCodexTerminalStreamCapturesRawScreenRows(t *testing.T) {
	fakeBin := t.TempDir()
	argsPath := fakeBin + "/capture-args.log"
	tmuxPath := fakeBin + "/tmux"
	script := `#!/bin/sh
if [ "$1" = "capture-pane" ]; then
  printf '%s\n' "$*" > "$TMUX_TEST_CAPTURE_ARGS"
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
	if !streamCodexTerminalSnapshot(context.Background(), "raw-display-session", stream, &last) {
		t.Fatal("streamCodexTerminalSnapshot returned false")
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
	if !strings.Contains(string(args), " -J") {
		t.Fatalf("terminal display capture did not use joined rows (-J): %q", string(args))
	}
	if want := fmt.Sprintf(" -S -%d", tmuxexec.DefaultScrollbackLines); !strings.Contains(string(args), want) {
		t.Fatalf("terminal display capture did not request %s: %q", want, string(args))
	}
}

func TestCodexStartSessionSetsHistoryLimit(t *testing.T) {
	fakeBin := t.TempDir()
	argsPath := fakeBin + "/tmux-args.log"
	tmuxPath := fakeBin + "/tmux"
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_ARGS"
exit 0
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_ARGS", argsPath)

	if err := startCodexTmuxSession(context.Background(), "history-session", []string{"codex"}, ""); err != nil {
		t.Fatalf("startCodexTmuxSession returned error: %v", err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read tmux args: %v", err)
	}
	want := "set-option -t history-session history-limit " + tmuxexec.DefaultHistoryLimit
	if !strings.Contains(string(args), want) {
		t.Fatalf("tmux args missing history limit %q:\n%s", want, string(args))
	}
}

func TestCodexInteractiveTimeoutDefaultsToNoDeadline(t *testing.T) {
	t.Setenv(EnvCodexInteractiveTimeoutSeconds, "")
	if got := codexInteractiveTimeout(); got != 0 {
		t.Fatalf("codexInteractiveTimeout default = %v, want 0", got)
	}

	t.Setenv(EnvCodexInteractiveTimeoutSeconds, "0")
	if got := codexInteractiveTimeout(); got != 0 {
		t.Fatalf("codexInteractiveTimeout zero env = %v, want 0", got)
	}

	t.Setenv(EnvCodexInteractiveTimeoutSeconds, "2")
	if got := codexInteractiveTimeout(); got != 2*time.Second {
		t.Fatalf("codexInteractiveTimeout env = %v, want 2s", got)
	}
}

func TestCodexInteractivePromptWaitDefaultsToStartupBudget(t *testing.T) {
	t.Setenv(tmuxlaunch.EnvPromptWaitSeconds, "")
	t.Setenv(EnvCodexInteractivePromptWaitSeconds, "")
	if got := codexInteractivePromptWait(); got != 300*time.Second {
		t.Fatalf("codexInteractivePromptWait default = %v, want 300s", got)
	}

	t.Setenv(tmuxlaunch.EnvPromptWaitSeconds, "3")
	t.Setenv(EnvCodexInteractivePromptWaitSeconds, "")
	if got := codexInteractivePromptWait(); got != 3*time.Second {
		t.Fatalf("codexInteractivePromptWait global env = %v, want 3s", got)
	}

	t.Setenv(EnvCodexInteractivePromptWaitSeconds, "2")
	if got := codexInteractivePromptWait(); got != 2*time.Second {
		t.Fatalf("codexInteractivePromptWait provider env = %v, want 2s", got)
	}
}

func TestCodexInteractiveShellCommandUsesCallerWorkingDir(t *testing.T) {
	shell := writeExecutableTestShell(t, "zsh")
	t.Setenv("CODING_AGENT_LOGIN_SHELL", shell)
	t.Setenv("CODING_AGENT_SHELL_MODE", "")

	got := codexInteractiveShellCommand([]string{"codex", "--no-alt-screen"}, "/tmp/user chat")
	if !strings.HasPrefix(got, "'"+shell+"' '-ilc' ") {
		t.Fatalf("shell command = %q, want login shell prefix", got)
	}
	if !strings.Contains(got, "'/tmp/user chat'") {
		t.Fatalf("shell command = %q, want caller cwd passed to login shell", got)
	}
	if strings.Contains(got, "--cd") {
		t.Fatalf("shell command = %q, interactive cwd must not rely on --cd", got)
	}
}

func TestCodexInteractiveShellCommandDirectMode(t *testing.T) {
	t.Setenv("CODING_AGENT_SHELL_MODE", "direct")

	got := codexInteractiveShellCommand([]string{"codex", "--no-alt-screen"}, "/tmp/user chat")
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

func TestCodexBridgeOnlyDisablesPluginAndDummyToolSurfaces(t *testing.T) {
	adapter := NewCodexCLIAdapter("", "gpt-5.3-codex-spark", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithDisableShellTool()(opts)

	args, systemPromptFile, _, err := adapter.buildCodexInteractiveArgs(opts, "")
	if err != nil {
		t.Fatalf("buildCodexInteractiveArgs error = %v", err)
	}
	if systemPromptFile != "" {
		t.Fatalf("systemPromptFile = %q, want empty", systemPromptFile)
	}

	for _, feature := range []string{"plugins", "unavailable_dummy_tools"} {
		if !codexArgsContainPair(args, "--disable", feature) {
			t.Fatalf("args missing --disable %s: %v", feature, args)
		}
	}
}

func TestCodexInteractiveArgsUseResumeCommandWhenThreadIDPresent(t *testing.T) {
	adapter := NewCodexCLIAdapter("", "gpt-5.5", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithResumeSessionID("019e2584-a35a-7100-877e-209c4518f957")(opts)

	args, systemPromptFile, _, err := adapter.buildCodexInteractiveArgs(opts, "")
	if err != nil {
		t.Fatalf("buildCodexInteractiveArgs error = %v", err)
	}
	if systemPromptFile != "" {
		t.Fatalf("systemPromptFile = %q, want empty", systemPromptFile)
	}
	want := []string{"codex", "resume", "-c", "check_for_update_on_startup=false", "--no-alt-screen", "--model", "gpt-5.5", "019e2584-a35a-7100-877e-209c4518f957"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestWriteCodexImageContentFilesFromBase64(t *testing.T) {
	raw := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	tempDir, paths, err := writeCodexImageContentFiles([]llmtypes.ImageContent{
		{
			SourceType: "base64",
			MediaType:  "image/png",
			Data:       base64.StdEncoding.EncodeToString(raw),
		},
	})
	if err != nil {
		t.Fatalf("writeCodexImageContentFiles() error = %v", err)
	}
	defer os.RemoveAll(tempDir)

	if len(paths) != 1 {
		t.Fatalf("paths = %v, want one image path", paths)
	}
	if !strings.HasSuffix(paths[0], ".png") {
		t.Fatalf("image path = %q, want .png extension", paths[0])
	}
	got, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("read image file: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("image bytes = %v, want %v", got, raw)
	}
}

func codexArgsContainPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestWriteCodexImageContentFilesRejectsURL(t *testing.T) {
	_, _, err := writeCodexImageContentFiles([]llmtypes.ImageContent{
		{SourceType: "url", Data: "https://example.com/image.png"},
	})
	if err == nil {
		t.Fatal("writeCodexImageContentFiles() error = nil, want unsupported URL error")
	}
	if !strings.Contains(err.Error(), "image URLs are not supported") {
		t.Fatalf("error = %v, want unsupported URL error", err)
	}
}

func TestCodexCLIBoundedInteractiveRejectsImageContent(t *testing.T) {
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})

	_, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Describe this image."},
				llmtypes.ImageContent{SourceType: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="},
			},
		},
	}, WithInteractiveSessionID("codex-image-test"))
	if err == nil {
		t.Fatal("GenerateContent() error = nil, want unsupported interactive image error")
	}
	if !strings.Contains(err.Error(), "interactive transport does not support llmtypes.ImageContent") {
		t.Fatalf("GenerateContent() error = %v, want interactive image unsupported error", err)
	}
}

func TestCodexCLIInteractiveIntegrationSpark(t *testing.T) {
	if os.Getenv("RUN_CODEX_CLI_INTERACTIVE_E2E") == "" {
		t.Skip("set RUN_CODEX_CLI_INTERACTIVE_E2E=1 to run real Codex CLI interactive tmux E2E")
	}
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ownerSessionID := "codex-interactive-e2e-" + codexRandomHex(4)
	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithDisableShellTool(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
	}

	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Keep answers short."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Remember the token CODEX_TMUX_OK_4821. Reply exactly: saved CODEX_TMUX_OK_4821"}}},
	}, options...)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	if got := first.Choices[0].Content; !strings.Contains(got, "CODEX_TMUX_OK_4821") {
		t.Fatalf("first content = %q, want token", got)
	}

	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What token did I ask you to remember? Reply with only the token."}}},
	}, options...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	if got := second.Choices[0].Content; !strings.Contains(got, "CODEX_TMUX_OK_4821") {
		t.Fatalf("second content = %q, want token from same tmux session", got)
	}
}

func TestExtractCodexVisibleAssistantTextFiltersTUIProgress(t *testing.T) {
	input := `
▐▛███▜▌ Codex
Thinking with high effort · esc to interrupt
Calling api-bridge… (ctrl+o to expand)
Press Ctrl+O to expand pasted text
Let me check the plan and summarize it.
Called api-bridge 2 times (ctrl+o to expand)
Here are the steps:
1. Prepare fixtures
2. Run the probes
❯
`
	got := extractCodexVisibleAssistantText(input)
	want := "Let me check the plan and summarize it.\nHere are the steps:\n1. Prepare fixtures\n2. Run the probes"
	if got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

func TestCodexTerminalTailTextFallbackUsesLatestAssistantLines(t *testing.T) {
	segments := []codexSegment{
		{Kind: codexSegmentToolStatus, Lines: []string{"• Called api-bridge.execute_shell_command"}},
		{Kind: codexSegmentAssistant, Lines: []string{"line 1", "line 2", "line 3", "line 4"}},
	}
	got := codexTerminalTailTextFallback(segments, 2)
	if got != "line 3\nline 4" {
		t.Fatalf("tail fallback = %q, want last two assistant lines", got)
	}
}

func TestCodexPolicyInvalidPromptDetection(t *testing.T) {
	pane := `
■ Invalid prompt: your prompt was flagged as potentially violating our usage policy. Please try again with a different prompt:
https://platform.openai.com/docs/guides/reasoning#advice-on-prompting

›
`
	if err := codexPolicyInvalidPromptError(pane); err == nil {
		t.Fatal("codexPolicyInvalidPromptError() error = nil, want policy rejection")
	}
}

func TestCodexPolicyInvalidPromptDetectionFromExtractedTail(t *testing.T) {
	text := "https://platform.openai.com/docs/guides/reasoning#advice-on-prompting"
	if err := codexPolicyInvalidPromptTextError(text); err == nil {
		t.Fatal("codexPolicyInvalidPromptTextError() error = nil, want policy rejection")
	}
}

func TestStripCodexHistoricalAssistantTextRemovesPaneReplay(t *testing.T) {
	previous := `Hello! I'm your Workflow Builder agent. I'm currently in the testing
workspace, where we have a regression test workflow designed to verify the
system's guardrails.
Would you like me to run it?`

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "full previous response before new answer",
			text: previous + "\nYes, I do! A message sequence is ordered.",
			want: "Yes, I do! A message sequence is ordered.",
		},
		{
			name: "suffix of previous response before new answer",
			text: `workspace, where we have a regression test workflow designed to verify the
system's guardrails.
Would you like me to run it?
Yes, I do! A message sequence is ordered.`,
			want: "Yes, I do! A message sequence is ordered.",
		},
		{
			name: "only historical suffix",
			text: `workspace, where we have a regression test workflow designed to verify the
system's guardrails.
Would you like me to run it?`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripCodexHistoricalAssistantText(tt.text, []string{previous})
			if got != tt.want {
				t.Fatalf("stripped = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripCodexEchoedUserPromptKeepsAssistantAnswer(t *testing.T) {
	token := "REAL_CODEX_TMUX_abc123"
	prompt := fmt.Sprintf(`This is a real Codex CLI tmux contract test.

Preserve input safely:

blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते

Reply exactly:
saved %s`, token, token)
	visible := fmt.Sprintf(`│ >_ OpenAI Codex (v0.130.0)                            │
│ directory: ~/ai-work/…/pkg/adapters/codexcli          │
Tip: New Use /fast to enable our fastest inference with increased plan usage.
Preserve input safely:
blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते
Reply exactly:
saved %s
saved %s
gpt-5.3-codex-spark low · ~/ai-work/multi-llm-provider-go/pkg/adapters/codexc…`, token, token, token)

	filtered := extractCodexVisibleAssistantText(visible)
	got := stripCodexEchoedUserPrompt(filtered, prompt)
	want := "saved " + token
	if got != want {
		t.Fatalf("stripped prompt = %q, want %q", got, want)
	}
}

func TestExtractCodexVisibleAssistantTextDropsLiveInputEcho(t *testing.T) {
	visible := `sent immediately)
↳ hmm
Actual answer after the live input.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Actual answer after the live input."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
}

func TestExtractCodexVisibleAssistantTextDropsCodexLandingURL(t *testing.T) {
	visible := `https://chatgpt.com/codex?app-landing-page=true
Here are the current top-level steps in the plan.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Here are the current top-level steps in the plan."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
}

func TestExtractCodexVisibleAssistantTextDropsToolStatusReplay(t *testing.T) {
	visible := `Called codex.list_mcp_resources({"cursor":null})
└ {"resources": []}
Searching the web
Searched https://example.com/
Called workspace.list_mcp_resources({"cursor":null,"server":"workspace"})
└ Error: resources/list failed: unknown MCP server 'workspace'
Called codex.list_mcp_resource_templates({})
└ {"resourceTemplates": []}
Called
└ workflow.read_mcp_resource({"server":"workflow","uri":"planning/plan.json"})
Error: resources/read failed: unknown MCP server 'workflow'
Updated Plan
└ quick check
✔ test
Updated Plan
└ □ try
Spawned Dalton (gpt-5.3-codex-spark high)
└ ping
Waiting for Dalton
Finished waiting
└ Dalton: Completed - pong
Now I will run the workflow step.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Now I will run the workflow step."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
}

func TestExtractCodexVisibleAssistantTextDropsFlattenedToolStatusReplay(t *testing.T) {
	visible := `Hi! 👋 What would you like me to do for the workflow today? Called codex.list_mcp_resources({}) └ {"resources": []} Updated Plan └ Need confirm target group and step before running. □ Gather available groups and step IDs. □ Request step/group details from user. Called codex.list_mcp_resource_templates({}) └ {"resourceTemplates": []} Called └ workflow.read_mcp_resource({"server":"workflow","uri":"planning/plan.json"}) Error: resources/read failed: unknown MCP server 'workflow' Updated Plan └ Fetching available context before running requested step. ✔ Gather available groups and step IDs. □ Request step/group details from user. Updated Plan └ Need to identify valid groups and steps before execution. ✔ Gather available groups and step IDs. ✔ Request step/group details from user.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Hi! 👋 What would you like me to do for the workflow today?"
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

// Codex CLI 0.137.0 renders a footer hint "shift + ← edit last queued message"
// below the input box. Before the chrome-list update it leaked into the
// extracted assistant text (e.g. the parallel-isolation e2e captured it as the
// model reply). This locks that it's now classified as terminal chrome.
func TestExtractCodexVisibleAssistantTextDropsQueuedMessageFooterHint(t *testing.T) {
	visible := `PAR_LEFT_89997b00
─────────────────────────────────────
›
  shift + ← edit last queued message`

	got := extractCodexVisibleAssistantText(visible)
	want := "PAR_LEFT_89997b00"
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
}

// Codex wraps a long user prompt across terminal lines: only the FIRST line
// carries the "›" marker; continuation lines are indented with no marker. The
// "›" line is correctly dropped as chrome, but the wrapped tail used to start a
// fresh assistant segment and leak into the extracted answer. This locks that
// the wrap is absorbed into the prompt chrome and the real answer is returned.
func TestExtractCodexVisibleAssistantTextDropsWrappedPromptContinuation(t *testing.T) {
	visible := `› Call the api-bridge slow_contract MCP tool with token SLOW_CODEX_QUEUE_09d66aa8 and delay_ms 8000. Do not answer
  until the tool returns. Then reply exactly CODEX_FIRST_DONE_5e322e38.
• Called api-bridge.slow_contract({"token":"SLOW_CODEX_QUEUE_09d66aa8","delay_ms":8000})
  └ SLOW_BRIDGE_TOOL_OK_SLOW_CODEX_QUEUE_09d66aa8
› Follow-up task: after the current answer completes, also reply exactly CODEX_LIVE_ACK_891ecff1 and nothing else.
• CODEX_FIRST_DONE_5e322e38`

	got := extractCodexVisibleAssistantText(visible)
	if strings.Contains(got, "until the tool returns") {
		t.Fatalf("wrapped prompt tail leaked into assistant text: %q", got)
	}
	want := "CODEX_FIRST_DONE_5e322e38"
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
}

func TestExtractCodexVisibleAssistantTextDropsToolReplayFragments(t *testing.T) {
	visible := `ver"})
environment.
□ Check current model auth status
bridge.get_api_spec({"server_name":"llm_config_tools","tool_name":"list_llm_capabilities"})
base: http://127.0.0.1:18743/s/session-id
auth: Bearer $MCP_API_TOKEN
POST /tools/custom/list_llm_capabilities
# List supported and currently usable LLM providers/models by capability.
tored. Supports optional provider override, aspect ratio, resolution, number of im...
mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-model-test && curl -sS -X POST "$MCP_API_URL/tools/custom/image_gen" -H "Authorization: Bearer $MCP_API_TOKEN" -H "Content-Type: application/json" -d '{"provider":"vertex","model_id":"gemini-3.1-flash-image-preview","prompt":"A calm cyberpunk city skyline","aspect_ratio":"16:9","resolution":"1K","number_of_images":1,"output_path":"_users/default/Chats/image-model-test/vertex_test.png"}'"})
{"stdout": "", "stderr": "mkdir: /Users/mipl/ai-work/mcp-agent-builder-go: Operation not permitted\n", "exit_code": 1, "execution_time_ms": 30}
32
-rw-r--r--@ 1 mipl staff 0 30 Apr 15:42 _index.json
drwxr-xr-x@ 3 mipl staff 96 9 May 19:55 _system
&& ls -l Chats/test.txt"})
{"stdout": "", "stderr": "touch: Chats/test.txt: Operation not permitted\n", "exit_code": 1, "execution_time_ms": 27}
Here is the actual answer.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Here is the actual answer."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestExtractCodexVisibleAssistantTextDropsModelCatalogAndShellReplay(t *testing.T) {
	visible := `catalog the frontend uses from /api/llm-config/models/metadata.
provider: string (required)
relative output_path so the caller decides exactly where the generated image is stored.
json, requests
base='http://127.0.0.1:18743/s/session-id'
url=base+'/tools/custom/list_provider_models'
headers={'Authorization':'Bearer '+os.environ['MCP_API_TOKEN'],'Content-Type':'application/json'}
for p in ['codex-cli','minimax-coding-plan','vertex','openai']:
 r=requests.post(url,headers=headers,json={'provider':p},timeout=60)
 print(json.dumps(r.json(),indent=2)[:2000])
"http://127.0.0.1:18743/s/session-id/tools/custom/list_provider_models"
{
  "count": 4,
  "models": [
    {
      "model_id": "pricing varies)",
      "context_window": 200000,
      "input_cost_per_1m": 0,
      "output_cost_per_1m": 0
    }
  ]
}
absolute host path (/Users/mipl/.codex/skills/.system/imagegen/SKILL.md) docs, /workspace-docs. Did you mean: /Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/skills/.system/imagegen/SKILL.md?
model-test && for m in low medium high; do
  echo "Generating with model: $m"
  payload='{"provider":"codex-cli","model_id":"'$m'","prompt":"A futuristic neon cityscape","aspect_ratio":"16:9","output_path":"Chats/image-model-test/'"$m"'.png"}'
32
-rw-r--r--@ 1 mipl staff 0 30 Apr 15:42 _index.json
drwxr-xr-x@ 3 mipl staff 96 9 May 19:55 _system
Actual concise answer.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Actual concise answer."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestExtractCodexVisibleAssistantTextDropsBulletedGenericMCPResourceReplay(t *testing.T) {
	visible := `Generating...
• Called list.read_mcp_resource({"server":"list","uri":"bad"})
Error: resources/read failed: unknown MCP server 'list'
• • •
I need the step name or group before I can run it.`

	got := extractCodexVisibleAssistantText(visible)
	want := "I need the step name or group before I can run it."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestNormalizeCodexPaneSnapshotSegmentsAssistantAndStatusBlocks(t *testing.T) {
	raw := `Hi, I can help with that.
Called codex.list_mcp_resources({"cursor":null})
└ {"resources": []}
Updated Plan
└ Need valid step details.
□ Ask user for step.
• Called list.read_mcp_resource({"server":"list","uri":"bad"})
Error: resources/read failed: unknown MCP server 'list'
Now I need the step name before running it.`

	snapshot := normalizeCodexPaneSnapshot(raw)
	wantAssistant := `Hi, I can help with that.
Now I need the step name before running it.`
	if snapshot.AssistantText != wantAssistant {
		t.Fatalf("assistant text = %q, want %q", snapshot.AssistantText, wantAssistant)
	}
	wantKinds := []codexSegmentKind{
		codexSegmentAssistant,
		codexSegmentToolStatus,
		codexSegmentPlanStatus,
		codexSegmentToolStatus,
		codexSegmentAssistant,
	}
	if len(snapshot.Segments) != len(wantKinds) {
		t.Fatalf("segments = %#v, want %d segments", snapshot.Segments, len(wantKinds))
	}
	for i, want := range wantKinds {
		if snapshot.Segments[i].Kind != want {
			t.Fatalf("segment %d kind = %q, want %q; segments=%#v", i, snapshot.Segments[i].Kind, want, snapshot.Segments)
		}
	}
	if snapshot.Fingerprint == "" || strings.Contains(snapshot.Fingerprint, "read_mcp_resource") {
		t.Fatalf("fingerprint = %q, want assistant-only fingerprint", snapshot.Fingerprint)
	}
}

func TestParseCodexInteractiveResponseDropsInternalToolReplay(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
Calling codex.list_mcp_resources({"cursor":null})
Called codex.list_mcp_resources({"cursor":null})
└ {"resources": []}
Searching the web
Searched https://example.com/
Called workspace.list_mcp_resources({"cursor":null,"server":"workspace"})
└ Error: resources/list failed: unknown MCP server 'workspace'
Updated Plan
└ quick check
✔ test
Called
└ workflow.read_mcp_resource({"server":"workflow","uri":"planning/plan.json"})
Error: resources/read failed: unknown MCP server 'workflow'
Here are the current top-level steps in the plan:
1. Prepare Regression Fixtures
2. Forbidden Access Probe
3. Execution Regression Router
›`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Here are the current top-level steps in the plan:
1. Prepare Regression Fixtures
2. Forbidden Access Probe
3. Execution Regression Router`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseKeepsOnlyFinalAnswerAfterShellReplay(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
this step's allowed folders. Allowed: _users/default/Chats/, _users/
default/chat_history/, skills/, subagents/, Downloads/, Workflow/,
Cannot read from ".". Wr...
32\n-rw-r--r--@  1 mipl  staff     0 30 Apr 15:42 _index.json\ndrwxr-xr-
x@  3 mipl  staff    96  9 May 19:55 _system\ndrwxr-xr-x   4 mipl  staff
128 10 May 09:34 _users\ndrwxr-xr-x@ 18 mipl  staff   576 11 May 10:46 .
\n-rw-r--r--@  1 mipl  staff  6148 10 May 09:34 .DS_Store\ndrwxr-xr-x
staff     0 30 Apr 15:42 _index.json\ndrwxr-xr-x@  3 mipl  staff    96
9 May 19:55 _system\ndrwxr-xr-x   4 mipl  staff   128 10 May 09:34
_users\ndrwxr-xr-x@ 18 mipl  staff   576 11 May 10:46 .\n-rw-r--r--@  1
mipl  staff  6148 10 May 09:34 .DS_Store\ndrwxr-xr-x  18 mipl  staff
(depth<=2):' && find . -maxdepth 2 -mindepth 1 -type d 2>/dev/null |
default\n./.git\n./.git/filter-repo\n./.git/gk\n./.git/hooks\n./.git/
info\n./.git/logs\n./.git/objects\n./.git/refs\n./Chats\n./config\n./
config/whatsapp-sessions\n./knowledgebase\n./knowledgebase/notes\n./
learnings\n./learnings/_global\n./skills\n./skills/agent-browser\n./
skills/ai-social-media-conte...
Here’s what’s in the current workspace root:
Files
- _index.json
- .DS_Store
- .gitignore
- SKILL.md
- skills-lock.json
Folders
- _system, _users, Chats, config, Downloads (symlink), knowledgebase,
learnings, skills, subagents, Workflow
I can also list files/folders inside any one of those (e.g. Chats or Workflow)
if you want a full breakdown.
›`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Here’s what’s in the current workspace root:
Files
- _index.json
- .DS_Store
- .gitignore
- SKILL.md
- skills-lock.json
Folders
- _system, _users, Chats, config, Downloads (symlink), knowledgebase,
learnings, skills, subagents, Workflow
I can also list files/folders inside any one of those (e.g. Chats or Workflow)
if you want a full breakdown.`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponsePrefersSeparatorFramedFinalAnswer(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
Calling codex.list_mcp_resources({"cursor":null})
Called codex.list_mcp_resources({"cursor":null})
└ {"resources": []}
Intermediate assistant-looking replay near tool output
────────────────────────────────────────────────────────────────────────────────
Here is the final answer:
- alpha
- beta
────────────────────────────────────────────────────────────────────────────────
❯`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Here is the final answer:
- alpha
- beta`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	testcontracts.AssertCleanFinalExtraction(t, "codex-cli", got,
		[]string{"Here is the final answer:", "- alpha", "- beta"},
		[]string{
			"Calling codex.list_mcp_resources",
			"Called codex.list_mcp_resources",
			"Intermediate assistant-looking replay",
			"codex.list_mcp_resources",
			"resources",
		},
	)
	assertCodexNoInternalStatus(t, got)
}

func TestCodexFinalExtractionVertexJudgeE2E(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
Calling codex.list_mcp_resources({"cursor":null})
Called codex.list_mcp_resources({"cursor":null})
└ {"resources": []}
Intermediate assistant-looking replay near tool output
────────────────────────────────────────────────────────────────────────────────
Here is the final answer:
- alpha
- beta
────────────────────────────────────────────────────────────────────────────────
❯`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	testcontracts.AssertVertexJudgesFinalExtraction(t, testcontracts.FinalExtractionJudgeCase{
		Provider:   "codex-cli",
		TmuxScreen: captured,
		Extracted:  got,
		UserGoal:   "Return the final answer list.",
		MustContain: []string{
			"Here is the final answer:",
			"- alpha",
			"- beta",
		},
		Forbidden: []string{
			"codex.list_mcp_resources",
			"resources",
			"Intermediate assistant-looking replay",
			"❯",
		},
		ExpectedNote: "The answer should preserve the heading and bullet list from the final framed response.",
	})
}

func TestParseCodexInteractiveResponseRejectsSeparatorFramedToolTail(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
• Called
  └ api-bridge.execute_shell_command({"command":"python3 - <<'PY'\nimport json\nprint(json.dumps({'ok': True}, indent=2))\nPY","timeout":120})
    {"stdout":"{\"ok\": true}\n","stderr":"","exit_code":0,"execution_time_ms":42}

────────────────────────────────────────────────────────────────────────────────

indent=2))\nPY","timeout":120})

────────────────────────────────────────────────────────────────────────────────

›`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	if got != "" {
		t.Fatalf("parsed response = %q, want empty string for tool-call tail", got)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseKeepsUnframedSavedImagePath(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
The generated image is saved at:

/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-generation/random-anything.png

(Equivalent relative path from workspace root: _users/default/Chats/image-generation/random-anything.png.)

› Find and fix a bug in @filename

gpt-5.3-codex-spark high · ~/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats
`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `The generated image is saved at:
/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-generation/random-anything.png
(Equivalent relative path from workspace root: _users/default/Chats/image-generation/random-anything.png.)`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseKeepsFramedReadImagePath(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
• Called
  └ api-bridge.execute_shell_command({"command":"curl -sS -X POST \"$MCP_API_URL/tools/custom/read_image\" ..."})
    {"stdout": "{\"filepath\":\"/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-model-test/vertex_1.jpg\",\"response\":\"...\"}"}

────────────────────────────────────────────────────────────────────────────────

• Yep — I found an image in the workspace and read it.

  I analyzed this image:

  /Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-model-test/vertex_1.jpg

  ### What it is

  A wide cyberpunk-style cityscape at dusk with a dark balcony foreground.

  ### Readable text detected

  - OBERNETICS
  - arasaka
  - NEO-VERIDIA

────────────────────────────────────────────────────────────────────────────────

› Improve documentation in @filename

gpt-5.3-codex-spark high · ~/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats
`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Yep — I found an image in the workspace and read it.
I analyzed this image:
/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-model-test/vertex_1.jpg
### What it is
A wide cyberpunk-style cityscape at dusk with a dark balcony foreground.
### Readable text detected
- OBERNETICS
- arasaka
- NEO-VERIDIA`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseKeepsFramedWorkspacePath(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
	────────────────────────────────────────────────────────────────────────────────

• Here are the files/folders in:

  /Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats

  - Folders: analysis, chat-system-summary, daily-summary, generated-images,
    skills, workflows.
  - Files: .writetest, find_step.py, fix_markdown.py, report_plan.md.

────────────────────────────────────────────────────────────────────────────────
❯`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Here are the files/folders in:
/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats
- Folders: analysis, chat-system-summary, daily-summary, generated-images,
skills, workflows.
- Files: .writetest, find_step.py, fix_markdown.py, report_plan.md.`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseIgnoresRateLimitReminderModal(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
› Take note of the exact token E2E_NOTE_deadbeef. Do not use tools. Reply with
  exactly ACK_E2E_NOTE_deadbeef and nothing else.

⚠ Heads up, you have less than 5% of your 5h limit left. Run /status for a breakdown.

• ACK_E2E_NOTE_deadbeef


  Approaching rate limits
  Switch to gpt-5.4-mini for lower credit usage?

› 1. Switch to gpt-5.4-mini                 Small, fast, and cost-efficient model for simpler coding tasks.
  2. Keep current model
  3. Keep current model (never show again)  Hide future rate limit reminders about switching models.

  Press enter to confirm or esc to go back
`

	if !hasCodexRateLimitReminderModal(captured) {
		t.Fatalf("rate limit reminder modal was not detected")
	}
	if hasCodexReadyPrompt(captured) {
		t.Fatalf("rate limit reminder selected option must not be treated as ready prompt")
	}
	if got := selectedCodexRateLimitReminderOption(captured); got != 1 {
		t.Fatalf("selected reminder option = %d, want 1", got)
	}

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := "ACK_E2E_NOTE_deadbeef"
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestCodexWorkspaceTrustPromptIsNotReady(t *testing.T) {
	pane := `> You are in /private/tmp/codex-trust-check

  Do you trust the contents of this directory? Working with untrusted contents comes with higher risk of prompt
  injection. Trusting the directory allows project-local config, hooks, and exec policies to load.

› 1. Yes, continue
  2. No, quit

  Press enter to continue`

	if !hasCodexTrustPrompt(pane) {
		t.Fatal("trust prompt was not detected")
	}
	if hasCodexReadyPrompt(pane) {
		t.Fatal("trust prompt must not be treated as ready")
	}
	if got := selectedCodexTrustPromptOption(pane); got != 1 {
		t.Fatalf("selected trust option = %d, want 1", got)
	}
	got := parseCodexInteractiveResponse(pane, "", "", nil)
	if got != "" {
		t.Fatalf("parsed trust prompt = %q, want empty", got)
	}
}

func TestCodexWorkspaceTrustPromptSelectedNoUsesPreviousOption(t *testing.T) {
	pane := `Do you trust the contents of this directory?
  1. Yes, continue
› 2. No, quit
  Press enter to continue`

	if got := selectedCodexTrustPromptOption(pane); got != 2 {
		t.Fatalf("selected trust option = %d, want 2", got)
	}
}

func TestCodexAcceptedWorkspaceTrustPromptScrollbackIsNotLiveTrustPrompt(t *testing.T) {
	pane := `> You are in /tmp/workspace

  Do you trust the contents of this directory? Working with untrusted contents comes with higher risk of prompt injection.

› 1. Yes, continue
  2. No, quit

  Press enter to continue

╭──────────────────────────────────────────────────────────╮
│ >_ OpenAI Codex (v0.130.0)                               │
╰──────────────────────────────────────────────────────────╯

› Explain this codebase

  gpt-5.3-codex-spark low · /tmp/workspace`

	if hasCodexTrustPrompt(pane) {
		t.Fatal("accepted trust prompt scrollback must not be treated as live trust prompt")
	}
	if !hasCodexReadyPrompt(pane) {
		t.Fatal("pane with later Codex input prompt should be ready")
	}
}

func TestCodexIdleDetectionIgnoresAssistantProseAboutRunning(t *testing.T) {
	pane := `
	⏺ The prepare-test-fixtures step is now running in the background.
	  I will wait for the automatic notification before proceeding.

────────────────────────────────────────────────────────────────────────────────
❯
`
	if !hasCodexReadyPrompt(pane) {
		t.Fatalf("ready prompt not detected")
	}
	if hasCodexActivity(pane) {
		t.Fatalf("assistant prose containing running should not count as active TUI state")
	}
	if isCodexTUILine("The prepare-test-fixtures step is now running in the background.") {
		t.Fatalf("assistant prose containing running should not be filtered as TUI chrome")
	}
}

func TestCodexQueuedInputKeepsSessionActive(t *testing.T) {
	pane := `
• Calling api-bridge.execute_shell_command({"command":"python3 slow.py"})

■ Ctrl+L is disabled while a task is in progress.

• Working (6m 32s • esc to interrupt)

• Messages to be submitted after next tool call (press esc to interrupt and send immediately)
  ↳ ## Pre-validation failed (retry attempt 3)

    ❌ PRE-VALIDATION FAILED

────────────────────────────────────────────────────────────────────────────────
›
`
	if !hasCodexQueuedInput(pane) {
		t.Fatalf("queued input was not detected")
	}
	if !hasCodexActivity(pane) {
		t.Fatalf("queued input should keep session active")
	}
	if hasCodexReadyPrompt(pane) {
		t.Fatalf("queued input must not be treated as ready/completed prompt")
	}
	if !isCodexTUILine("Messages to be submitted after next tool call (press esc to interrupt and send immediately)") {
		t.Fatalf("queued-input banner should be treated as TUI chrome")
	}
}

func TestCodexHistoricalQueuedInputDoesNotBlockLaterPrompt(t *testing.T) {
	pane := `
• Calling api-bridge.execute_shell_command({"command":"python3 slow.py"})

■ Ctrl+L is disabled while a task is in progress.

• Working (6m 32s • esc to interrupt)

• Messages to be submitted after next tool call (press esc to interrupt and send immediately)
  ↳ ## Pre-validation failed (retry attempt 3)

    ❌ PRE-VALIDATION FAILED

────────────────────────────────────────────────────────────────────────────────

• Restarted.

  There were no running executions left to cancel, so I started a fresh full workflow run.

────────────────────────────────────────────────────────────────────────────────

› Run /review on my current changes

  gpt-5.5 xhigh · ~/ai-work/mcp-agent-builder-go/workspace-docs/Workflow/instagram
`
	if hasCodexQueuedInput(pane) {
		t.Fatalf("historical queued-input banner must not keep a later prompt blocked")
	}
	if hasCodexActivity(pane) {
		t.Fatalf("historical queued-input banner must not keep the pane active")
	}
	if !hasCodexReadyPrompt(pane) {
		t.Fatalf("later prompt should be considered ready")
	}
}

func TestCodexActiveStatusAboveLongToolOutputKeepsSessionActive(t *testing.T) {
	var filler strings.Builder
	for i := 0; i < 48; i++ {
		fmt.Fprintf(&filler, "tool output line %02d\n", i)
	}
	pane := `
• Calling api-bridge.execute_shell_command({"command":"python3 slow.py"})

• Working (6m 49s • esc to interrupt)
` + filler.String() + `
────────────────────────────────────────────────────────────────────────────────
›
`
	if !hasCodexActivity(pane) {
		t.Fatalf("active status above long tool output should keep session active")
	}
	if hasCodexReadyPrompt(pane) {
		t.Fatalf("ready prompt must be ignored while active status remains in the current turn")
	}
}

func TestCodexCompletedStatusAllowsReadyPromptDespiteOldWorkingLine(t *testing.T) {
	pane := `
› can you list all files

• Working (14s • esc to interrupt)

⏺ Here are the files.

✻ Cogitated for 14s

────────────────────────────────────────────────────────────────────────────────
›
`
	if hasCodexActivity(pane) {
		t.Fatalf("completed turn should not be kept active by old working status")
	}
	if !hasCodexReadyPrompt(pane) {
		t.Fatalf("completed turn with bottom prompt should be ready")
	}
}

func TestParseCodexInteractiveResponseRejectsQueuedValidationEcho(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
• Messages to be submitted after next tool call (press esc to interrupt and send immediately)
  ↳ ## Pre-validation failed (retry attempt 3)

❌ PRE-VALIDATION FAILED

Checks: 0 passed, 1 failed

Fix the specific issues above and re-produce the required outputs.

────────────────────────────────────────────────────────────────────────────────
›
`
	got := parseCodexInteractiveResponse(captured, baseline, "## Pre-validation failed (retry attempt 3)", nil)
	if got != "" {
		t.Fatalf("parsed queued validation echo = %q, want empty", got)
	}
}

func TestCodexTUIFilterKeepsAssistantProseAboutTokens(t *testing.T) {
	if isCodexTUILine("Tokenizer behavior depends on how many tokens are in the prompt.") {
		t.Fatalf("assistant prose about tokens should not be filtered as Codex TUI chrome")
	}

	if !isCodexTUILine("· Working (9s · ↑ 363 tokens · thinking with high effort)") {
		t.Fatalf("Codex token/status line should still be filtered as TUI chrome")
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

type codexDrainedStream struct {
	content        string
	terminalCount  int
	terminalSample string
}

func drainCodexStream(streamChan <-chan llmtypes.StreamChunk) codexDrainedStream {
	var parts []string
	var drained codexDrainedStream
	for {
		select {
		case chunk, ok := <-streamChan:
			if !ok {
				drained.content = strings.TrimSpace(strings.Join(parts, ""))
				return drained
			}
			switch chunk.Type {
			case llmtypes.StreamChunkTypeContent:
				parts = append(parts, chunk.Content)
			case llmtypes.StreamChunkTypeTerminal:
				drained.terminalCount++
				if drained.terminalSample == "" {
					drained.terminalSample = chunk.Content
				}
			}
		default:
			drained.content = strings.TrimSpace(strings.Join(parts, ""))
			return drained
		}
	}
}

func assertCodexInteractiveTerminalOnlyStream(t *testing.T, streamChan <-chan llmtypes.StreamChunk) {
	t.Helper()
	drained := drainCodexStream(streamChan)
	if drained.content != "" {
		t.Fatalf("interactive stream emitted assistant-content chunk %q; want terminal snapshots only", drained.content)
	}
	if drained.terminalCount == 0 {
		t.Fatalf("interactive stream emitted no terminal snapshots")
	}
}

func assertCodexStreamQuality(t *testing.T, streamed, want string) {
	t.Helper()
	if !strings.Contains(streamed, want) {
		t.Fatalf("streamed content = %q, want assistant response containing %q", streamed, want)
	}
	assertCodexNoInternalStatus(t, streamed)
}

func assertCodexNoInternalStatus(t *testing.T, streamed string) {
	t.Helper()
	for _, noisy := range []string{
		"Generating",
		"esc to interrupt",
		"Ctrl+O",
		"ctrl+o",
		"pasted text",
		"Codex",
		"api-bridge",
		"read_mcp_resource",
		"list_mcp_resources",
		"list_mcp_resource_templates",
		"codex.list_mcp_resources",
		"workspace.list_mcp_resources",
		"Searching the web",
		"Searched https://",
		"Updated Plan",
		"Spawned ",
		"Waiting for ",
		"Finished waiting",
		"unknown MCP server",
		"execute_shell_command",
		"exit_code",
		"stdout",
		"stderr",
		"MCP_API_URL",
		"MCP_API_TOKEN",
		"Authorization: Bearer",
		"Authorization",
		"127.0.0.1",
		"/api/llm-config/models/metadata",
		"list_provider_models",
		"model_id",
		"input_cost_per_1m",
		"absolute host path",
		"writable folders",
		"Generating with model",
		"tmux focus-events",
	} {
		if strings.Contains(streamed, noisy) {
			t.Fatalf("streamed content = %q, should not contain TUI noise %q", streamed, noisy)
		}
	}
}
