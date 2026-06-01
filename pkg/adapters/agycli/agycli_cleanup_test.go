package agycli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCleanupAgyCLIInteractiveSessionsDoesNotBlockOnBusySession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	session := &agyInteractiveSession{
		ownerSessionID:  "busy-owner",
		tmuxSessionName: "mlp-agy-cli-cleanup-busy-test",
	}
	session.mu.Lock()
	defer session.mu.Unlock()

	agyPersistentRegistry.Lock()
	oldPersistent := agyPersistentRegistry.sessions
	agyPersistentRegistry.sessions = map[string]*agyInteractiveSession{
		session.ownerSessionID: session,
	}
	agyPersistentRegistry.Unlock()
	t.Cleanup(func() {
		agyPersistentRegistry.Lock()
		agyPersistentRegistry.sessions = oldPersistent
		agyPersistentRegistry.Unlock()
	})

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done <- CleanupAgyCLIInteractiveSessions(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cleanup error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cleanup blocked on busy session mutex")
	}
}

func TestAgyGeneratingPaneWithPromptMarkerIsStillActive(t *testing.T) {
	pane := `
> Call the api-bridge slow_contract MCP tool

▸ Thought for 4s
  Executing Slow Contract

○ api-bridge/slow_contract(MCP tool execution)
⣯ Generating...
────────────────────────────────────────
>
────────────────────────────────────────
esc to cancel
`
	if !hasAgyActivity(pane) {
		t.Fatal("generating pane should be classified active")
	}
	if hasAgyReadyPrompt(pane) {
		t.Fatal("generating pane with prompt marker should not be classified ready")
	}
}

func TestAgyCompletedImageGenerationPaneIsReady(t *testing.T) {
	token := "AGY_IMAGE_GENERATED_abc123"
	pane := `
● GenerateImage(simple_blue_square)
  Generating image...
● Bash(cp /tmp/simple_blue_square.png /tmp/generated.png)
● Bash(ls -la /tmp/generated.png)

` + token + `

────────────────────────────────────────
>
────────────────────────────────────────
`
	if !hasAgyReadyPrompt(pane) {
		t.Fatal("completed image-generation pane should be classified ready")
	}
	content := parseAgyInteractiveResponse(pane, "", "", nil)
	if !strings.Contains(content, token) {
		t.Fatalf("content = %q, want token %s", content, token)
	}
	if strings.Contains(content, "GenerateImage(") || strings.Contains(content, "Bash(") {
		t.Fatalf("content = %q, want native tool transcript filtered", content)
	}
}

func TestAgyDetectsQueuedLiveInputDraftAtReadyPrompt(t *testing.T) {
	pane := `
The original task is complete.

────────────────────────────────────────
> PREVALIDATION_FAILED_AGY_LIVE_123
────────────────────────────────────────
`
	draft, ok := agyUnsubmittedPromptDraft(pane)
	if !ok {
		t.Fatal("ready prompt with live input draft should be treated as unsubmitted")
	}
	if draft != "PREVALIDATION_FAILED_AGY_LIVE_123" {
		t.Fatalf("draft = %q", draft)
	}
}

func TestAgyIgnoresEmptyReadyPromptDraft(t *testing.T) {
	pane := `
The original task is complete.

────────────────────────────────────────
>
────────────────────────────────────────
`
	if draft, ok := agyUnsubmittedPromptDraft(pane); ok {
		t.Fatalf("empty ready prompt should not be treated as unsubmitted draft: %q", draft)
	}
}

func TestAgyFinalExtractionKeepsLatestAssistantAfterPromptLineFollowup(t *testing.T) {
	pane := `
> First task

First answer should not be final.

────────────────────────────────────────
> Follow-up task

▸ Thought for 1s
  Preparing concise answer

Second answer is final.

────────────────────────────────────────
>
────────────────────────────────────────
`
	content := parseAgyInteractiveResponse(pane, "", "", nil)
	if content != "Second answer is final." {
		t.Fatalf("content = %q", content)
	}
}

func TestAgyFinalExtractionDropsPastedTextMarker(t *testing.T) {
	pane := `
> [Pasted text #1]

Fast pasted input was accepted.

────────────────────────────────────────
>
────────────────────────────────────────
`
	content := parseAgyInteractiveResponse(pane, "", "", nil)
	if content != "Fast pasted input was accepted." {
		t.Fatalf("content = %q", content)
	}
}

