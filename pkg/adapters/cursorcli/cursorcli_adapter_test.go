package cursorcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

func TestCursorCLIAdapterImplementsWebSearchModel(t *testing.T) {
	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	if _, ok := interface{}(adapter).(llmtypes.WebSearchModel); !ok {
		t.Fatal("CursorCLIAdapter should implement llmtypes.WebSearchModel")
	}
}

func TestSendCursorControlIfVisibleRechecksPaneInsideBroker(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "send-keys.log")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
if [ "$1" = "capture-pane" ]; then
  printf '%s\n' "$FAKE_TMUX_CAPTURE"
  exit 0
fi
if [ "$1" = "send-keys" ]; then
  printf '%s\n' "$*" >> "$FAKE_TMUX_LOG"
fi
exit 0
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_TMUX_LOG", logPath)

	t.Setenv("FAKE_TMUX_CAPTURE", "normal cursor composer")
	handled, err := sendCursorControlIfVisible(context.Background(), "cursor-control-stale", "test", hasCursorMCPToolApprovalPrompt, "Tab")
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("stale approval was handled after the modal disappeared")
	}
	if data, err := os.ReadFile(logPath); err == nil && strings.TrimSpace(string(data)) != "" {
		t.Fatalf("stale approval injected keys: %q", data)
	}

	t.Setenv("FAKE_TMUX_CAPTURE", "Run this MCP tool?\nAllowlist MCP Tool (tab)")
	handled, err = sendCursorControlIfVisible(context.Background(), "cursor-control-visible", "test", hasCursorMCPToolApprovalPrompt, "Tab")
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("visible approval modal was not handled")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "send-keys -t cursor-control-visible Tab") {
		t.Fatalf("send-keys log = %q", data)
	}
}

func TestCursorInteractiveTimeoutDefaultsToNoDeadline(t *testing.T) {
	t.Setenv(EnvCursorInteractiveTimeoutSeconds, "")
	if got := cursorInteractiveTimeout(); got != 0 {
		t.Fatalf("cursorInteractiveTimeout default = %v, want 0", got)
	}

	t.Setenv(EnvCursorInteractiveTimeoutSeconds, "0")
	if got := cursorInteractiveTimeout(); got != 0 {
		t.Fatalf("cursorInteractiveTimeout zero env = %v, want 0", got)
	}

	t.Setenv(EnvCursorInteractiveTimeoutSeconds, "2")
	if got := cursorInteractiveTimeout(); got != 2*time.Second {
		t.Fatalf("cursorInteractiveTimeout env = %v, want 2s", got)
	}
}

func TestCursorInteractivePromptWaitDefaultsToStartupBudget(t *testing.T) {
	t.Setenv(tmuxlaunch.EnvPromptWaitSeconds, "")
	t.Setenv(EnvCursorInteractivePromptWaitSeconds, "")
	if got := cursorInteractivePromptWait(); got != 300*time.Second {
		t.Fatalf("cursorInteractivePromptWait default = %v, want 300s", got)
	}

	t.Setenv(tmuxlaunch.EnvPromptWaitSeconds, "3")
	t.Setenv(EnvCursorInteractivePromptWaitSeconds, "")
	if got := cursorInteractivePromptWait(); got != 3*time.Second {
		t.Fatalf("cursorInteractivePromptWait global env = %v, want 3s", got)
	}

	t.Setenv(EnvCursorInteractivePromptWaitSeconds, "2")
	if got := cursorInteractivePromptWait(); got != 2*time.Second {
		t.Fatalf("cursorInteractivePromptWait provider env = %v, want 2s", got)
	}
}

func TestHasCursorWebSearchApprovalPrompt(t *testing.T) {
	panes := []string{`┌──────────────────────────────┐
│ 🔍 Web Search: capital of France │
└──────────────────────────────┘

Allow this web search?
 → Allow search (y)
   Auto-run everything (shift+tab)
   Skip (esc or n)`,
		`┌──────────────────────────────┐
│ 🌐 Open URL: https://example.com │
└──────────────────────────────┘

Open this URL?
 → Open (y)
   Skip (esc or n)`,
		`Allow opening URL https://example.com?
 → Open link (y)
   Skip (esc or n)`,
		`┌──────────────────────────────┐
│ 🌐 Web Fetch: https://example.com │
└──────────────────────────────┘

Allow this web fetch?
 → Fetch (y)
   Always allow example.com (tab)
   Skip (esc or n)`,
		`$ open "https://example.com/?cursor_approval_test=abc123" Waiting for approval...

────────────────────────────────────────────────────────
 $  open "https://example.com/?cursor_approval_test=abc123" in /tmp/work

 Run this command?
 Not in allowlist: open
  → Run (once) (y)
    Add Shell(open) to allowlist? (tab)
    Run Everything (shift+tab)
    Skip (esc or n)`,
	}

	for _, pane := range panes {
		if !hasCursorWebSearchApprovalPrompt(pane) {
			t.Fatalf("expected Cursor web-access approval prompt to be detected:\n%s", pane)
		}
		if hasCursorReadyPrompt(pane) {
			t.Fatalf("web-access approval prompt must not be treated as a ready prompt:\n%s", pane)
		}
	}
}

func TestHasCursorModeSwitchPrompt(t *testing.T) {
	pane := `╭─────────────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│ Subagent prompt: List and update schedules                                                                      │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯

┌─────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│ Switch to Agent mode?                                                                                           │
│                                                                                                                 │
│ Built-in shell/read tools are blocked in subagent session; switching to agent mode may restore MCP bridge       │
│ access needed to update schedules.                                                                              │
│                                                                                                                 │
│  → Approve mode switch (y)                                                                                      │
│    Reject (n or esc)                                                                                            │
└─────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘`

	if !hasCursorModeSwitchPrompt(pane) {
		t.Fatalf("expected Cursor mode-switch prompt to be detected:\n%s", pane)
	}
	if hasCursorReadyPrompt(pane) {
		t.Fatalf("mode-switch prompt must not be treated as a ready prompt:\n%s", pane)
	}
}

