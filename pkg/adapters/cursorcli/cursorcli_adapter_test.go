package cursorcli

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

func TestCursorCLIAdapterImplementsWebSearchModel(t *testing.T) {
	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	if _, ok := interface{}(adapter).(llmtypes.WebSearchModel); !ok {
		t.Fatal("CursorCLIAdapter should implement llmtypes.WebSearchModel")
	}
}

func TestHasCursorWebSearchApprovalPrompt(t *testing.T) {
	pane := `┌──────────────────────────────┐
│ 🔍 Web Search: capital of France │
└──────────────────────────────┘

Allow this web search?
 → Allow search (y)
   Auto-run everything (shift+tab)
   Skip (esc or n)`

	if !hasCursorWebSearchApprovalPrompt(pane) {
		t.Fatal("expected Cursor web-search approval prompt to be detected")
	}
	if hasCursorReadyPrompt(pane) {
		t.Fatal("web-search approval prompt must not be treated as a ready prompt")
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

	args, env, gotWorkDir, cleanup, err := adapter.buildCursorInteractiveLaunch(opts, "Follow repo rules.")
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
	if matches, _ := filepath.Glob(filepath.Join(workDir, ".cursor", "rules", "mlp-system-*.mdc")); len(matches) != 1 {
		t.Fatalf("system rule files = %v, want one temporary Cursor rule", matches)
	}
}

func TestPrepareCursorProjectFilesRestoresExistingConfig(t *testing.T) {
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

	cleanup, err := prepareCursorProjectFiles(workDir, "System text", opts)
	if err != nil {
		t.Fatalf("prepareCursorProjectFiles error = %v", err)
	}
	if got, err := os.ReadFile(cliPath); err != nil || !strings.Contains(string(got), "Shell(rm)") {
		t.Fatalf("temporary cli.json = %q err=%v, want override", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(cursorDir, "mcp.json")); err != nil {
		t.Fatalf("mcp.json not written: %v", err)
	}

	cleanup()

	if got, err := os.ReadFile(cliPath); err != nil || string(got) != original {
		t.Fatalf("restored cli.json = %q err=%v, want original", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(cursorDir, "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("mcp.json should be removed after cleanup, err=%v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(cursorDir, "rules", "mlp-system-*.mdc")); len(matches) != 0 {
		t.Fatalf("system rule files remain after cleanup: %v", matches)
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

func TestStripCursorHistoricalAssistantTextRemovesPaneReplay(t *testing.T) {
	previous := "The first turn answer.\nIt has two lines."
	text := previous + "\nThe second turn answer."

	got := stripCursorHistoricalAssistantText(text, []string{previous})
	want := "The second turn answer."
	if got != want {
		t.Fatalf("stripped = %q, want %q", got, want)
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