func TestAgyFinalExtractionPreservesMarkdownTablesAndLists(t *testing.T) {
	pane := `
│ > Please generate a table and bullet points.                         │
│                                                                      │
│ Here is the table:                                                   │
│ │ Header 1 │ Header 2 │                                              │
│ │ ──────── │ ──────── │                                              │
│ │ Value 1  │ Value 2  │                                              │
│                                                                      │
│ And the list:                                                        │
│ • First bullet point                                                 │
│ • Second bullet point                                                │
│                                                                      │
│ ────────────────────────────────────────                             │
│ >                                                                    │
│ ────────────────────────────────────────                             │
`
	content := parseAgyInteractiveResponse(pane, "", "", nil)
	expected := "Here is the table:\n│ Header 1 │ Header 2 │\n│ ──────── │ ──────── │\n│ Value 1  │ Value 2  │\n\nAnd the list:\n• First bullet point\n• Second bullet point"
	if content != expected {
		t.Fatalf("content = %q, want %q", content, expected)
	}
}

func TestAgyFinalExtractionDropsMCPToolAndThoughtNoise(t *testing.T) {
	pane := agyNoisyFinalExtractionPane()
	content := parseAgyInteractiveResponse(pane, "", "", nil)
	assertAgyNoisyFinalExtractionClean(t, content)
}

func TestAgyFinalExtractionKeepsAnswerMatchingPromptSuffix(t *testing.T) {
	prompt := "Return a final answer containing these three plain lines and no setup commentary:\nAgy final LIVE_AGY_FINAL_suffix\nfirst LIVE_AGY_FINAL_suffix\nsecond LIVE_AGY_FINAL_suffix"
	pane := `
> Return a final answer containing these three plain lines and no setup commentary:
  Agy final LIVE_AGY_FINAL_suffix
  first LIVE_AGY_FINAL_suffix
  second LIVE_AGY_FINAL_suffix

▸ Thought for 3s, 418 tokens
  Focusing on Final Output
  Agy final LIVE_AGY_FINAL_suffix
  first LIVE_AGY_FINAL_suffix
  second LIVE_AGY_FINAL_suffix

────────────────────────────────────────
>
────────────────────────────────────────
`
	content := parseAgyInteractiveResponse(pane, "", prompt, nil)
	testcontracts.AssertCleanFinalExtraction(t, "agy-cli", content,
		[]string{
			"Agy final LIVE_AGY_FINAL_suffix",
			"first LIVE_AGY_FINAL_suffix",
			"second LIVE_AGY_FINAL_suffix",
		},
		[]string{
			"Return a final answer",
			"Focusing on Final Output",
			"Thought",
			">",
		},
	)
}

func TestAgyFinalExtractionVertexJudgeE2E(t *testing.T) {
	testcontracts.RequireVertexFinalExtractionJudgeE2E(t)

	pane := agyNoisyFinalExtractionPane()
	content := parseAgyInteractiveResponse(pane, "", "", nil)
	assertAgyNoisyFinalExtractionClean(t, content)
	testcontracts.AssertVertexJudgesFinalExtraction(t, testcontracts.FinalExtractionJudgeCase{
		Provider:   "agy-cli",
		TmuxScreen: pane,
		Extracted:  content,
		UserGoal:   "Explain whether this workflow has a system prompt or instructions.",
		MustContain: []string{
			"Yes, the system prompt",
			"soul/soul.md",
			"step-specific runtime prompts",
		},
		Forbidden: []string{
			"api-bridge/",
			"Thought",
			"Would you like me",
			"+ 28 tools",
			"Authorization: Bearer",
			"execute_shell_command",
		},
		ExpectedNote: "The answer should be the final prose paragraph only, preserving its two-paragraph formatting.",
	})
}

func agyNoisyFinalExtractionPane() string {
	return `
> do you have a system prompt or instructions for workflow

+ 28 tools
• execute_shell_command

-H "Authorization: Bearer $MCP_API_TOKEN"
-H "Content-Type: application/json"
-d '{ "group_name": "test-group" }'

Would you like me to trigger one of these steps or run the entire workflow for you?

● api-bridge/get_api_spec(Get get_step_prompts API spec)
● api-bridge/get_api_spec(Get get_workflow_config API spec)
● api-bridge/get_step_prompts(Get step prompts)

▸ Thought for 2s, 184 tokens
  Investigating Execution Logs

● api-bridge/execute_shell_command(List iteration-36 prepare-test-fixtures logs)
● api-bridge/execute_shell_command(Read system prompt for step)
● api-bridge/execute_shell_command(Get system prompt preview)

▸ Thought for 3s, 207 tokens
  Discovering Core Instructions

● api-bridge/execute_shell_command(Read soul.md)

▸ Thought Process
  Understanding Workflow Mechanisms

Yes, the system prompt and instructions for this workflow are structured across multiple layers.

The high-level objective is in soul/soul.md, and step-specific runtime prompts are synthesized into each run's execution logs.

────────────────────────────────────────
>
────────────────────────────────────────
`
}