func TestCursorInteractiveStreamTmuxScreenFlag(t *testing.T) {
	t.Setenv(EnvCursorInteractiveStreamTmuxScreen, "")
	if !cursorInteractiveStreamTmuxScreenEnabled() {
		t.Fatal("tmux screen streaming should be enabled by default")
	}

	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(EnvCursorInteractiveStreamTmuxScreen, value)
		if !cursorInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be enabled for %q", value)
		}
	}

	for _, value := range []string{"0", "false", "FALSE", "no", "off"} {
		t.Setenv(EnvCursorInteractiveStreamTmuxScreen, value)
		if cursorInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be disabled for %q", value)
		}
	}
}

func TestCursorTerminalStreamCapturesRawScreenRows(t *testing.T) {
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
	if !streamCursorTerminalSnapshot(context.Background(), "raw-display-session", stream, &last) {
		t.Fatal("streamCursorTerminalSnapshot returned false")
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

func TestCursorStartSessionSetsHistoryLimit(t *testing.T) {
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

	if err := startCursorTmuxSession(context.Background(), "history-session", []string{"cursor-agent"}, nil, t.TempDir()); err != nil {
		t.Fatalf("startCursorTmuxSession returned error: %v", err)
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

func TestCursorResetPaneForTurnPreservesScrollback(t *testing.T) {
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

	resetCursorPaneForTurn(context.Background(), "history-session")
	args, err := os.ReadFile(argsPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read tmux args: %v", err)
	}
	if strings.Contains(string(args), "clear-history") {
		t.Fatalf("resetCursorPaneForTurn should preserve tmux history, got args:\n%s", string(args))
	}
}

func TestCursorTmuxSessionLostErrorDetection(t *testing.T) {
	for _, message := range []string{
		"can't find pane: mlp-cursor-cli-int-123",
		"can't find session: mlp-cursor-cli-int-123",
		"no server running on /private/tmp/tmux-501/default",
		"no current target",
	} {
		if !isCursorTmuxSessionLostError(fmt.Errorf("%s", message)) {
			t.Fatalf("expected session loss detection for %q", message)
		}
	}
	if isCursorTmuxSessionLostError(nil) {
		t.Fatal("nil error must not be classified as session loss")
	}
	if !isCursorTmuxSessionLostError(llmtypes.WrapCodingAgentTmuxSessionLostError(fmt.Errorf("wrapped"), "cursor-cli", "mlp-cursor-cli-int-123", "tmux session lost")) {
		t.Fatal("typed tmux session lost error must be classified as session loss")
	}
	if isCursorTmuxSessionLostError(fmt.Errorf("permission denied")) {
		t.Fatal("unrelated errors must not be classified as session loss")
	}
}

func TestCursorWorkspaceTrustPromptIsNotReady(t *testing.T) {
	pane := `Tip: You can start the Cursor CLI with agent.
⚠ Workspace Trust Required
This will also enable the MCP servers configured for this workspace.
Do you trust the contents of this directory?
/tmp/workspace
[a] Trust this workspace
[w] Trust this workspace, but don't enable all MCP servers
[q] Quit
→ Plan, search, build anything
Ask (shift+tab to cycle)`

	if !hasCursorTrustPrompt(pane) {
		t.Fatalf("trust prompt was not detected")
	}
	if hasCursorReadyPrompt(pane) {
		t.Fatalf("trust prompt must not be treated as ready")
	}
	if got := cursorTrustPromptResponse(pane); got != "a" {
		t.Fatalf("trust prompt response = %q, want a", got)
	}
}

func TestCursorTrustingWorkspaceStateIsNotLiveTrustPrompt(t *testing.T) {
	pane := `⚠ Workspace Trust Required
[a] Trust this workspace
⏳ Trusting workspace...

Cursor Agent
Ask (shift+tab to cycle)`

	if hasCursorTrustPrompt(pane) {
		t.Fatal("post-accept trusting state must not be treated as a live trust prompt")
	}
	if !hasCursorReadyPrompt(pane) {
		t.Fatal("post-accept trusting state should allow ready prompt detection")
	}
}

func TestCursorDetectorsIgnoreStaleScrollbackOutsideVisiblePane(t *testing.T) {
	pane := `⚠ Workspace Trust Required
Do you trust the contents of this directory?
[a] Trust this workspace
Thinking...
` + strings.Repeat("ordinary completed output\n", 40) + `
→ Add a follow-up

Composer 2 Fast · 5.4%
~/workspace · main`
	if hasCursorTrustPrompt(pane) {
		t.Fatal("stale trust prompt outside the visible pane must not be treated as current")
	}
	if hasCursorActivity(pane) {
		t.Fatal("stale activity outside the visible pane must not be treated as current")
	}
	if !hasCursorReadyPrompt(pane) {
		t.Fatal("current visible ready prompt should still be detected")
	}
}

// TestCursorReadyPromptRejectsComposer25BootBanner locks in the fix
// for the cold-start failure where the first user prompt was silently
// dropped because hasCursorReadyPrompt fired on the welcome screen.
//
// Cursor Agent v2026.05.24+ paints "→ Plan, search, build anything"
// as a placeholder on the boot banner. The "→" + placeholder
// combination tripped hasCursorReadyMarker; the wait loop returned
// nil; the submit fired before the input field was actually
// interactive; the keystrokes were lost; the turn timed out with the
// pane still showing the banner.
//
// Pane fixture is verbatim from the observed failure
// (session e0d8032c-..., 2026-05-25 13:23:37) minus padding spaces.
func TestCursorReadyPromptRejectsComposer25BootBanner(t *testing.T) {
	pane := `  Cursor Agent
  v2026.05.24-dda726e
  Use /skills to give Cursor specialized knowledge for tasks.

  → Plan, search, build anything

  Composer 2.5                                                                                                                                     Auto-run
  ~/ai-work/mcp-agent-builder-go/workspace-docs/Workflow/ICICI-BANK-PARSING · main`

	if hasCursorReadyPrompt(pane) {
		t.Fatal("Composer 2.5 cold-start banner must NOT be treated as ready — submitting on this pane drops the prompt")
	}
}

// TestCursorReadyPromptAcceptsPostBannerComposerPane verifies the
// banner guard does not regress the warm-session readiness signal.
// Once cursor has scrolled past the boot banner (no more "Cursor
// Agent\nv...\nUse /skills..." header), the same "→" / "Plan, search,
// build anything" tokens should still be treated as ready so chat
// turns submit normally.
func TestCursorReadyPromptAcceptsPostBannerComposerPane(t *testing.T) {
	pane := `Some previous assistant output that scrolled the banner away.

  → Plan, search, build anything

  Composer 2.5                                                                                                                                     Auto-run
  ~/workspace · main`

	if !hasCursorReadyPrompt(pane) {
		t.Fatal("post-banner Composer pane should be ready — only the boot banner is special-cased")
	}
}

func TestCursorReadyPromptAllowsStaleRunningLineAfterToolCompletion(t *testing.T) {
	pane := `Cursor Agent
v2026.05.16-0338208

Running the requested shell command.

$ sleep 8; echo READY_FOR_LIVE_INPUT 8.5s
  READY_FOR_LIVE_INPUT

Command finished: READY_FOR_LIVE_INPUT. LIVE_ACK_abc123

→ Add a follow-up

Composer 2 Fast · 9.8%
~/workspace · main`

	if !hasCursorActivity(pane) {
		t.Fatal("fixture should include a stale activity line")
	}
	if !hasCursorReadyPrompt(pane) {
		t.Fatal("completed tool output with follow-up prompt should be treated as ready")
	}
}

func TestBuildCursorInteractiveLaunchUsesTmuxTUIArgs(t *testing.T) {
	adapter := NewCursorCLIAdapter("secret", "gpt-5", &MockLogger{})
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(workDir)(opts)
	WithForce()(opts)
	WithApproveMCPs()(opts)
	WithSandbox("enabled")(opts)

	args, env, gotWorkDir, cleanup, err := adapter.buildCursorInteractiveLaunch(opts, "Follow repo rules.", "test-session-build-launch")
	if err != nil {
		t.Fatalf("buildCursorInteractiveLaunch error = %v", err)
	}
	defer cleanup()

	if gotWorkDir != workDir {
		t.Fatalf("workingDir = %q, want %q", gotWorkDir, workDir)
	}
	if len(args) == 0 || args[0] != "cursor-agent" {
		t.Fatalf("args = %v, want cursor-agent launch", args)
	}
	if cursorArgsContain(args, "-p") || cursorArgsContainPair(args, "--output-format", "stream-json") {
		t.Fatalf("args = %v, tmux mode must not use -p/stream-json", args)
	}
	for _, want := range [][]string{
		{"--workspace", workDir},
		{"--model", "gpt-5"},
		{"--sandbox", "enabled"},
	} {
		if !cursorArgsContainPair(args, want[0], want[1]) {
			t.Fatalf("args missing %s %s: %v", want[0], want[1], args)
		}
	}
	if !cursorArgsContain(args, "--force") || !cursorArgsContain(args, "--approve-mcps") {
		t.Fatalf("args missing force/approve-mcps: %v", args)
	}
	if len(env) != 1 || env[0] != "CURSOR_API_KEY=secret" {
		t.Fatalf("env = %v, want CURSOR_API_KEY", env)
	}
	if matches, _ := filepath.Glob(filepath.Join(workDir, ".cursor", "rules", "mlp-system*.mdc")); len(matches) != 1 {
		t.Fatalf("system rule files = %v, want one temporary Cursor rule", matches)
	}
}

func TestBuildCursorInteractiveLaunchPinsDefaultAliasToComposer25(t *testing.T) {
	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(workDir)(opts)

	args, _, _, cleanup, err := adapter.buildCursorInteractiveLaunch(opts, "Follow repo rules.", "test-session-build-default-launch")
	if err != nil {
		t.Fatalf("buildCursorInteractiveLaunch error = %v", err)
	}
	defer cleanup()

	if !cursorArgsContainPair(args, "--workspace", workDir) {
		t.Fatalf("args missing workspace: %v", args)
	}
	if !cursorArgsContainPair(args, "--model", "composer-2.5") {
		t.Fatalf("args = %v, default cursor-cli alias should pin --model composer-2.5", args)
	}
}

// TestPrepareCursorProjectFilesNukesCursorTreeOnCleanup locks in the
// new aggressive-cleanup contract: mid-session the orchestrator's
// override config is visible, and on cleanup the entire .cursor/ tree
// is removed — including the operator's original cli.json. The trade-off
// (operator content destroyed) is intentional: .cursor/ is treated as a
// session-scoped artifact area so orphans from a prior crashed session
// don't leak across runs.
func TestPrepareCursorProjectFilesNukesCursorTreeOnCleanup(t *testing.T) {
	workDir := t.TempDir()
	cursorDir := filepath.Join(workDir, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cliPath := filepath.Join(cursorDir, "cli.json")
	original := `{"permissions":{"allow":["Shell(ls)"],"deny":[]}}`
	if err := os.WriteFile(cliPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := &llmtypes.CallOptions{}
	WithProjectConfig(`{"permissions":{"allow":[],"deny":["Shell(rm)"]}}`)(opts)
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"node","args":["server.js"]}}}`)(opts)

	cleanup, err := prepareCursorProjectFiles(workDir, "System text", opts, "test-session-restore")
	if err != nil {
		t.Fatalf("prepareCursorProjectFiles error = %v", err)
	}
	if got, err := os.ReadFile(cliPath); err != nil || !strings.Contains(string(got), "Shell(rm)") {
		t.Fatalf("mid-session cli.json = %q err=%v, want orchestrator override", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(cursorDir, "mcp.json")); err != nil {
		t.Fatalf("mcp.json not written: %v", err)
	}

	cleanup()

	if _, err := os.Stat(cursorDir); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove the full .cursor/ tree (including the operator's pre-existing cli.json); stat err = %v", err)
	}
}

func TestPrepareCursorProjectFilesWritesMCPAllowlistCLIConfig(t *testing.T) {
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"node","args":["server.js"]}}}`)(opts)

	cleanup, err := prepareCursorProjectFiles(workDir, "System text", opts, "test-session-mcp-allow")
	if err != nil {
		t.Fatalf("prepareCursorProjectFiles error = %v", err)
	}
	defer cleanup()

	cursorDir := filepath.Join(workDir, ".cursor")
	mcpBody, err := os.ReadFile(filepath.Join(cursorDir, "mcp.json"))
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	if !strings.Contains(string(mcpBody), `"type":"stdio"`) {
		t.Fatalf("mcp.json = %s, want stdio type normalization", string(mcpBody))
	}
	cliBody, err := os.ReadFile(filepath.Join(cursorDir, "cli.json"))
	if err != nil {
		t.Fatalf("read cli.json: %v", err)
	}
	if !strings.Contains(string(cliBody), `Mcp(api-bridge:*)`) {
		t.Fatalf("cli.json = %s, want MCP allowlist", string(cliBody))
	}
}

func TestBuildCursorPromptResumeSendsOnlyLatestHuman(t *testing.T) {
	messages := []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "first"}}},
		{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "old answer"}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "latest"}}},
	}
	if got := buildCursorPrompt(messages, true); got != "latest" {
		t.Fatalf("resume prompt = %q, want latest", got)
	}
}

