package agycli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestAgyCLIRealIsolatedTmpDirDoesNotTouchOuterWorkspace is the
// Phase B+ workflow-step isolation E2E for Antigravity CLI,
// templated from cursorcli_isolated_workspace_e2e_test.go.
//
// Agy-specific anchors:
//   - Working-dir option is WithWorkingDir.
//   - Leaked artifacts: .agents/ (rules, mcp_config.json, hooks.json).
//
// Skipped unless RUN_AGY_CLI_REAL_E2E=1 (or RUN_AGY_CLI_INTERACTIVE_E2E=1)
// and agy+tmux are on PATH.
func TestAgyCLIRealIsolatedTmpDirDoesNotTouchOuterWorkspace(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	userWorkspace := t.TempDir()
	sentinelPath := filepath.Join(userWorkspace, "operator-do-not-touch.txt")
	sentinelContent := []byte("ISOLATION_SENTINEL_AGY_42\nmust not be touched by agy built-ins\n")
	if err := os.WriteFile(sentinelPath, sentinelContent, 0o600); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}
	preInfo, _ := os.Stat(sentinelPath)
	preSeedMTime := preInfo.ModTime()
	time.Sleep(10 * time.Millisecond)

	isolatedWorkspace, err := os.MkdirTemp("", "mlp-cli-session-*")
	if err != nil {
		t.Fatalf("create isolated tmp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(isolatedWorkspace) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	_, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply with exactly the word OK and nothing else."}},
		},
	},
		WithInteractiveSessionID("agy-isolated-"+agyRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(isolatedWorkspace),
	)
	if callErr != nil {
		t.Fatalf("GenerateContent error = %v", callErr)
	}
	if err := CleanupAgyCLIInteractiveSessions(context.Background()); err != nil {
		t.Fatalf("force-cleanup: %v", err)
	}

	postContent, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("sentinel must still exist: %v", err)
	}
	if string(postContent) != string(sentinelContent) {
		t.Errorf("sentinel was modified\n  want: %s\n  got:  %s", sentinelContent, postContent)
	}
	postInfo, _ := os.Stat(sentinelPath)
	if !postInfo.ModTime().Equal(preSeedMTime) {
		t.Errorf("sentinel mtime advanced — something touched the file")
	}
	if _, err := os.Stat(filepath.Join(userWorkspace, ".agents")); err == nil {
		t.Errorf(".agents/ leaked into user workspace — isolation failed")
	}
}
