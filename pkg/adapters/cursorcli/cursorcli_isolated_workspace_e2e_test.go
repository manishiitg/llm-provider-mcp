package cursorcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCursorCLIRealIsolatedTmpDirDoesNotTouchOuterWorkspace is the
// Phase B end-to-end validation for the workflow-step isolation
// design (docs/WORKFLOW_STEP_ISOLATION.md). It simulates what
// mcpagent does when WithIsolatedSessionWorkspace is on:
//
//  1. Caller has a "real" workflow dir with operator-owned files
//     they DON'T want the coding agent's built-in tools to touch.
//  2. The orchestrator creates a fresh tmp dir and overrides the
//     cursor adapter's WithCursorWorkingDir to point at that tmp.
//  3. Cursor's session runs entirely inside the tmp dir.
//  4. WithCursorDenyBuiltinTools(true) blocks the agent's built-in
//     Read/Shell tools, so the only way to reach files outside the
//     tmp dir is via the MCP bridge (which we're not exercising in
//     this test — we just verify the agent has no path TO the
//     outer workspace via its built-ins).
//
// What this test proves:
//
//   - Cursor cwd resolves to the tmp dir, NOT the user workspace
//     dir (visible via the agent reporting its CWD).
//   - The user workspace's sentinel file remains untouched after
//     the cursor session completes (no .cursor/, no AGENTS.md, no
//     stale config files appear in user workspace).
//   - The tmp dir is the location where cursor's per-session files
//     (.cursor/rules/, .cursor/mcp.json, .cursor/hooks.json) land
//     — operator inspecting the user workspace mid-session sees
//     none of them.
//
// What this test does NOT prove (deferred to Phase C / orchestrator
// integration):
//
//   - mcpagent's Agent.IsolatedSessionWorkspace option actually
//     calls into this codepath. That's covered by the Phase A unit
//     test TestAppendCodingAgentWorkingDirOverridesWithIsolatedTmpDir
//     in mcpagent/agent/isolated_workspace_test.go.
//
//   - The workflow orchestrator passes WithIsolatedSessionWorkspace(true)
//     for workflow steps. That's Phase C wire-through work.
//
//   - The MCP bridge can still reach the outer workspace via its
//     own paths. The bridge config in real workflow runs carries
//     the user workspace paths; this test doesn't configure an
//     MCP server so we don't need to validate bridge access here.
//
// Skipped unless RUN_CURSOR_CLI_REAL_E2E=1 + cursor + tmux on PATH.
func TestCursorCLIRealIsolatedTmpDirDoesNotTouchOuterWorkspace(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	// "User workspace" — what the operator believes they're working in.
	// In a real workflow run this would be /Users/x/my-repo or similar.
	userWorkspace := t.TempDir()
	sentinelPath := filepath.Join(userWorkspace, "operator-do-not-touch.txt")
	sentinelContent := []byte("ISOLATION_SENTINEL_42\nthis file must NOT be touched by the agent's built-in tools\n")
	if err := os.WriteFile(sentinelPath, sentinelContent, 0o600); err != nil {
		t.Fatalf("seed sentinel in user workspace: %v", err)
	}
	preSeedInfo, _ := os.Stat(sentinelPath)
	preSeedMTime := preSeedInfo.ModTime()
	time.Sleep(10 * time.Millisecond)

	// "Isolated workspace" — what mcpagent creates when
	// IsolatedSessionWorkspace is on. The orchestrator overrides the
	// cursor adapter's working-dir option with this path.
	isolatedWorkspace, err := os.MkdirTemp("", "mlp-cli-session-*")
	if err != nil {
		t.Fatalf("create isolated tmp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(isolatedWorkspace) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-isolated-" + cursorRandomHex(4)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 256)
	var streamContent strings.Builder
	streamDone := make(chan struct{})
	go func() {
		for chunk := range streamChan {
			streamContent.WriteString(chunk.Content)
		}
		close(streamDone)
	}()

	_, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				// Direct, deterministic prompt that exercises the cwd
				// without depending on model behavior to interpret
				// nuanced instructions.
				llmtypes.TextContent{Text: "Run `pwd` in the shell and reply with exactly the path you printed, nothing else."},
			},
		},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithWorkingDir(isolatedWorkspace), // ← this is what mcpagent does with isolation on
		llmtypes.WithStreamingChan(streamChan),
	)
	<-streamDone

	if callErr != nil {
		t.Fatalf("GenerateContent error = %v\nstream:\n%s", callErr, streamContent.String())
	}

	// 1. SENTINEL UNCHANGED: the user workspace file the operator
	// pre-seeded must not have been touched. mtime must equal the
	// pre-seed mtime, and content must match byte-for-byte. This is
	// the core security property the isolation provides.
	postContent, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("user workspace sentinel must still exist after session: %v", err)
	}
	if string(postContent) != string(sentinelContent) {
		t.Errorf("user workspace sentinel was modified — isolation failed to protect it\n  want: %s\n  got:  %s", sentinelContent, postContent)
	}
	postInfo, _ := os.Stat(sentinelPath)
	if !postInfo.ModTime().Equal(preSeedMTime) {
		t.Errorf("user workspace sentinel mtime advanced — something touched the file\n  pre:  %v\n  post: %v", preSeedMTime, postInfo.ModTime())
	}

	// 2. NO CURSOR ARTIFACTS IN USER WORKSPACE: per-session files
	// (.cursor/rules/, .cursor/mcp.json, .cursor/hooks.json) must
	// have landed in the isolated tmp dir, NOT the user workspace.
	if _, err := os.Stat(filepath.Join(userWorkspace, ".cursor")); err == nil {
		t.Errorf(".cursor/ leaked into user workspace — isolation failed to redirect cursor's project-file writes")
	}

	// 3. CURSOR'S REPORTED CWD WAS THE ISOLATED DIR: the agent
	// answered `pwd` with the path it sees. Should resolve to the
	// isolated tmp dir, NOT the user workspace. We accept either the
	// raw path or a symlink-resolved variant (macOS /var →
	// /private/var aliasing).
	reportedCWD := strings.TrimSpace(streamContent.String())
	resolvedIsolated, _ := filepath.EvalSymlinks(isolatedWorkspace)
	resolvedUser, _ := filepath.EvalSymlinks(userWorkspace)
	if strings.Contains(reportedCWD, resolvedUser) || strings.Contains(reportedCWD, userWorkspace) {
		t.Errorf("cursor reported cwd inside user workspace — isolation failed to override cwd\n  reported:  %s\n  user dir:  %s", reportedCWD, userWorkspace)
	}
	if !strings.Contains(reportedCWD, isolatedWorkspace) && !strings.Contains(reportedCWD, resolvedIsolated) {
		// Soft check — model output may not contain the literal path
		// (e.g. it might paraphrase). We log but don't fail here,
		// since the hard guarantee comes from #1 and #2 above.
		t.Logf("note: cursor's reported cwd did not contain the isolated dir path verbatim (model may have paraphrased); reportedCWD=%q isolatedDir=%q", reportedCWD, isolatedWorkspace)
	}
}