func TestCursorCLIRejectsImageContent(t *testing.T) {
	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
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
		t.Fatal("GenerateContent() error = nil, want unsupported image error")
	}
	if !strings.Contains(err.Error(), "does not support llmtypes.ImageContent") {
		t.Fatalf("GenerateContent() error = %v, want image unsupported error", err)
	}
}

func TestParseCursorInteractiveResponseDropsTUIAndEcho(t *testing.T) {
	prompt := `Preserve input safely:
JSON: {"token":"CURSOR_TMUX_OK"}
Reply exactly: saved CURSOR_TMUX_OK`
	baseline := "Cursor Agent\nType your message\n"
	captured := baseline + `
> Preserve input safely:
JSON: {"token":"CURSOR_TMUX_OK"}
Reply exactly: saved CURSOR_TMUX_OK
Thinking...
Running Shell(ls)
{"stdout":"file\n","exit_code":0}
saved CURSOR_TMUX_OK
Type your message
`

	got := parseCursorInteractiveResponse(captured, baseline, prompt, nil)
	want := "saved CURSOR_TMUX_OK"
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
}

func TestParseCursorInteractiveResponseDropsPromptOnlyTUI(t *testing.T) {
	prompt := "This is a real Cursor CLI tmux contract test.\nReply exactly: saved REAL_CURSOR_TMUX_ok"
	captured := `v2026.05.16-0338208
Try Composer via /models, frontier intelligence at a fraction of the cost.
→ This is a real Cursor CLI tmux contract test.
Ask (shift+tab to cycle)
Composer 2 Fast
~/ai-work/multi-llm-provider-go/pkg/adapters/cursorcli · main`

	got := parseCursorInteractiveResponse(captured, "", prompt, nil)
	if got != "" {
		t.Fatalf("parsed prompt-only TUI = %q, want empty", got)
	}
	if !cursorPaneShowsPromptDraft(captured, prompt) {
		t.Fatalf("expected prompt draft detection")
	}
}