func assertAgyNoisyFinalExtractionClean(t *testing.T, content string) {
	t.Helper()
	testcontracts.AssertCleanFinalExtraction(t, "agy-cli", content,
		[]string{"Yes, the system prompt"},
		[]string{
			"api-bridge/",
			"Thought",
			"Would you like me",
			"+ 28 tools",
			"Authorization: Bearer",
			"execute_shell_command",
			"Discovering Core Instructions",
		},
	)
}

func TestAgyPromptDraftToClearBeforePaste(t *testing.T) {
	pane := `
Assistant: Done

────────────────────────────────────────
> go with option B
────────────────────────────────────────
`
	draft, ok := agyPromptDraftToClearBeforePaste(pane)
	if !ok {
		t.Fatal("agyPromptDraftToClearBeforePaste ok = false, want true for stale idle draft")
	}
	if draft != "go with option B" {
		t.Fatalf("draft = %q, want stale draft text", draft)
	}
}

func TestAgyPromptDraftToClearBeforePasteIgnoresBlankPrompt(t *testing.T) {
	pane := `
Assistant: Done

────────────────────────────────────────
>
────────────────────────────────────────
`
	if draft, ok := agyPromptDraftToClearBeforePaste(pane); ok {
		t.Fatalf("agyPromptDraftToClearBeforePaste = (%q, true), want no clear for blank prompt", draft)
	}
}

func TestAgyPromptDraftToClearBeforePasteIgnoresPlaceholder(t *testing.T) {
	pane := `
Assistant: Done

────────────────────────────────────────
> Add a follow-up
────────────────────────────────────────
`
	if draft, ok := agyPromptDraftToClearBeforePaste(pane); ok {
		t.Fatalf("agyPromptDraftToClearBeforePaste = (%q, true), want no clear for placeholder", draft)
	}
}

func TestAgyPromptDraftToClearBeforePasteIgnoresActivePane(t *testing.T) {
	pane := `
○ api-bridge/slow_contract(MCP tool execution)
⣯ Generating...
────────────────────────────────────────
> go with option B
────────────────────────────────────────
esc to cancel
`
	if draft, ok := agyPromptDraftToClearBeforePaste(pane); ok {
		t.Fatalf("agyPromptDraftToClearBeforePaste = (%q, true), want no clear while active", draft)
	}
}

func TestAgyTrustPromptDetectionAndResponse(t *testing.T) {
	projectPrompt := `
Workspace trust required
Do you trust the contents of this project?
Yes, I trust this folder
`
	if !hasAgyTrustPrompt(projectPrompt) {
		t.Fatal("expected project trust prompt detection")
	}
	if got := agyTrustPromptResponse(projectPrompt); got != "Enter" {
		t.Fatalf("project trust response = %q, want Enter", got)
	}

	mcpPrompt := `
Do you trust the contents of this directory?
[a] Trust this workspace, but don't enable all MCP servers
[w] Trust workspace and enable MCP servers
`
	if !hasAgyTrustPrompt(mcpPrompt) {
		t.Fatal("expected MCP trust prompt detection")
	}
	if got := agyTrustPromptResponse(mcpPrompt); got != "a" {
		t.Fatalf("MCP trust response = %q, want a", got)
	}

	if hasAgyTrustPrompt("Trusting workspace /tmp/example") {
		t.Fatal("completed trust status should not be treated as a prompt")
	}
}

func TestAgyAuthPromptDetection(t *testing.T) {
	pane := `
     ▄▀▀▄
    ▀▀▀▀▀▀
   ▀▀▀▀▀▀▀▀
  ▄▀▀    ▀▀▄
 ▄▀▀      ▀▀▄

 Welcome to the Antigravity CLI. You are currently not signed in.

 Select login method:
 > 1. Google OAuth
   2. Use a Google Cloud project

 [Use arrow keys to navigate, Enter to select]
`
	if !hasAgyAuthPrompt(pane) {
		t.Fatal("expected Antigravity auth prompt detection")
	}
	if hasAgyReadyPrompt(pane) {
		t.Fatal("auth prompt must not be treated as a ready prompt")
	}
}

