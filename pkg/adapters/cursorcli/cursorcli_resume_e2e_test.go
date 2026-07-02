package cursorcli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCursorCLIRealCrossRestartResume is the behavioral lock-in for
// cursor's resume support across a simulated server restart. Without
// this test, the orchestrator wiring + adapter cooperation that
// makes --resume work end-to-end has no real-binary coverage, and
// the tmux adapter's silent failure to publish cursor_session_id
// would not be caught.
//
// Flow:
//  1. Turn 1: tell cursor a sentinel ("CROSS_RESTART_SENTINEL_42").
//     Capture the cursor_session_id from response.GenerationInfo.Additional.
//  2. Force-cleanup the persistent tmux session, simulating the
//     in-memory cursorPersistentRegistry loss that happens on
//     server restart. Allocate a NEW adapter instance and a NEW
//     owner session ID so nothing is inherited in-process.
//  3. Turn 2: with WithResumeSessionID(captured), ask cursor what
//     the sentinel was. If --resume is honored end-to-end, cursor
//     replays its store.db conversation and the sentinel comes
//     back in the response. If resume is broken (no session ID
//     captured, or cursor doesn't honor --resume in tmux mode),
//     cursor has no memory of turn 1 and the sentinel is absent.
//
// Same workingDir is used across both turns because cursor scopes
// its chat store by md5(cwd); resuming from a different cwd would
// not find the prior session.
//
// Skipped unless RUN_CURSOR_CLI_REAL_E2E=1 (or _INTERACTIVE_E2E=1)
// and cursor + tmux are on PATH.
func TestCursorCLIRealCrossRestartResume(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	workingDir := t.TempDir()
	sentinel := "CROSS_RESTART_SENTINEL_42"

	// --- Turn 1: seed the sentinel, capture the native session ID.
	adapter1 := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx1, cancel1 := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel1()

	resp1, err := adapter1.GenerateContent(ctx1, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: fmt.Sprintf("Remember this exact token for the rest of our conversation: %s. Reply with just the word ACK.", sentinel)},
			},
		},
	},
		WithInteractiveSessionID("cursor-resume-turn1-"+cursorRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workingDir),
	)
	if err != nil {
		t.Fatalf("turn 1 GenerateContent error = %v", err)
	}
	if resp1 == nil || len(resp1.Choices) == 0 {
		t.Fatalf("turn 1 returned no choices")
	}
	if resp1.Choices[0].GenerationInfo == nil || resp1.Choices[0].GenerationInfo.Additional == nil {
		t.Fatalf("turn 1 response missing GenerationInfo.Additional — cursor adapter must publish per-turn metadata")
	}
	rawSessionID, ok := resp1.Choices[0].GenerationInfo.Additional["cursor_session_id"]
	if !ok {
		t.Fatalf("turn 1 response missing additional[cursor_session_id]; tmux adapter is not publishing cursor's native session id, so cross-restart resume cannot work\nadditional keys: %v", keysOf(resp1.Choices[0].GenerationInfo.Additional))
	}
	nativeSessionID, ok := rawSessionID.(string)
	if !ok || strings.TrimSpace(nativeSessionID) == "" {
		t.Fatalf("additional[cursor_session_id] = %v (type %T); want non-empty string", rawSessionID, rawSessionID)
	}
	t.Logf("captured cursor native session ID: %s", nativeSessionID)

	// --- Simulated restart: kill the in-memory persistent tmux session.
	// After this, the new adapter cannot reuse the old session and
	// must rely on --resume to recover the prior conversation.
	if err := CleanupCursorCLIInteractiveSessions(context.Background()); err != nil {
		t.Fatalf("simulated restart: cleanup of persistent cursor sessions failed: %v", err)
	}

	// --- Turn 2: fresh adapter, fresh owner session, --resume the captured ID.
	adapter2 := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel2()

	resp2, err := adapter2.GenerateContent(ctx2, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What was the exact token I asked you to remember earlier in this conversation? Reply with just the token and nothing else."},
			},
		},
	},
		WithInteractiveSessionID("cursor-resume-turn2-"+cursorRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workingDir),
		WithResumeSessionID(nativeSessionID),
	)
	if err != nil {
		t.Fatalf("turn 2 GenerateContent error = %v", err)
	}
	if resp2 == nil || len(resp2.Choices) == 0 {
		t.Fatalf("turn 2 returned no choices")
	}
	body := resp2.Choices[0].Content
	if !strings.Contains(body, sentinel) {
		t.Errorf("turn 2 response missing sentinel %q — cursor does not appear to have resumed turn-1 conversation\nresponse body:\n%s", sentinel, body)
	}
}

