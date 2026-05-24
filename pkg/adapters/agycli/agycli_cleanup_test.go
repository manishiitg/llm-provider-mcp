package agycli

import (
	"context"
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