func TestAgyFeedbackPromptDetection(t *testing.T) {
	pane := `
 How's the CLI experience so far? Help us improve:
 [1] Good  [2] Fine  [3] Bad  [0] Skip

? for shortcuts                                                                                                                                 Gemini 3.5 Flash
`
	if !hasAgyFeedbackPrompt(pane) {
		t.Fatal("expected Antigravity feedback prompt detection")
	}
	if hasAgyReadyPrompt(pane) {
		t.Fatal("feedback prompt must not be treated as a ready prompt")
	}
}

func TestAgyReadyPromptAfterFeedbackSkipWithEscFooter(t *testing.T) {
	pane := `
  Hello! I am Antigravity, your AI coding assistant.

  How can I help you with this project today?
⣽ Working...
────────────────────────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────────────────────────
esc to cancel                                                                                                            Gemini 3.5 Flash
`
	if !hasAgyReadyPrompt(pane) {
		t.Fatal("ready prompt with stale esc footer should be classified ready")
	}
}

func TestAgyWorkspaceMCPConfigLeaseRejectsConcurrentConflicts(t *testing.T) {
	workDir := t.TempDir()
	first := &agyInteractiveSession{ownerSessionID: "first"}
	firstOpts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"alpha"}}}`)(firstOpts)
	releaseFirst, err := acquireAgyWorkspaceMCPConfigLease(workDir, firstOpts, first)
	if err != nil {
		t.Fatalf("first lease error = %v", err)
	}
	defer releaseFirst()

	second := &agyInteractiveSession{ownerSessionID: "second"}
	secondOpts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"beta"}}}`)(secondOpts)
	if _, err := acquireAgyWorkspaceMCPConfigLease(workDir, secondOpts, second); err == nil {
		t.Fatal("expected conflicting MCP config lease error")
	}

	sameConfig := &agyInteractiveSession{ownerSessionID: "same"}
	sameOpts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"alpha"}}}`)(sameOpts)
	releaseSame, err := acquireAgyWorkspaceMCPConfigLease(workDir, sameOpts, sameConfig)
	if err != nil {
		t.Fatalf("same config lease error = %v", err)
	}
	releaseSame()
	releaseFirst()
	releaseSecond, err := acquireAgyWorkspaceMCPConfigLease(workDir, secondOpts, second)
	if err != nil {
		t.Fatalf("conflict should clear after releases: %v", err)
	}
	releaseSecond()
}

func TestAgyWorkspaceMCPConfigLeaseAllowsConcurrentSessionVariations(t *testing.T) {
	workDir := t.TempDir()

	// Create two sessions with different MCP_SESSION_ID and MCP_API_URL (with session suffix path)
	first := &agyInteractiveSession{ownerSessionID: "first"}
	firstOpts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"alpha","env":{"MCP_SESSION_ID":"session-abc","MCP_API_URL":"http://127.0.0.1:8081/s/session-abc","MCP_VIRTUAL_SCOPE_ID":"scope-1"}}}}`)(firstOpts)

	releaseFirst, err := acquireAgyWorkspaceMCPConfigLease(workDir, firstOpts, first)
	if err != nil {
		t.Fatalf("first lease error = %v", err)
	}
	defer releaseFirst()

	second := &agyInteractiveSession{ownerSessionID: "second"}
	secondOpts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"alpha","env":{"MCP_SESSION_ID":"session-xyz","MCP_API_URL":"http://127.0.0.1:8081/s/session-xyz","MCP_VIRTUAL_SCOPE_ID":"scope-2"}}}}`)(secondOpts)

	releaseSecond, err := acquireAgyWorkspaceMCPConfigLease(workDir, secondOpts, second)
	if err != nil {
		t.Fatalf("concurrent lease should succeed because session-specific parameters are normalized/fingerprinted identically: %v", err)
	}
	defer releaseSecond()
}

func TestBuildAgyInteractiveLaunchAddsConversationBeforePromptInteractive(t *testing.T) {
	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(t.TempDir())(opts)
	WithResumeSessionID("agy-conversation-123")(opts)

	args, _, _, cleanup, err := adapter.buildAgyInteractiveLaunch(opts, "Follow repo rules.", "test-session-agy")
	if err != nil {
		t.Fatalf("build launch: %v", err)
	}
	defer cleanup()

	conversationIdx := indexOfAgyArg(args, "--conversation")
	promptIdx := indexOfAgyArg(args, "--prompt-interactive")
	if conversationIdx < 0 || conversationIdx+1 >= len(args) || args[conversationIdx+1] != "agy-conversation-123" {
		t.Fatalf("args = %#v, want --conversation agy-conversation-123", args)
	}
	if promptIdx < 0 {
		t.Fatalf("args = %#v, want --prompt-interactive", args)
	}
	if promptIdx+1 >= len(args) || args[promptIdx+1] != "" {
		t.Fatalf("args = %#v, want empty --prompt-interactive argument", args)
	}
	if conversationIdx > promptIdx {
		t.Fatalf("--conversation must appear before --prompt-interactive so agy treats it as a flag: %#v", args)
	}
	matches, _ := filepath.Glob(filepath.Join(opts.Metadata.Custom[MetadataKeyWorkingDir].(string), ".agents", "rules", "mlp-system*.md"))
	if len(matches) != 1 {
		t.Fatalf("system rule files = %v, want one temporary Agy rule", matches)
	}
}

func TestBuildAgyInteractiveLaunchAddsAPIKeyAliases(t *testing.T) {
	adapter := NewAgyCLIAdapter("test-key", "agy-cli", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(t.TempDir())(opts)

	_, env, _, cleanup, err := adapter.buildAgyInteractiveLaunch(opts, "", "test-session-agy")
	if err != nil {
		t.Fatalf("build launch: %v", err)
	}
	defer cleanup()

	for _, want := range []string{"AGY_API_KEY=test-key", "GOOGLE_API_KEY=test-key", "GEMINI_API_KEY=test-key"} {
		if !containsString(env, want) {
			t.Fatalf("env = %#v, want %s", env, want)
		}
	}
}

func TestPrepareAgyProjectFilesWritesProjectFilesAndCleansUp(t *testing.T) {
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"node","args":["server.js"]}}}`)(opts)

	cleanup, err := prepareAgyProjectFiles(workDir, "Follow repo rules.", opts, "test-session-agy")
	if err != nil {
		t.Fatalf("prepareAgyProjectFiles error = %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(workDir, ".agents", "rules", "mlp-system*.md"))
	if len(matches) != 1 {
		t.Fatalf("system rule files = %v, want one temporary Agy rule", matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read system rule: %v", err)
	}
	if !strings.Contains(string(body), "Follow repo rules.") {
		t.Fatalf("system rule body = %q, want system prompt", string(body))
	}
	mcpPath := filepath.Join(workDir, ".agents", "mcp_config.json")
	mcpBody, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read mcp_config.json: %v", err)
	}
	if !strings.Contains(string(mcpBody), "api-bridge") {
		t.Fatalf("mcp_config.json = %q, want bridge config", string(mcpBody))
	}

	cleanup()

	if matches, _ := filepath.Glob(filepath.Join(workDir, ".agents", "rules", "mlp-system-*.md")); len(matches) != 0 {
		t.Fatalf("system rule files remain after cleanup: %v", matches)
	}
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Fatalf("mcp_config.json should be removed after cleanup, err=%v", err)
	}
}