// TestCursorCLIRealCrossRestartResumeWithMCPBridge is the combined contract
// missing from the separate resume-only and MCP-only E2Es: a native Cursor
// session that was born with .cursor/mcp.json should still expose the MCP tools
// after a simulated restart + cursor-agent --resume.
func TestCursorCLIRealCrossRestartResumeWithMCPBridge(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	workingDir := t.TempDir()
	rememberToken := "RESUME_MCP_MEMORY_" + cursorRandomHex(4)
	firstBridgeToken := "RESUME_MCP_FIRST_" + cursorRandomHex(4)
	secondBridgeToken := "RESUME_MCP_SECOND_" + cursorRandomHex(4)

	mcpServerPath := writeCursorTmuxContractMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)
	preApproveCursorMCP(t, workingDir, mcpConfig, "api-bridge")

	adapter1 := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSession1 := "cursor-resume-mcp-turn1-" + cursorRandomHex(4)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel1()

	resp1, err := adapter1.GenerateContent(ctx1, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "You have access to MCP tools. Use the api-bridge MCP tool when asked. Keep answers concise."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: fmt.Sprintf(
					"Remember this exact token for our conversation: %s. Then call api-bridge contract_echo_token with token %s and reply with the exact tool result.",
					rememberToken, firstBridgeToken,
				)},
			},
		},
	},
		WithInteractiveSessionID(ownerSession1),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workingDir),
		WithMCPConfig(mcpConfig),
		WithApproveMCPs(),
		WithDenyBuiltinTools(true),
	)
	if err != nil {
		t.Fatalf("turn 1 GenerateContent error = %v", err)
	}
	firstWant := "BRIDGE_TOOL_OK_" + firstBridgeToken
	firstPane := ""
	if tmuxSession, ok := activeCursorInteractiveSession(ownerSession1); ok && tmuxSession != "" {
		firstPane, _ = captureCursorPane(context.Background(), tmuxSession)
	}
	firstBody := ""
	if resp1 != nil && len(resp1.Choices) > 0 && resp1.Choices[0] != nil {
		firstBody = resp1.Choices[0].Content
	}
	if !strings.Contains(firstBody+"\n"+firstPane, firstWant) {
		t.Fatalf("turn 1 response missing MCP bridge result %q\nresponse body:\n%s\npane:\n%s", firstWant, firstBody, firstPane)
	}
	if resp1.Choices[0].GenerationInfo == nil || resp1.Choices[0].GenerationInfo.Additional == nil {
		t.Fatalf("turn 1 response missing GenerationInfo.Additional")
	}
	rawSessionID, ok := resp1.Choices[0].GenerationInfo.Additional["cursor_session_id"]
	if !ok {
		t.Fatalf("turn 1 response missing additional[cursor_session_id]; keys=%v", keysOf(resp1.Choices[0].GenerationInfo.Additional))
	}
	nativeSessionID, ok := rawSessionID.(string)
	if !ok || strings.TrimSpace(nativeSessionID) == "" {
		t.Fatalf("additional[cursor_session_id] = %v (type %T); want non-empty string", rawSessionID, rawSessionID)
	}
	t.Logf("captured cursor native session ID for MCP resume: %s", nativeSessionID)

	if err := CleanupCursorCLIInteractiveSessions(context.Background()); err != nil {
		t.Fatalf("simulated restart: cleanup of persistent cursor sessions failed: %v", err)
	}

	adapter2 := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSession2 := "cursor-resume-mcp-turn2-" + cursorRandomHex(4)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel2()

	resp2, err := adapter2.GenerateContent(ctx2, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "You have access to MCP tools. Use the api-bridge MCP tool when asked. Keep answers concise."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: fmt.Sprintf(
					"What exact token did I ask you to remember earlier? Then call api-bridge contract_echo_token with token %s. Reply with both the remembered token and the exact tool result.",
					secondBridgeToken,
				)},
			},
		},
	},
		WithInteractiveSessionID(ownerSession2),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workingDir),
		WithResumeSessionID(nativeSessionID),
		WithMCPConfig(mcpConfig),
		WithApproveMCPs(),
		WithDenyBuiltinTools(true),
	)
	if err != nil {
		t.Fatalf("turn 2 GenerateContent with --resume + MCP bridge error = %v", err)
	}
	if resp2 == nil || len(resp2.Choices) == 0 {
		t.Fatalf("turn 2 returned no choices")
	}
	secondContent := resp2.Choices[0].Content
	secondPane := ""
	if tmuxSession, ok := activeCursorInteractiveSession(ownerSession2); ok && tmuxSession != "" {
		secondPane, _ = captureCursorPane(context.Background(), tmuxSession)
	}
	secondHaystack := secondContent + "\n" + secondPane
	secondWant := "BRIDGE_TOOL_OK_" + secondBridgeToken
	if !strings.Contains(secondHaystack, rememberToken) {
		t.Fatalf("turn 2 response missing remembered token %q; cursor native resume did not restore conversation\nresponse body:\n%s", rememberToken, secondContent)
	}
	if !strings.Contains(secondHaystack, secondWant) {
		t.Fatalf("turn 2 response missing MCP bridge result %q after --resume\nresponse body:\n%s", secondWant, secondContent)
	}
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
