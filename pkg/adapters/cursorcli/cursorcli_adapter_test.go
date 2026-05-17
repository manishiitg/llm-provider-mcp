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
			want:   "Hey — not much on my side, just here and ready to help.\nWhat are you working on?",
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