func TestPrepareAgyProjectFilesWritesBridgeOnlyHooksAndCleansUp(t *testing.T) {
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithBridgeOnlyTools(true)(opts)

	cleanup, err := prepareAgyProjectFiles(workDir, "", opts, "test-session-agy")
	if err != nil {
		t.Fatalf("prepareAgyProjectFiles error = %v", err)
	}

	hookPath := filepath.Join(workDir, ".agents", "hooks.json")
	hookBody, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var hooks map[string]struct {
		PreToolUse []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"PreToolUse"`
	}
	if err := json.Unmarshal(hookBody, &hooks); err != nil {
		t.Fatalf("hooks.json is not valid JSON: %v", err)
	}
	bridgeHook, ok := hooks["mlp-bridge-only-tools"]
	if !ok || len(bridgeHook.PreToolUse) != 1 {
		t.Fatalf("hooks.json = %s, want one mlp-bridge-only-tools PreToolUse hook", hookBody)
	}
	matcher := bridgeHook.PreToolUse[0].Matcher
	if !strings.Contains(matcher, "run_command") || !strings.Contains(matcher, "write_to_file") ||
		!strings.Contains(matcher, "Read|read|read_file") || !strings.Contains(matcher, "ListDir|list_dir") ||
		!strings.Contains(matcher, "Search|search|grep_search") {
		t.Fatalf("bridge-only matcher = %q, want read, list, search, command, and write tools denied", matcher)
	}
	if len(bridgeHook.PreToolUse[0].Hooks) != 1 {
		t.Fatalf("bridge-only hooks = %#v, want one command hook", bridgeHook.PreToolUse[0].Hooks)
	}
	handler := bridgeHook.PreToolUse[0].Hooks[0]
	if handler.Type != "command" || handler.Timeout != 10 || !strings.Contains(handler.Command, "mlp-bridge-only-hook.sh") {
		t.Fatalf("bridge-only hook handler = %#v, want shell command handler", handler)
	}

	scriptPath := filepath.Join(workDir, ".agents", "mlp-bridge-only-hook.sh")
	scriptBody, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read bridge-only hook script: %v", err)
	}
	if !strings.Contains(string(scriptBody), `"decision":"deny"`) || !strings.Contains(string(scriptBody), "api-bridge.execute_shell_command") {
		t.Fatalf("bridge-only hook script = %q, want deny JSON listing api-bridge tools", string(scriptBody))
	}
	cmd := exec.CommandContext(context.Background(), "sh", scriptPath)
	cmd.Stdin = strings.NewReader(`{"toolCall":{"name":"view_file","args":{"AbsolutePath":"/tmp/secret.txt"}}}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bridge-only hook script execution error = %v, output=%q", err, string(output))
	}
	var decision struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(output, &decision); err != nil {
		t.Fatalf("bridge-only hook script output is not valid JSON: %v; output=%q", err, string(output))
	}
	if decision.Decision != "deny" || !strings.Contains(decision.Reason, "api-bridge.execute_shell_command") {
		t.Fatalf("bridge-only hook decision = %#v, want deny with bridge guidance", decision)
	}
	for _, want := range []string{"$MCP_CUSTOM", "list_published_llms", "list_provider_models", "Do not read or edit config/ files for LLM/provider configuration"} {
		if !strings.Contains(decision.Reason, want) {
			t.Fatalf("bridge-only hook reason missing %q:\n%s", want, decision.Reason)
		}
	}

	cleanup()

	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatalf("hooks.json should be removed after cleanup, err=%v", err)
	}
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Fatalf("bridge-only hook script should be removed after cleanup, err=%v", err)
	}
}

