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

func TestBuildAgyInteractiveLaunchAddsConversationBeforePromptInteractive(t *testing.T) {
	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(t.TempDir())(opts)
	WithResumeSessionID("agy-conversation-123")(opts)

	args, _, _, cleanup, err := adapter.buildAgyInteractiveLaunch(opts, "Follow repo rules.")
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
	matches, _ := filepath.Glob(filepath.Join(opts.Metadata.Custom[MetadataKeyWorkingDir].(string), ".agents", "rules", "mlp-system-*.md"))
	if len(matches) != 1 {
		t.Fatalf("system rule files = %v, want one temporary Agy rule", matches)
	}
}

func TestPrepareAgyProjectFilesWritesProjectFilesAndCleansUp(t *testing.T) {
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"node","args":["server.js"]}}}`)(opts)

	cleanup, err := prepareAgyProjectFiles(workDir, "Follow repo rules.", opts)
	if err != nil {
		t.Fatalf("prepareAgyProjectFiles error = %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(workDir, ".agents", "rules", "mlp-system-*.md"))
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

	cleanup, err := prepareAgyProjectFiles(workDir, "", opts)
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
	if !strings.Contains(matcher, "run_command") || !strings.Contains(matcher, "write_to_file") {
		t.Fatalf("bridge-only matcher = %q, want command and write tools denied", matcher)
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
	if !strings.Contains(string(scriptBody), `"decision":"deny"`) || !strings.Contains(string(scriptBody), "MCP bridge-only mode") {
		t.Fatalf("bridge-only hook script = %q, want deny JSON with bridge-only reason", string(scriptBody))
	}

	cleanup()

	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatalf("hooks.json should be removed after cleanup, err=%v", err)
	}
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Fatalf("bridge-only hook script should be removed after cleanup, err=%v", err)
	}
}

func TestPrepareAgyProjectFilesMergesAndRestoresExistingHooks(t *testing.T) {
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

	cleanup, err := prepareAgyProjectFiles(workDir, "", opts)
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
		t.Fatalf("active hooks.json = %s, want existing hook preserved", activeBody)
	}
	if _, ok := active["mlp-bridge-only-tools"]; !ok {
		t.Fatalf("active hooks.json = %s, want bridge-only hook merged", activeBody)
	}

	cleanup()

	restored, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read restored hooks.json: %v", err)
	}
	if string(restored) != original {
		t.Fatalf("restored hooks.json = %q, want original", string(restored))
	}
}

func TestPrepareAgyProjectFilesRestoresExistingMCPConfig(t *testing.T) {
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

	cleanup, err := prepareAgyProjectFiles(workDir, "", opts)
	if err != nil {
		t.Fatalf("prepareAgyProjectFiles error = %v", err)
	}
	if got, err := os.ReadFile(mcpPath); err != nil || !strings.Contains(string(got), `"new"`) {
		t.Fatalf("temporary mcp_config.json = %q err=%v, want override", string(got), err)
	}

	cleanup()

	if got, err := os.ReadFile(mcpPath); err != nil || string(got) != original {
		t.Fatalf("restored mcp_config.json = %q err=%v, want original", string(got), err)
	}
}

func TestPrepareAgyProjectFilesRejectsInvalidMCPConfig(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":`)(opts)

	if cleanup, err := prepareAgyProjectFiles(t.TempDir(), "", opts); err == nil {
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

func indexOfAgyArg(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}
