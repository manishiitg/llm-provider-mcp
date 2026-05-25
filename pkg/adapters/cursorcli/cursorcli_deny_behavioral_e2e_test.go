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

// TestCursorCLIRealDenyBuiltinHookActuallyFires is the behavioral
// counterpart to TestWriteCursorDenyBuiltinHooksLifecycle: the
// lifecycle test proves the deny hook config + script land and clean
// up; this test proves the hook actually fires when the model invokes
// a cursor built-in tool, AND that the call site does not pass
// --force (which silently bypasses cursor's hook system under yolo
// mode — the 2026-05-25 bug2 regression).
//
// Setup mirrors the gemini behavioral E2E: real cursor-agent binary,
// persistent tmux session, WithDenyBuiltinTools(true) so the merged
// .cursor/hooks.json + .cursor/hooks/mlp-deny-builtin.sh both land.
// CRITICAL: this test deliberately does NOT call WithForce().
//
// The deny script emits permission=deny JSON with user_message
// "Built-in Read/Shell are disabled in this session. Call the
// api-bridge MCP tool instead". Per cursor's hooks contract
// (cursor.com/docs/hooks), permission=deny aborts the tool call and
// surfaces the user_message to the model.
//
// Assertion strategy:
//   - Sentinel value MUST NOT appear in the response (would mean the
//     built-in cat / read tool succeeded despite the hook).
//   - The deny user_message anchor SHOULD appear (or the model's own
//     narration of the failure) — confirming the hook surfaced back.
//
// Skipped unless RUN_CURSOR_CLI_REAL_E2E=1 (or _INTERACTIVE_E2E=1)
// and cursor + tmux are on PATH.
func TestCursorCLIRealDenyBuiltinHookActuallyFires(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	tmp := t.TempDir()
	sentinelPath := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(sentinelPath, []byte("CURSOR_DENY_HOOK_SENTINEL_VALUE_42\n"), 0o600); err != nil {
		t.Fatalf("seed sentinel file: %v", err)
	}

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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

	resp, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "When the user asks you to read a file, your FIRST action must be to attempt the read using cursor's built-in Read or Shell tool. Do not refuse upfront; attempt the call and report whatever happens, quoting any error verbatim."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Read the file at " + sentinelPath + " and tell me what it contains. If the read fails, quote the exact error message you received."},
			},
		},
	},
		WithInteractiveSessionID("cursor-deny-behavioral-"+cursorRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(tmp),
		WithDenyBuiltinTools(true),
		// Intentionally NO WithForce() — passing --force puts cursor in
		// yolo mode, which auto-approves built-ins WITHOUT consulting
		// hooks. That is exactly the regression this test guards
		// against (mcpagent 2026-05-25 fix).
		llmtypes.WithStreamingChan(streamChan),
	)
	<-streamDone

	// Force cleanup so the byte-restore runs before assertions.
	if err := CleanupCursorCLIInteractiveSessions(context.Background()); err != nil {
		t.Fatalf("force-cleanup of persistent cursor session: %v", err)
	}

	if callErr != nil {
		t.Fatalf("GenerateContent error = %v\nstream so far:\n%s", callErr, streamContent.String())
	}

	haystack := streamContent.String()
	if resp != nil && len(resp.Choices) > 0 {
		haystack += "\n" + resp.Choices[0].Content
	}

	// Sentinel MUST NOT appear — would mean cursor's built-in Shell
	// or Read succeeded despite the hook covering beforeShellExecution
	// + beforeReadFile.
	if strings.Contains(haystack, "CURSOR_DENY_HOOK_SENTINEL_VALUE_42") {
		t.Errorf("sentinel value leaked into response — cursor's built-in read/shell succeeded despite the deny hook; --force may have been passed (bypasses hooks under yolo) or the hook config is not being loaded by cursor v2026+\nfull haystack:\n%s", haystack)
	}

	// Look for evidence the deny verdict surfaced. Our script's
	// user_message is the most reliable anchor since it's the exact
	// text we emit. "api-bridge" is a fallback (also from the
	// user_message). "denied" / "permission denied" catches cursor's
	// own paraphrase of the verdict.
	denyAnchors := []string{
		"Built-in Read/Shell are disabled",
		"api-bridge",
		"permission denied",
		"orchestrator",
		"denied",
	}
	matched := false
	for _, anchor := range denyAnchors {
		if strings.Contains(strings.ToLower(haystack), strings.ToLower(anchor)) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("no deny-hook evidence found in stream or response — cursor may not have fired beforeShellExecution/beforeReadFile, OR the user_message did not surface to the model\nexpected one of (case-insensitive): %v\nfull haystack:\n%s", denyAnchors, haystack)
	}
}