// TestPrepareAgyProjectFilesMergesExistingHooksMidSessionAndNukesOnCleanup
// locks in the OPT-IN merge contract (WithRestoreProjectFiles(true)):
// mid-session the orchestrator's bridge-only hook is merged in alongside
// any operator-supplied entries, but on cleanup the entire .agents/ tree
// is removed — including the operator's original hooks.json. The merge is
// only reachable now that restore is opted in; the default overwrites
// (see TestPrepareAgyProjectFilesOverwritesExistingHooksByDefault).
func TestPrepareAgyProjectFilesMergesExistingHooksMidSessionAndNukesOnCleanup(t *testing.T) {
	workDir := t.TempDir()
	agentsDir := filepath.Join(workDir, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(agentsDir, "hooks.json")
	original := `{"existing-policy":{"PreToolUse":[{"matcher":"read_url_content","hooks":[{"type":"command","command":"echo keep","timeout":1}]}]}}`
	if err := os.WriteFile(hooksPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := &llmtypes.CallOptions{}
	WithBridgeOnlyTools(true)(opts)
	WithRestoreProjectFiles(true)(opts)

	cleanup, err := prepareAgyProjectFiles(workDir, "", opts, "test-session-agy")
	if err != nil {
		t.Fatalf("prepareAgyProjectFiles error = %v", err)
	}
	activeBody, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read active hooks.json: %v", err)
	}
	var active map[string]interface{}
	if err := json.Unmarshal(activeBody, &active); err != nil {
		t.Fatalf("active hooks.json is not valid JSON: %v", err)
	}
	if _, ok := active["existing-policy"]; !ok {
		t.Fatalf("active hooks.json = %s, want existing hook preserved mid-session", activeBody)
	}
	if _, ok := active["mlp-bridge-only-tools"]; !ok {
		t.Fatalf("active hooks.json = %s, want bridge-only hook merged in mid-session", activeBody)
	}

	cleanup()

	if _, err := os.Stat(agentsDir); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove the full .agents/ tree; stat err = %v", err)
	}
}