func TestParseCursorInteractiveResponseFiltersGreetingAndSlashHints(t *testing.T) {
	tests := []struct {
		name     string
		captured string
		baseline string
		prompt   string
		want     string
	}{
		{
			name: "real pane with greeting and response",
			baseline: `  Cursor Agent
  v2026.05.16-0338208
  Try Composer via /models, frontier intelligence at a fraction of the cost.




  → Plan, search, build anything


  Composer 2 Fast
  ~/ai-work/multi-llm-provider-go · main`,
			captured: `  Cursor Agent
  v2026.05.16-0338208
  Try Composer via /models, frontier intelligence at a fraction of the cost.


  hi whats up



  Hey — not much on my side, just here and ready to help.

  What are you working on?




  → Add a follow-up


  Composer 2 Fast · 5.5%
  ~/ai-work/multi-llm-provider-go · main`,
			prompt: "hi whats up",
			// Blank line between sentences in the pane is now preserved as
			// "\n\n" so the CommonMark renderer treats the second sentence as
			// its own paragraph instead of running both together.
			want: "Hey — not much on my side, just here and ready to help.\n\nWhat are you working on?",
		},
		{
			name: "filters Use /plan and Use /skills greeting lines",
			captured: `  Cursor Agent
  v2026.05.16-0338208
  Use /plan to iterate on an implementation plan before code changes.


  hi whats up


  Use /skills to give Cursor specialized knowledge for tasks.
  Not much—here and ready to help. What would you like to work on?


  → Add a follow-up


  Composer 2 Fast · 14.5%
  ~/ai-work/mcp-agent-builder-go · main`,
			prompt: "hi whats up",
			want:   "Not much—here and ready to help. What would you like to work on?",
		},
		{
			name: "stops extraction at Add a follow-up boundary",
			captured: `This is the real answer.
Some extra detail.
Add a follow-up
Some junk after the prompt boundary`,
			prompt: "question",
			want:   "This is the real answer.\nSome extra detail.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCursorInteractiveResponse(tt.captured, tt.baseline, tt.prompt, nil)
			if got != tt.want {
				t.Fatalf("parsed response =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestParseCursorMultiTurnExtractsOnlyLatestResponse(t *testing.T) {
	// Baseline: pane state after turn 2 completed (before turn 3 prompt)
	baseline := `  Cursor Agent
  v2026.05.16-0338208
  Use /auto-run to skip all approvals.


  what is 2+2



  2 + 2 = 4


  now multiply that by 3




  From before, \(2 + 2 = 4\). Multiplying by 3:

  \(4 \times 3 = 12\)




  → Add a follow-up


  Composer 2 Fast · 5.5%
  ~/ai-work/multi-llm-provider-go · main`

	// Captured: pane after turn 3 response
	captured := `  Cursor Agent
  v2026.05.16-0338208
  Use /auto-run to skip all approvals.


  what is 2+2



  2 + 2 = 4


  now multiply that by 3




  From before, \(2 + 2 = 4\). Multiplying by 3:

  \(4 \times 3 = 12\)


  now add 100 to it and explain step by step




  Here's the chain, step by step.
  Step 1 — Start from the last number
  We left off with 12 (that was \(4 \times 3\)).
  Step 2 — Do the addition
  12 + 100 = 112
  Final answer: 112




  → Add a follow-up


  Composer 2 Fast · 5.6%
  ~/ai-work/multi-llm-provider-go · main`

	prompt := "now add 100 to it and explain step by step"
	historicalResponses := []string{
		"2 + 2 = 4",
		`From before, \(2 + 2 = 4\). Multiplying by 3:
\(4 \times 3 = 12\)`,
	}

	got := parseCursorInteractiveResponse(captured, baseline, prompt, historicalResponses)
	if !strings.Contains(got, "Here's the chain, step by step.") {
		t.Fatalf("turn 3 response missing expected content, got:\n%s", got)
	}
	if strings.Contains(got, "2 + 2 = 4") {
		t.Fatalf("turn 3 response contains turn 1 historical text:\n%s", got)
	}
	if strings.Contains(got, "Multiplying by 3") {
		t.Fatalf("turn 3 response contains turn 2 historical text:\n%s", got)
	}
	if strings.Contains(got, "now add 100") {
		t.Fatalf("turn 3 response contains echoed user prompt:\n%s", got)
	}
}

func TestStripCursorHistoricalAssistantTextRemovesPaneReplay(t *testing.T) {
	previous := "The first turn answer.\nIt has two lines."
	text := previous + "\nThe second turn answer."

	got := stripCursorHistoricalAssistantText(text, []string{previous})
	want := "The second turn answer."
	if got != want {
		t.Fatalf("stripped = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// hasCursorActivity — generation-in-progress detection
// ---------------------------------------------------------------------------

func TestHasCursorActivityDuringGeneration(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want bool
	}{
		{
			name: "ctrl+c to stop during streaming",
			pane: `  writing a story about cats

  Once upon a time there was a cat.

 ⠘⠤ Composing  123 tokens

  → Add a follow-up                                                ctrl+c to stop

  Composer 2 Fast · 5.4%`,
			want: true,
		},
		{
			name: "composing spinner active",
			pane: `  Step 1: read the file
 ⠘⠤ Composing  456 tokens
  → Add a follow-up
  Composer 2 Fast`,
			want: true,
		},
		{
			name: "thinking prefix",
			pane: `  Thinking about the problem...
  → Add a follow-up
  Composer 2 Fast`,
			want: true,
		},
		{
			name: "running tool",
			pane: `  Running Shell(ls -la)
  → Add a follow-up
  Composer 2 Fast`,
			want: true,
		},
		{
			name: "editing file",
			pane: `  Editing src/main.go
  → Add a follow-up
  Composer 2 Fast`,
			want: true,
		},
		{
			name: "esc to interrupt",
			pane: `  Looking at the codebase...
  esc to interrupt
  Composer 2 Fast`,
			want: true,
		},
		{
			name: "calling tool",
			pane: `  calling execute_shell_command
  → Add a follow-up
  Composer 2 Fast`,
			want: true,
		},
		{
			name: "completed response no activity",
			pane: `  The answer is 42.

  → Add a follow-up

  Composer 2 Fast · 5.6%`,
			want: false,
		},
		{
			name: "initial prompt no activity",
			pane: `  Cursor Agent
  v2026.05.16-0338208
  → Plan, search, build anything
  Composer 2 Fast`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCursorActivity(tt.pane)
			if got != tt.want {
				t.Fatalf("hasCursorActivity = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// hasCursorReadyPrompt — completion detection (ready = no activity + prompt visible)
// ---------------------------------------------------------------------------

func TestHasCursorReadyPromptStates(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want bool
	}{
		{
			name: "completed response with arrow prompt",
			pane: `  Cursor Agent
  v2026.05.16-0338208


  hello



  Hi! How can I help?



  → Add a follow-up


  Composer 2 Fast · 5.4%
  ~/workspace · main`,
			want: true,
		},
		{
			name: "initial ready state with arrow",
			pane: `  Cursor Agent
  v2026.05.16-0338208
  Use /plan to iterate.


  → Plan, search, build anything


  Composer 2 Fast
  ~/workspace · main`,
			want: true,
		},
		{
			name: "mid-generation with composing spinner",
			pane: `  Cursor Agent

  hello


  Working on it...
 ⠘⠤ Composing  200 tokens

  → Add a follow-up                                                ctrl+c to stop

  Composer 2 Fast · 5.4%`,
			want: false,
		},
		{
			name: "mid-generation with thinking and ctrl+c to stop",
			pane: `  Cursor Agent

  hello

  Thinking about this...

  → Add a follow-up                                                ctrl+c to stop

  Composer 2 Fast`,
			want: false,
		},
		{
			name: "stale thinking line after completion is ready",
			pane: `  Cursor Agent

  hello

  Thinking about this...

  The answer is yes.

  → Add a follow-up

  Composer 2 Fast · 5.4%`,
			want: true,
		},
		{
			name: "trust prompt is not ready",
			pane: `  ⚠ Workspace Trust Required
  Do you trust the contents of this directory?
  [a] Trust this workspace
  [q] Quit
  → Plan, search, build anything`,
			want: false,
		},
		{
			name: "stale running line with completed follow-up prompt",
			pane: `  Cursor Agent

  Running Shell(ls)
  file1.go file2.go

  Done listing files.

  → Add a follow-up

  Composer 2 Fast · 5.5%`,
			want: true,
		},
		{
			name: "no arrow prompt visible",
			pane: `  Cursor Agent
  v2026.05.16-0338208

  Processing request...

  Composer 2 Fast`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCursorReadyPrompt(tt.pane)
			if got != tt.want {
				t.Fatalf("hasCursorReadyPrompt = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// hasCursorReadyMarker — structural → arrow detection
// ---------------------------------------------------------------------------

func TestHasCursorReadyMarkerUsesArrow(t *testing.T) {
	tests := []struct {
		name    string
		cleaned string
		want    bool
	}{
		{name: "arrow with follow-up", cleaned: "→ add a follow-up", want: true},
		{name: "arrow with plan prompt", cleaned: "→ plan, search, build anything", want: true},
		{name: "arrow with unknown future text", cleaned: "→ some new cursor prompt text v2027", want: true},
		{name: "arrow alone", cleaned: "→", want: true},
		{name: "legacy type your message", cleaned: "type your message here", want: true},
		{name: "legacy ask shift tab", cleaned: "ask (shift+tab to cycle)", want: true},
		{name: "no marker at all", cleaned: "hello world\nsome response text", want: false},
		{name: "composing is not a marker", cleaned: "composing 200 tokens", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCursorReadyMarker(tt.cleaned)
			if got != tt.want {
				t.Fatalf("hasCursorReadyMarker = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isCursorTUILine — TUI chrome filtering
// ---------------------------------------------------------------------------

func TestIsCursorTUILineFiltering(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"Use /plan to iterate on an implementation plan before code changes.", true},
		{"Use /skills to give Cursor specialized knowledge for tasks.", true},
		{"Use /context to add files or docs.", true},
		{"Use /auto-run to skip all approvals.", true},
		{"Add a follow-up", true},
		{"v2026.05.16-0338208", true},
		{"Try Composer via /models, frontier intelligence at a fraction of the cost.", true},
		{"Composer 2 Fast · 5.4%", true},
		{"~/ai-work/multi-llm-provider-go · main", true},
		{"ctrl+c to stop", true},
		{"Auto-run", true},
		{"→ Add a follow-up", true}, // also a TUI line via "→ " prefix; prompt boundary breaks first in extraction
		{"The answer is 42.", false},
		{"Hello! How can I help you today?", false},
		{"Step 1 — Read the file", false},
		{"12 + 100 = 112", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isCursorTUILine(tt.line)
			if got != tt.want {
				t.Fatalf("isCursorTUILine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isCursorPromptBoundaryLine — extraction break point
// ---------------------------------------------------------------------------

func TestIsCursorPromptBoundaryLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"→ Add a follow-up", true},
		{"→ Plan, search, build anything", true},
		{"→ Some future Cursor prompt text", true},
		{"→", true},
		{">", true},
		{"›", true},
		{"❯", true},
		{"Add a follow-up", true},
		{"Type your message", true},
		{"The answer is 42.", false},
		{"Hello → world", false}, // → not at start
		{"Running Shell(ls)", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isCursorPromptBoundaryLine(tt.line)
			if got != tt.want {
				t.Fatalf("isCursorPromptBoundaryLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// cursorCapturedAfterBaseline — line-based prefix divergence
// ---------------------------------------------------------------------------

func TestCursorCapturedAfterBaselineLineDivergence(t *testing.T) {
	tests := []struct {
		name     string
		captured string
		baseline string
		wantHas  string
		wantNot  string
	}{
		{
			name:     "exact substring match",
			baseline: "line1\nline2\nline3",
			captured: "line1\nline2\nline3\nnew content here",
			wantHas:  "new content here",
			wantNot:  "line1",
		},
		{
			name: "line prefix divergence when baseline tail changes",
			baseline: `  Cursor Agent
  v2026.05.16-0338208
  Use /auto-run to skip all approvals.
  Previous content
  → Add a follow-up
  Composer 2 Fast · 5.5%`,
			captured: `  Cursor Agent
  v2026.05.16-0338208
  Use /auto-run to skip all approvals.
  Previous content
  New user prompt
  New response text
  → Add a follow-up
  Composer 2 Fast · 5.6%`,
			wantHas: "New response text",
			wantNot: "",
		},
		{
			name:     "empty baseline returns full captured",
			baseline: "",
			captured: "full content here",
			wantHas:  "full content here",
		},
		{
			name:     "completely different content returns full captured",
			baseline: "something completely different",
			captured: "nothing in common at all with the other text",
			wantHas:  "nothing in common",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cursorCapturedAfterBaseline(tt.captured, tt.baseline)
			if tt.wantHas != "" && !strings.Contains(got, tt.wantHas) {
				t.Fatalf("result missing %q, got:\n%s", tt.wantHas, got)
			}
			if tt.wantNot != "" && strings.Contains(got, tt.wantNot) {
				t.Fatalf("result should not contain %q, got:\n%s", tt.wantNot, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseCursorInteractiveResponse — end-to-end extraction with tool use
// ---------------------------------------------------------------------------

func TestParseCursorResponseWithToolOutput(t *testing.T) {
	baseline := `  Cursor Agent
  v2026.05.16-0338208


  → Plan, search, build anything


  Composer 2 Fast
  ~/workspace · main`

	captured := `  Cursor Agent
  v2026.05.16-0338208


  list the go files


  Running Shell(ls *.go)
  main.go  server.go  utils.go

  Here are the Go files in the project:
  - main.go
  - server.go
  - utils.go


  → Add a follow-up


  Composer 2 Fast · 5.5%
  ~/workspace · main`

	got := parseCursorInteractiveResponse(captured, baseline, "list the go files", nil)
	if !strings.Contains(got, "main.go") {
		t.Fatalf("response should contain file listing, got:\n%s", got)
	}
	if strings.Contains(got, "list the go files") {
		t.Fatalf("response should not contain echoed prompt, got:\n%s", got)
	}
}

func TestParseCursorResponseWithCodeBlock(t *testing.T) {
	captured := `  explain the function


  The \x60greet\x60 function takes a name and returns a greeting:

  \x60\x60\x60go
  func greet(name string) string {
      return "Hello, " + name
  }
  \x60\x60\x60

  It concatenates "Hello, " with the provided name.


  → Add a follow-up


  Composer 2 Fast · 5.5%`

	got := parseCursorInteractiveResponse(captured, "", "explain the function", nil)
	if !strings.Contains(got, "greet") {
		t.Fatalf("response should contain function name, got:\n%s", got)
	}
	if !strings.Contains(got, "concatenates") {
		t.Fatalf("response should contain explanation, got:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// isCursorToolStatusLine — tool execution status filtering
// ---------------------------------------------------------------------------

func TestIsCursorToolStatusLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"Thinking about the problem...", true},
		{"Working on your request...", true},
		{"Running Shell(ls -la)", true},
		{"Reading src/main.go", true},
		{"Editing utils.go", true},
		{"Writing tests/new_test.go", true},
		{"Searching for references...", true},
		{"Applying changes...", true},
		{"Calling execute_shell_command", true},
		{"Called read_file successfully", true},
		{"Executing the plan...", true},
		{`"stdout":"hello\n"`, true},
		{`"stderr":"error"`, true},
		{`"exit_code":0`, true},
		{"mcp bridge tool call", true},
		{"The answer is 42.", false},
		{"Here are the results:", false},
		{"Step 1: Read the file", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isCursorToolStatusLine(tt.line)
			if got != tt.want {
				t.Fatalf("isCursorToolStatusLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestParseCursorResponseDropsToolTranscriptAndUserHeader locks in the fix for
// the live-pane bug where extractCursorVisibleAssistantText leaked the prior
// "User:" turn header and the tool transcript (Globbed/Found N files/$ cmd
// duration) into the final assistant text.
func TestParseCursorResponseDropsToolTranscriptAndUserHeader(t *testing.T) {
	baseline := `  Cursor Agent
  v2026.05.20-2b5dd59
  Use /mcp to connect Cursor to your tools and data sources.


  → Add a follow-up


  Ask (shift+tab to cycle)
  Composer 2.5 · 15.0%
  ~/ai-work/test · main`

	captured := `  Cursor Agent
  v2026.05.20-2b5dd59
  Use /mcp to connect Cursor to your tools and data sources.


  User: which workflows are there


  Listing workflows in the workspace.

    Globbed "*" in /tmp/Workflow
    Found 33 files

  $ ls -1 /tmp/Workflow | sort 407ms
    alpha
    beta
    … truncated (31 more lines) · ctrl+o to expand

  Assistant: Here are the workflows: alpha, beta, gamma.


  → Add a follow-up


  Ask (shift+tab to cycle)
  Composer 2.5 · 15.8%
  ~/ai-work/test · main`

	got := parseCursorInteractiveResponse(captured, baseline, "which workflows are there", nil)

	want := "Here are the workflows: alpha, beta, gamma."
	if !strings.Contains(got, want) {
		t.Fatalf("response missing final assistant line %q, got:\n%s", want, got)
	}
	testcontracts.AssertCleanFinalExtraction(t, "cursor-cli", got,
		[]string{want},
		[]string{
			"User:",               // prior-turn header leaked through
			"which workflows are", // echoed user prompt
			"Listing workflows in the workspace.",
			`Globbed "*"`,    // tool transcript
			"Found 33 files", // tool result summary
			"$ ls -1",        // raw shell echo
			"\nalpha\nbeta\n",
			"407ms",     // shell duration suffix
			"truncated", // tool-output ellipsis marker
			"ctrl+o",
			"Assistant:", // stripped label prefix should not survive
		},
	)
}

func TestCursorFinalExtractionVertexJudgeE2E(t *testing.T) {
	baseline := `  Cursor Agent
  v2026.05.20-2b5dd59


  → Add a follow-up`

	captured := `  Cursor Agent
  v2026.05.20-2b5dd59


  User: which workflows are there


  Listing workflows in the workspace.

    Globbed "*" in /tmp/Workflow
    Found 33 files

  $ ls -1 /tmp/Workflow | sort 407ms
    alpha
    beta
    … truncated (31 more lines) · ctrl+o to expand

  Assistant: Here are the workflows: alpha, beta, gamma.


  → Add a follow-up`

	got := parseCursorInteractiveResponse(captured, baseline, "which workflows are there", nil)
	testcontracts.AssertVertexJudgesFinalExtraction(t, testcontracts.FinalExtractionJudgeCase{
		Provider:   "cursor-cli",
		TmuxScreen: captured,
		Extracted:  got,
		UserGoal:   "List the workflows.",
		MustContain: []string{
			"Here are the workflows: alpha, beta, gamma.",
		},
		Forbidden: []string{
			"User:",
			"which workflows are there",
			`Globbed "*"`,
			"Found 33 files",
			"$ ls -1",
			"ctrl+o",
			"Assistant:",
		},
		ExpectedNote: "The extracted answer should be only the assistant sentence without labels or shell transcript.",
	})
}

// Documents the known small false-positive in the shell-echo filter: a single
// line that literally looks like "$ <cmd> NNNms" is treated as a tool
// transcript line even when the assistant wrote it inside a markdown response.
// The duration anchor (\d+ms|\d+s at end-of-line) is narrow enough that this
// only triggers on prose that happens to mimic Cursor's shell echo exactly.
// Multi-line code blocks where the duration appears on a separate line are
// unaffected, which is the common case.
func TestCursorShellEchoFilterDoesNotEatMultilineCodeBlocks(t *testing.T) {
	captured := `  Cursor Agent
  v2026.05.20-2b5dd59

  User: show me how to run the tests

  Assistant: Here's how to run the test suite:

  $ npm test
  This prints the results once it finishes.


  → Add a follow-up


  Ask (shift+tab to cycle)
  Composer 2.5 · 12.0%`

	got := parseCursorInteractiveResponse(captured, "", "show me how to run the tests", nil)
	if !strings.Contains(got, "$ npm test") {
		t.Fatalf("multi-line code block should keep its $ shell example, got:\n%s", got)
	}
	if !strings.Contains(got, "prints the results") {
		t.Fatalf("multi-line code block should keep follow-up prose, got:\n%s", got)
	}
}

// Pins the upgraded turn-boundary behaviour: a multi-turn pane should yield
// only the most recent assistant turn, with no leakage from earlier User: /
// Assistant: blocks even when baseline-diff falls back to line-prefix mode.
func TestParseCursorResponseStripsStaleMultiTurnHistory(t *testing.T) {
	baseline := `  Cursor Agent
  v2026.05.20-2b5dd59

  → Add a follow-up

  Ask (shift+tab to cycle)
  Composer 2.5 · 10.0%`

	captured := `  Cursor Agent
  v2026.05.20-2b5dd59

  User: what is two plus two

  Assistant: Two plus two is four.

  User: and what is three plus three

  Assistant: Three plus three is six.


  → Add a follow-up


  Ask (shift+tab to cycle)
  Composer 2.5 · 11.5%`

	got := parseCursorInteractiveResponse(captured, baseline, "and what is three plus three", nil)
	if !strings.Contains(got, "Three plus three is six.") {
		t.Fatalf("response should keep the latest assistant turn, got:\n%s", got)
	}
	// Earlier-turn assistant prose should NOT survive into the new turn's
	// extracted text. (User: headers and prompt echoes are covered by the
	// existing TestParseCursorResponseDropsToolTranscriptAndUserHeader.)
	if strings.Contains(got, "Two plus two is four") {
		t.Fatalf("earlier assistant turn leaked into latest response, got:\n%s", got)
	}
}

// Cursor prints a "tool activity" narration block above the actual answer:
//
//	Checking where schedules are defined in the workspace.
//	Read, grepped, globbed 7 files, 4 greps, 2 globs
//	… 10 earlier items hidden
//	Read .../Workflow/social-media/workflow.json lines 100-179
//	Read .../Workflow/social-media/workflow.json lines 63-102
//	Here's what's configured …
//
// The summary, truncation header, and per-file read lines must be filtered;
// the "Checking …" sentence reads like prose so we don't try to strip it.
// Paragraph breaks (blank lines in the pane) must survive as "\n\n" so the
// CommonMark renderer treats them as paragraph breaks rather than wraps.
func TestParseCursorResponseFiltersToolNarrationAndKeepsParagraphBreaks(t *testing.T) {
	captured := `  Cursor Agent
  v2026.05.20-2b5dd59


  list the schedules


  Checking where schedules are defined in the workspace.
  Read, grepped, globbed 7 files, 4 greps, 2 globs
  … 10 earlier items hidden
  Read .../Workflow/social-media/workflow.json lines 100-179
  Read .../Workflow/social-media/workflow.json lines 63-102

  Here’s what’s configured in the workspace right now.

  1. Multi-agent chat schedules
  None.

  2. Workflow schedules (enabled)
  These live in each workflow’s workflow.json under schedules.


  → Add a follow-up


  Ask (shift+tab to cycle)
  Composer 2.5 · 12.0%`

	got := parseCursorInteractiveResponse(captured, "", "list the schedules", nil)

	forbidden := []string{
		"Read, grepped, globbed",
		"earlier items hidden",
		"lines 100-179",
		"lines 63-102",
	}
	for _, bad := range forbidden {
		if strings.Contains(got, bad) {
			t.Fatalf("response should not contain narration %q, got:\n%s", bad, got)
		}
	}
	keep := []string{
		"Here’s what’s configured",
		"Multi-agent chat schedules",
		"Workflow schedules (enabled)",
	}
	for _, want := range keep {
		if !strings.Contains(got, want) {
			t.Fatalf("response missing prose %q, got:\n%s", want, got)
		}
	}
	// Paragraph breaks between sections must survive as "\n\n" so the
	// CommonMark renderer renders them as paragraph breaks (not wraps).
	if !strings.Contains(got, "\n\n1. Multi-agent chat schedules") {
		t.Fatalf("paragraph break before '1. Multi-agent' lost, got:\n%s", got)
	}
	if !strings.Contains(got, "\n\n2. Workflow schedules") {
		t.Fatalf("paragraph break before '2. Workflow' lost, got:\n%s", got)
	}
}

// Cursor's tmux TUI does not write a parseable sidecar with token counts
// (unlike claude-code's *.jsonl, codex's rollout, or gemini's transcript file)
// — the only on-disk record is a SQLite blob format we don't read. The
// adapter therefore estimates tokens from prompt + response character lengths
// using the 4-chars-per-token English heuristic, so the cost ledger gets a
// non-zero row instead of a bare timestamp. This test pins the heuristic
// against a few representative cases; if cursor ever exposes exact tokens we
// can drop this in favor of the real source.
func TestEstimateCursorTmuxTokensCharBased(t *testing.T) {
	for _, tc := range []struct {
		name             string
		prompt           string
		content          string
		wantInputTokens  int
		wantOutputTokens int
	}{
		{"both empty", "", "", 0, 0},
		{"one-char prompt", "x", "", 1, 0},
		{"4-char prompt rounds to 1 token", "abcd", "", 1, 0},
		{"5-char prompt rounds up to 2 tokens", "abcde", "", 2, 0},
		{"typical short prompt + reply", "hi", "Hi — how can I help today?", 1, 7},
		{"40-char prompt + 80-char reply", strings.Repeat("a", 40), strings.Repeat("b", 80), 10, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, out := estimateCursorTmuxTokens(tc.prompt, tc.content)
			if in != tc.wantInputTokens || out != tc.wantOutputTokens {
				t.Fatalf("estimateCursorTmuxTokens(%q, %q) = (%d, %d), want (%d, %d)",
					tc.prompt, tc.content, in, out, tc.wantInputTokens, tc.wantOutputTokens)
			}
		})
	}
}

func cursorArgsContain(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func cursorArgsContainPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
