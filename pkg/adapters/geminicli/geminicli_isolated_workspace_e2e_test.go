package geminicli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestGeminiCLIRealIsolatedTmpDirDoesNotTouchOuterWorkspace is the
// Phase B+ workflow-step isolation E2E for Gemini CLI, templated
// from cursorcli_isolated_workspace_e2e_test.go. See the cursor
// variant for the shared rationale.
//
// Gemini-specific anchors:
//   - Working-dir option is WithWorkingDir.
//   - Leaked artifacts to guard against are GEMINI.md and .gemini/.
//   - Gemini-cli's own projectDir (separate from workingDir) lives
//     in /tmp/gemini-cli-project-* — that's gemini's launch cwd
//     and is independent of the isolation guarantee being tested.
//     userWorkspace remains the operator's workflow dir.
//
// Skipped unless RUN_GEMINI_CLI_REAL_E2E=1 + GEMINI_API_KEY + gemini.
func TestGeminiCLIRealIsolatedTmpDirDoesNotTouchOuterWorkspace(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })
	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY required")
	}

	userWorkspace := t.TempDir()
	sentinelPath := filepath.Join(userWorkspace, "operator-do-not-touch.txt")
	sentinelContent := []byte("ISOLATION_SENTINEL_GEMINI_42\nmust not be touched by gemini built-ins\n")
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

	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	_, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply with exactly the word OK and nothing else."}},
		},
	},
		WithInteractiveSessionID("gemini-isolated-"+geminiRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(isolatedWorkspace),
		WithApprovalMode("yolo"),
		WithProjectSettings(`{}`),
	)
	if callErr != nil {
		t.Fatalf("GenerateContent error = %v", callErr)
	}
	if err := CleanupGeminiCLIInteractiveSessions(context.Background()); err != nil {
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
	for _, leak := range []string{"GEMINI.md", ".gemini"} {
		if _, err := os.Stat(filepath.Join(userWorkspace, leak)); err == nil {
			t.Errorf("%q leaked into user workspace — isolation failed", leak)
		}
	}
}