// TestPrepareAgyProjectFilesOverwritesExistingHooksByDefault locks in the
// DEFAULT (restore off): a pre-existing operator hooks.json is NOT merged —
// our bridge-only hook is written fresh and the operator's entry is gone
// mid-session. This is the "always write fresh, never restore/merge"
// behavior. (Cleanup still nukes the whole .agents/ tree as before.)
func TestPrepareAgyProjectFilesOverwritesExistingHooksByDefault(t *testing.T) {
	workDir := t.TempDir()
	agentsDir := filepath.Join(workDir, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(agentsDir, "hooks.json")
	original := `{"existing-policy":{"PreToolUse":[{"matcher":"read_url_content","hooks":[{"type":"command","command":"echo keep","timeout":1}]}]}}`
	if err := os.WriteFile(hooksPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := &llmtypes.CallOptions{}
	WithBridgeOnlyTools(true)(opts)

	cleanup, err := prepareAgyProjectFiles(workDir, "", opts, "test-session-agy")
	if err != nil {
		t.Fatalf("prepareAgyProjectFiles error = %v", err)
	}
	activeBody, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read active hooks.json: %v", err)
	}
	var active map[string]interface{}
	if err := json.Unmarshal(activeBody, &active); err != nil {
		t.Fatalf("active hooks.json is not valid JSON: %v", err)
	}
	if _, ok := active["existing-policy"]; ok {
		t.Fatalf("default must overwrite, not merge: existing-policy should be gone; got %s", activeBody)
	}
	if _, ok := active["mlp-bridge-only-tools"]; !ok {
		t.Fatalf("active hooks.json = %s, want fresh bridge-only hook installed", activeBody)
	}

	cleanup()

	if _, err := os.Stat(agentsDir); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove the full .agents/ tree; stat err = %v", err)
	}
}

// TestPrepareAgyProjectFilesNukesAgentsTreeOnCleanup locks in that the
// orchestrator-supplied MCP config is written mid-session AND that the
// whole .agents/ tree (including any pre-existing operator content) is
// removed on cleanup. Counterpart to the hooks test above.
func TestPrepareAgyProjectFilesNukesAgentsTreeOnCleanup(t *testing.T) {
	workDir := t.TempDir()
	agentsDir := filepath.Join(workDir, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(agentsDir, "mcp_config.json")
	original := `{"mcpServers":{"old":{"command":"old"}}}`
	if err := os.WriteFile(mcpPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"new":{"command":"new"}}}`)(opts)

	cleanup, err := prepareAgyProjectFiles(workDir, "", opts, "test-session-agy")
	if err != nil {
		t.Fatalf("prepareAgyProjectFiles error = %v", err)
	}
	if got, err := os.ReadFile(mcpPath); err != nil || !strings.Contains(string(got), `"new"`) {
		t.Fatalf("mid-session mcp_config.json = %q err=%v, want orchestrator override", string(got), err)
	}

	cleanup()

	if _, err := os.Stat(agentsDir); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove the full .agents/ tree; stat err = %v", err)
	}
}

func TestPrepareAgyProjectFilesRejectsInvalidMCPConfig(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":`)(opts)

	if cleanup, err := prepareAgyProjectFiles(t.TempDir(), "", opts, "test-session-agy"); err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("expected invalid MCP config error")
	}
}

func TestReadAgyConversationIDForTurnFromHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	appDir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(filepath.Join(appDir, "history.jsonl"), []byte(
		`{"display":"old","workspace":"`+workspace+`","conversationId":"old-id"}`+"\n"+
			`{"display":"prompt","workspace":"`+workspace+`","conversationId":"new-id"}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := readAgyConversationIDForTurn(workspace, "prompt"); got != "new-id" {
		t.Fatalf("conversation id = %q, want new-id", got)
	}
}

func TestReadAgyLatestConversationIDFromCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cacheDir := filepath.Join(home, ".gemini", "antigravity-cli", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(filepath.Join(cacheDir, "last_conversations.json"), []byte(`{"`+workspace+`":"latest-id"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := readAgyLatestConversationID(workspace); got != "latest-id" {
		t.Fatalf("latest conversation id = %q, want latest-id", got)
	}
}

func TestReadAgyConversationIDFromLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logDir := filepath.Join(home, ".gemini", "antigravity-cli", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	logText := `I0524 manager.go:249] Initializing CLI store manager for workspace ` + workspace + `
I0524 server.go:747] Created conversation 11111111-2222-3333-4444-555555555555
I0524 conversation_manager.go:378] Streaming conversation 66666666-7777-8888-9999-aaaaaaaaaaaa
`
	if err := os.WriteFile(filepath.Join(logDir, "cli-20260524_150310.log"), []byte(logText), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := readAgyConversationIDFromLogs(workspace); got != "66666666-7777-8888-9999-aaaaaaaaaaaa" {
		t.Fatalf("conversation id from logs = %q", got)
	}
}

func TestParseAgyStatuslineUsagePrefersCurrentUsage(t *testing.T) {
	raw := []byte(`{
		"email":"secret@example.com",
		"context_window":{
			"total_input_tokens":161,
			"total_output_tokens":356,
			"current_usage":{
				"input_tokens":14861,
				"output_tokens":133,
				"cache_creation_input_tokens":17,
				"cache_read_input_tokens":29
			}
		}
	}`)

	usage, ok := parseAgyStatuslineUsageJSON(raw)
	if !ok {
		t.Fatal("parseAgyStatuslineUsageJSON ok = false")
	}
	if usage.InputTokens != 14861 || usage.OutputTokens != 133 || usage.CacheCreationInputTokens != 17 || usage.CacheReadInputTokens != 29 {
		t.Fatalf("usage = %+v, want current_usage counts", usage)
	}
}

func TestParseAgyStatuslineUsageFallsBackToTotals(t *testing.T) {
	raw := []byte(`{"context_window":{"total_input_tokens":11,"total_output_tokens":22}}`)

	usage, ok := parseAgyStatuslineUsageJSON(raw)
	if !ok {
		t.Fatal("parseAgyStatuslineUsageJSON ok = false")
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 22 {
		t.Fatalf("usage = %+v, want context-window totals", usage)
	}
}

func TestBuildAgyStatuslineCaptureScriptSanitizesUsage(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "usage.json")
	scriptPath := filepath.Join(dir, "capture.sh")
	if err := os.WriteFile(scriptPath, []byte(buildAgyStatuslineCaptureScript(outputPath)), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.CommandContext(t.Context(), "sh", scriptPath)
	cmd.Stdin = strings.NewReader(`{"email":"secret@example.com","plan_tier":"Business","context_window":{"total_input_tokens":41,"total_output_tokens":42,"current_usage":{"input_tokens":123,"output_tokens":45,"cache_creation_input_tokens":6,"cache_read_input_tokens":7}}}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run script: %v output=%s", err, out)
	}
	body, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.Contains(string(body), "secret@example.com") || strings.Contains(string(body), "Business") {
		t.Fatalf("sanitized usage leaked account metadata: %s", body)
	}
	usage, ok := parseAgyStatuslineUsageJSON(body)
	if !ok {
		t.Fatalf("parse sanitized usage failed: %s", body)
	}
	if usage.InputTokens != 123 || usage.OutputTokens != 45 || usage.CacheCreationInputTokens != 6 || usage.CacheReadInputTokens != 7 {
		t.Fatalf("usage = %+v, want sanitized current_usage counts", usage)
	}
}

func TestAgyTmuxTokenCountsStatuslineAddsCacheMetadata(t *testing.T) {
	additional := map[string]interface{}{}
	input, output, cacheRead := agyTmuxTokenCounts("prompt", "reply", &agyStatuslineUsage{
		InputTokens:              10,
		OutputTokens:             5,
		CacheCreationInputTokens: 3,
		CacheReadInputTokens:     7,
	}, additional)

	if input != 13 || output != 5 || cacheRead != 7 {
		t.Fatalf("counts = (%d, %d, %d), want (13, 5, 7)", input, output, cacheRead)
	}
	if got := additional["agy_token_usage_source"]; got != "statusline" {
		t.Fatalf("agy_token_usage_source = %v, want statusline", got)
	}
	if got := additional["cache_creation_input_tokens"]; got != 3 {
		t.Fatalf("cache_creation_input_tokens = %v, want 3", got)
	}
	if got := additional["cache_read_input_tokens"]; got != 7 {
		t.Fatalf("cache_read_input_tokens = %v, want 7", got)
	}
}

func TestEstimateAgyTmuxTokensRoundsUp(t *testing.T) {
	input, output := estimateAgyTmuxTokens("12345", "123456789")
	if input != 2 || output != 3 {
		t.Fatalf("estimateAgyTmuxTokens = (%d, %d), want (2, 3)", input, output)
	}
}

func indexOfAgyArg(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
