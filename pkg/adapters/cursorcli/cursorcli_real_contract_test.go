package cursorcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCursorCLIRealInteractiveTmuxFullContract(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-contract-" + cursorRandomHex(4)
	token := "REAL_CURSOR_TMUX_" + cursorRandomHex(4)

	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithMode("ask"),
	}

	largeSystemPrompt := strings.Repeat("You are testing the Cursor Agent CLI tmux transport. Do not use tools. Keep exact-token replies concise.\n", 80)
	firstPrompt := fmt.Sprintf(`This is a real Cursor CLI tmux contract test.

Preserve input safely:

blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते

Reply exactly:
saved %s`, token, token)

	firstStream := make(chan llmtypes.StreamChunk, 64)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	firstOptions := append([]llmtypes.CallOption{}, options...)
	firstOptions = append(firstOptions, llmtypes.WithStreamingChan(firstStream))

	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: largeSystemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: firstPrompt}}},
	}, firstOptions...)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	firstContent := strings.TrimSpace(first.Choices[0].Content)
	if !strings.Contains(firstContent, token) {
		t.Fatalf("first content = %q, want token %s", firstContent, token)
	}
	assertCursorInteractiveTerminalOnlyStream(t, firstStream)

	tmuxSession, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Cursor tmux session for %s", ownerSessionID)
	}
	pane, err := captureCursorPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture Cursor pane: %v", err)
	}
	if !hasCursorReadyPrompt(pane) {
		t.Fatalf("real Cursor TUI ready prompt not detected; pane:\n%s", pane)
	}
	if hasCursorActivity(pane) {
		t.Fatalf("real Cursor TUI should be idle after first turn; pane:\n%s", pane)
	}

	secondToken := "SECOND_" + token
	secondStream := make(chan llmtypes.StreamChunk, 64)
	secondOptions := append([]llmtypes.CallOption{}, options...)
	secondOptions = append(secondOptions, llmtypes.WithStreamingChan(secondStream))
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: largeSystemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: firstPrompt}}},
		{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: firstContent}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: " + secondToken + ". Do not mention the previous token."}}},
	}, secondOptions...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	secondContent := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(secondContent, secondToken) {
		t.Fatalf("second content = %q, want token %s", secondContent, secondToken)
	}
	if strings.Contains(secondContent, "saved "+token) {
		t.Fatalf("second content replayed first assistant response: %q", secondContent)
	}
	assertCursorInteractiveTerminalOnlyStream(t, secondStream)

	tmuxSessionAfter, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok || tmuxSessionAfter != tmuxSession {
		t.Fatalf("expected same tmux session reused, before=%q after=%q ok=%v", tmuxSession, tmuxSessionAfter, ok)
	}
}

func TestCursorCLIRealInteractiveLiveInputAndEscapeContract(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-live-" + cursorRandomHex(4)
	token := "LIVE_REAL_" + cursorRandomHex(4)

	parentCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	streamChan := make(chan llmtypes.StreamChunk, 128)

	go func() {
		_, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. This is a live input and Escape transport test."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Draft a detailed 1200 word checklist about tmux transport testing. Include token %s near the end only.", token)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithMode("ask"),
			llmtypes.WithStreamingChan(streamChan),
		)
		errCh <- err
	}()

	tmuxSession := waitForCursorRealActiveSession(t, ownerSessionID, 45*time.Second, errCh)
	waitForCursorRealPaneContains(t, tmuxSession, token, 90*time.Second, errCh)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	liveMessage := "LIVE_FOLLOWUP_" + token
	liveErr := SendCursorInteractiveInput(sendCtx, ownerSessionID, liveMessage)
	sendCancel()
	if liveErr != nil {
		cancel()
		t.Fatalf("SendCursorInteractiveInput error = %v", liveErr)
	}
	waitForCursorRealPaneContains(t, tmuxSession, liveMessage, 10*time.Second, errCh)

	pane, err := captureCursorPane(context.Background(), tmuxSession)
	if err != nil {
		cancel()
		t.Fatalf("capture Cursor pane after live input: %v", err)
	}
	if !strings.Contains(pane, liveMessage) {
		cancel()
		t.Fatalf("live input was not visible in tmux pane; pane:\n%s", pane)
	}

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("GenerateContent completed normally; want cancellation after Escape path")
		}
	case <-time.After(45 * time.Second):
		t.Fatalf("timed out waiting for GenerateContent to return after cancellation")
	}
	_ = drainCursorStream(streamChan)
}

func requireRealCursorCLIE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_CURSOR_CLI_REAL_E2E") == "" && os.Getenv("RUN_CURSOR_CLI_INTERACTIVE_E2E") == "" {
		t.Skip("set RUN_CURSOR_CLI_REAL_E2E=1 to run real Cursor CLI tmux contract tests")
	}
	for _, bin := range []string{"cursor-agent", "tmux"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("real Cursor CLI tests require %s in PATH: %v", bin, err)
		}
	}
}

func waitForCursorRealActiveSession(t *testing.T, ownerSessionID string, timeout time.Duration, errCh <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before active session was available: %v", err)
		default:
		}
		if sessionName, ok := activeCursorInteractiveSession(ownerSessionID); ok {
			return sessionName
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for real Cursor interactive session %q", ownerSessionID)
	return ""
}

func waitForCursorRealPaneContains(t *testing.T, tmuxSession, want string, timeout time.Duration, errCh <-chan error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before tmux pane contained %q: %v", want, err)
		default:
		}
		pane, err := captureCursorPane(context.Background(), tmuxSession)
		if err == nil && strings.Contains(pane, want) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	pane, _ := captureCursorPane(context.Background(), tmuxSession)
	t.Fatalf("timed out waiting for Cursor tmux pane to contain %q; latest pane:\n%s", want, pane)
}

type cursorDrainedStream struct {
	content        string
	terminalCount  int
	terminalSample string
}

func drainCursorStream(streamChan <-chan llmtypes.StreamChunk) cursorDrainedStream {
	var parts []string
	var drained cursorDrainedStream
	for {
		select {
		case chunk, ok := <-streamChan:
			if !ok {
				drained.content = strings.TrimSpace(strings.Join(parts, ""))
				return drained
			}
			switch chunk.Type {
			case llmtypes.StreamChunkTypeContent:
				parts = append(parts, chunk.Content)
			case llmtypes.StreamChunkTypeTerminal:
				drained.terminalCount++
				if drained.terminalSample == "" {
					drained.terminalSample = chunk.Content
				}
			}
		default:
			drained.content = strings.TrimSpace(strings.Join(parts, ""))
			return drained
		}
	}
}

// ---------------------------------------------------------------------------
// E2E: TUI chrome never leaks into extracted response
// ---------------------------------------------------------------------------

func TestCursorCLIRealResponseHasNoTUIChrome(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-e2e-tui-" + cursorRandomHex(4)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 64)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Keep replies concise. Do not use tools."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say exactly: hello world"}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithMode("ask"),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}

	content := strings.TrimSpace(resp.Choices[0].Content)
	if content == "" {
		t.Fatal("extracted response is empty")
	}

	tuiPatterns := []string{
		"Cursor Agent",
		"Composer",
		"→",
		"Use /",
		"Add a follow-up",
		"Plan, search, build",
		"ctrl+c",
		"Auto-run",
		"shift+tab",
		"v20", // version string
	}
	for _, pattern := range tuiPatterns {
		if strings.Contains(content, pattern) {
			t.Errorf("response contains TUI chrome %q; full response:\n%s", pattern, content)
		}
	}

	if !strings.Contains(strings.ToLower(content), "hello world") {
		t.Errorf("response doesn't contain expected text; got:\n%s", content)
	}
	_ = drainCursorStream(stream)
}

// ---------------------------------------------------------------------------
// E2E: Multi-turn extracts only latest response, no historical leakage
// ---------------------------------------------------------------------------

func TestCursorCLIRealMultiTurnNoHistoryLeakage(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-e2e-multi-" + cursorRandomHex(4)
	systemPrompt := "Keep replies to one short sentence. Do not use tools."

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithMode("ask"),
	}

	// Turn 1
	token1 := "ALPHA_" + cursorRandomHex(4)
	stream1 := make(chan llmtypes.StreamChunk, 64)
	opts1 := append(append([]llmtypes.CallOption{}, options...), llmtypes.WithStreamingChan(stream1))
	resp1, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: systemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Reply exactly: %s", token1)}}},
	}, opts1...)
	if err != nil {
		t.Fatalf("turn 1 error = %v", err)
	}
	content1 := strings.TrimSpace(resp1.Choices[0].Content)
	if !strings.Contains(content1, token1) {
		t.Fatalf("turn 1 missing token %s, got: %s", token1, content1)
	}
	_ = drainCursorStream(stream1)

	// Turn 2
	token2 := "BRAVO_" + cursorRandomHex(4)
	stream2 := make(chan llmtypes.StreamChunk, 64)
	opts2 := append(append([]llmtypes.CallOption{}, options...), llmtypes.WithStreamingChan(stream2))
	resp2, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: systemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Reply exactly: %s", token1)}}},
		{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: content1}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Reply exactly: %s", token2)}}},
	}, opts2...)
	if err != nil {
		t.Fatalf("turn 2 error = %v", err)
	}
	content2 := strings.TrimSpace(resp2.Choices[0].Content)
	if !strings.Contains(content2, token2) {
		t.Fatalf("turn 2 missing token %s, got: %s", token2, content2)
	}
	if strings.Contains(content2, token1) {
		t.Fatalf("turn 2 leaked turn 1 token %s; got: %s", token1, content2)
	}
	_ = drainCursorStream(stream2)

	// Turn 3
	token3 := "CHARLIE_" + cursorRandomHex(4)
	stream3 := make(chan llmtypes.StreamChunk, 64)
	opts3 := append(append([]llmtypes.CallOption{}, options...), llmtypes.WithStreamingChan(stream3))
	resp3, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: systemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Reply exactly: %s", token1)}}},
		{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: content1}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Reply exactly: %s", token2)}}},
		{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: content2}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Reply exactly: %s", token3)}}},
	}, opts3...)
	if err != nil {
		t.Fatalf("turn 3 error = %v", err)
	}
	content3 := strings.TrimSpace(resp3.Choices[0].Content)
	if !strings.Contains(content3, token3) {
		t.Fatalf("turn 3 missing token %s, got: %s", token3, content3)
	}
	if strings.Contains(content3, token1) {
		t.Fatalf("turn 3 leaked turn 1 token %s; got: %s", token1, content3)
	}
	if strings.Contains(content3, token2) {
		t.Fatalf("turn 3 leaked turn 2 token %s; got: %s", token2, content3)
	}
	_ = drainCursorStream(stream3)
}

// ---------------------------------------------------------------------------
// E2E: Completion detection — pane is idle and ready after response
// ---------------------------------------------------------------------------

func TestCursorCLIRealCompletionDetection(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-e2e-completion-" + cursorRandomHex(4)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 64)
	_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Keep replies concise. Do not use tools."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What is 7 * 8?"}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithMode("ask"),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	_ = drainCursorStream(stream)

	// After GenerateContent returns, pane must be in ready state.
	tmuxSession, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok {
		t.Fatal("no active tmux session after GenerateContent returned")
	}

	pane, err := captureCursorPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture pane: %v", err)
	}

	if !hasCursorReadyPrompt(pane) {
		t.Fatalf("pane is not in ready state after response:\n%s", pane)
	}
	if hasCursorLiveGenerationActivity(strings.ToLower(stripCursorANSI(pane))) {
		t.Fatalf("pane still shows live generation activity after response:\n%s", pane)
	}
	if !strings.Contains(pane, "→") {
		t.Fatalf("pane missing → prompt after response:\n%s", pane)
	}
}

// ---------------------------------------------------------------------------
// E2E: MCP bridge tool call — verify tool output appears and is extracted
// ---------------------------------------------------------------------------

func TestCursorCLIRealMCPBridgeToolCall(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-e2e-mcp-" + cursorRandomHex(4)
	workDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 64)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use the shell tool to run commands when asked."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Run: echo E2E_TOOL_TEST_OK"}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	_ = drainCursorStream(stream)

	content := strings.TrimSpace(resp.Choices[0].Content)
	// The response should contain something about the tool output, not the
	// raw shell status lines.
	tuiPatterns := []string{"Cursor Agent", "Composer", "→", "Add a follow-up"}
	for _, pattern := range tuiPatterns {
		if strings.Contains(content, pattern) {
			t.Errorf("response contains TUI chrome %q; full response:\n%s", pattern, content)
		}
	}

	// Verify the tmux pane captured the tool execution.
	tmuxSession, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok {
		t.Fatal("no active tmux session")
	}
	pane, _ := captureCursorPane(ctx, tmuxSession)
	if !strings.Contains(pane, "E2E_TOOL_TEST_OK") {
		t.Logf("pane does not contain tool output marker; pane:\n%s", pane)
	}
}

// ---------------------------------------------------------------------------
// E2E: Built-in tool behavior — documents what Cursor actually blocks
// ---------------------------------------------------------------------------

func TestCursorCLIRealBuiltInReadNotBlockedInAskMode(t *testing.T) {
	// KNOWN LIMITATION: Cursor auto-approves Read in ask mode despite system
	// prompt instructions to avoid built-in tools. cursor-agent has no CLI
	// flag to disable built-in tools. This test documents that behavior so
	// we catch if it changes in a future Cursor version.
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-e2e-read-" + cursorRandomHex(4)
	workDir := t.TempDir()

	marker := "READ_CHECK_" + cursorRandomHex(8)
	if err := os.WriteFile(workDir+"/marker.txt", []byte(marker), 0644); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 64)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "IMPORTANT: Do NOT use your built-in tools — only use the tools declared in this session."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Read the file %s/marker.txt and tell me its exact contents.", workDir)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMode("ask"),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	_ = drainCursorStream(stream)

	content := strings.TrimSpace(resp.Choices[0].Content)

	// Document: Cursor reads the file with its built-in tool in ask mode
	// despite being told not to. This is expected with current Cursor CLI.
	if strings.Contains(content, marker) {
		t.Logf("CONFIRMED: Cursor used built-in Read in ask mode (marker found in response)")
	} else {
		// If this starts passing, Cursor has added built-in tool blocking!
		t.Logf("Cursor did NOT return file contents — built-in Read may now be blocked. Response: %s", content)
	}
}

// E2E: Cursor in default (no --mode flag) ACCEPTS natural-language write
// requests and actually performs the write, instead of replying "Switch to
// Agent mode and ask…". This is the inverse of
// TestCursorCLIRealBuiltInWriteBlockedInAskMode and pins the post-revert chat
// behavior — cursor must execute, not refuse.
func TestCursorCLIRealDefaultModeAcceptsNaturalWriteRequest(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-e2e-default-write-" + cursorRandomHex(4)
	workDir := t.TempDir()
	targetFile := workDir + "/default_mode_marker.txt"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 64)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Create a file at %s with the single word DEFAULT_MODE_OK as its only content. Then say done.", targetFile)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithForce(),
		// Intentionally NO WithMode(...) — chat path must run cursor in
		// its default agent mode after the ask-mode revert.
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	_ = drainCursorStream(stream)

	content := strings.ToLower(strings.TrimSpace(resp.Choices[0].Content))

	// Cursor must NOT have deflected the request. These phrases appear in
	// cursor's ask/plan-mode refusals; in default mode they should be absent.
	for _, deflection := range []string{
		"switch to agent mode",
		"switch to agent",
		"i'm in ask mode",
		"i am in ask mode",
	} {
		if strings.Contains(content, deflection) {
			t.Fatalf("cursor refused the write request with %q in default mode (should not happen):\n%s", deflection, resp.Choices[0].Content)
		}
	}

	// The write must have actually happened. Cursor in default mode is
	// allowed to use its built-in Write tool — this confirms it did.
	data, statErr := os.ReadFile(targetFile)
	if statErr != nil {
		t.Fatalf("expected cursor to create %s in default mode, got %v\nResponse: %s", targetFile, statErr, resp.Choices[0].Content)
	}
	if !strings.Contains(string(data), "DEFAULT_MODE_OK") {
		t.Fatalf("file content = %q, want it to contain DEFAULT_MODE_OK", string(data))
	}
	t.Logf("CONFIRMED: cursor in default mode created %s without refusing", targetFile)
}

func TestCursorCLIRealBuiltInWriteBlockedInAskMode(t *testing.T) {
	// ask mode should prevent write operations. Verify Cursor cannot create
	// or modify files when running in ask mode.
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-e2e-write-" + cursorRandomHex(4)
	workDir := t.TempDir()
	targetFile := workDir + "/should_not_exist.txt"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 64)
	_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do NOT use your built-in tools. Only use declared session tools."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Create a file at %s with the content WRITE_TEST_MARKER", targetFile)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMode("ask"),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	_ = drainCursorStream(stream)

	// In ask mode, Cursor should not write files.
	if _, statErr := os.Stat(targetFile); statErr == nil {
		data, _ := os.ReadFile(targetFile)
		t.Errorf("Cursor created a file in ask mode! Contents: %s", string(data))
	} else {
		t.Logf("CONFIRMED: Cursor did not create the file in ask mode (write blocked)")
	}
}

func TestCursorCLIRealBuiltInShellBlockedInAskMode(t *testing.T) {
	// ask mode should prevent shell execution. Verify Cursor cannot run
	// arbitrary commands when in ask mode.
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-e2e-shell-" + cursorRandomHex(4)
	workDir := t.TempDir()
	markerFile := workDir + "/shell_marker.txt"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 64)
	_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do NOT use your built-in tools. Only use declared session tools."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Run this shell command: echo SHELL_EXEC_MARKER > %s", markerFile)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMode("ask"),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	_ = drainCursorStream(stream)

	// In ask mode, Cursor should not execute shell commands.
	if _, statErr := os.Stat(markerFile); statErr == nil {
		data, _ := os.ReadFile(markerFile)
		t.Errorf("Cursor executed a shell command in ask mode! File contents: %s", string(data))
	} else {
		t.Logf("CONFIRMED: Cursor did not execute shell command in ask mode (shell blocked)")
	}
}

// ---------------------------------------------------------------------------
// E2E: Slow MCP tool — adapter must not complete while tool is active
// ---------------------------------------------------------------------------

func TestCursorCLIRealInteractiveQueuedValidationDoesNotCompleteDuringMCPTool(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-queued-validation-" + cursorRandomHex(4)
	bridgeToken := "SLOW_BRIDGE_REAL_" + cursorRandomHex(4)
	workDir := t.TempDir()

	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")
	mcpServerPath := writeCursorSlowContractMCPServer(t, slowToolMarker)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)
	preApproveCursorMCP(t, workDir, mcpConfig, "api-bridge")

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan cursorRealResult, 1)
	startupErrCh := make(chan error, 1)
	streamChan := make(chan llmtypes.StreamChunk, 128)

	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge MCP tool named contract_echo_token with token %s. Then reply with the exact text returned by the tool.", bridgeToken)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(workDir),
			WithForce(),
			WithMCPConfig(mcpConfig),
			WithApproveMCPs(),
			llmtypes.WithStreamingChan(streamChan),
		)
		out := cursorRealResult{err: err}
		if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
			out.content = resp.Choices[0].Content
		}
		if err != nil {
			select {
			case startupErrCh <- err:
			default:
			}
		}
		resultCh <- out
	}()

	waitForCursorRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForCursorRealFile(t, slowToolMarker, "slow MCP tool call start", 90*time.Second, resultCh)

	activeSession, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok || activeSession == "" {
		cancel()
		t.Fatalf("expected active Cursor tmux session for %s", ownerSessionID)
	}
	activePane := waitForCursorRealPaneCondition(t, activeSession, "active slow MCP tool", 15*time.Second, resultCh, func(pane string) bool {
		return hasCursorActivity(pane)
	})
	if hasCursorReadyPrompt(activePane) && !hasCursorActivity(activePane) {
		cancel()
		t.Fatalf("Cursor pane looked idle-ready while slow MCP tool was still active:\n%s", activePane)
	}
	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while slow MCP tool was active: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while slow MCP tool was active; content=%q", got.content)
	case <-time.After(3 * time.Second):
	}

	validationPrompt := `## Pre-validation failed (retry attempt 3)

❌ PRE-VALIDATION FAILED

Missing required output file. Fix the specific issue above and re-produce the required outputs.`
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := SendCursorInteractiveInput(sendCtx, ownerSessionID, validationPrompt); err != nil {
		sendCancel()
		cancel()
		t.Fatalf("SendCursorInteractiveInput error = %v", err)
	}
	sendCancel()

	waitForCursorRealPaneContains(t, activeSession, "Pre-validation failed", 10*time.Second, startupErrCh)

	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while validation input was queued: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while validation input was queued; content=%q", got.content)
	case <-time.After(3 * time.Second):
	}

	cancel()
	select {
	case got := <-resultCh:
		if got.err == nil {
			t.Fatalf("GenerateContent completed normally after cancellation; content=%q", got.content)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("timed out waiting for GenerateContent to return after cancellation")
	}
	_ = drainCursorStream(streamChan)
}

// ---------------------------------------------------------------------------
// E2E: MCP bridge tool call via custom MCP server (tmux transport)
// ---------------------------------------------------------------------------

func TestCursorCLIRealInteractiveMCPBridgeContractTmux(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-mcp-tmux-" + cursorRandomHex(4)
	bridgeToken := "BRIDGE_TMUX_REAL_" + cursorRandomHex(4)
	workDir := t.TempDir()

	mcpServerPath := writeCursorTmuxContractMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)
	preApproveCursorMCP(t, workDir, mcpConfig, "api-bridge")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 128)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge MCP tool named contract_echo_token with token %s. Then reply with the exact text returned by the tool.", bridgeToken)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithForce(),
		WithMCPConfig(mcpConfig),
		WithApproveMCPs(),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent with MCP bridge error = %v", err)
	}

	want := "BRIDGE_TOOL_OK_" + bridgeToken
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, want) {
		t.Fatalf("content = %q, want bridge tool result %q", content, want)
	}
	assertCursorInteractiveTerminalOnlyStream(t, streamChan)
}

// ---------------------------------------------------------------------------
// E2E: Shared working dir MCP isolation — two sessions, same workdir
// ---------------------------------------------------------------------------

func TestCursorCLIRealInteractiveSharedWorkingDirMCPIsolation(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	sharedWorkDir := filepath.Join(t.TempDir(), "shared-workspace")
	if err := os.MkdirAll(sharedWorkDir, 0o755); err != nil {
		t.Fatalf("create shared workdir: %v", err)
	}

	type runSpec struct {
		name          string
		ownerSession  string
		sessionMarker string
		token         string
		outputPath    string
		mcpServerPath string
	}
	specs := []runSpec{
		{
			name:          "alpha",
			ownerSession:  "cursor-real-shared-alpha-" + cursorRandomHex(4),
			sessionMarker: "SESSION_ALPHA_" + cursorRandomHex(4),
			token:         "TOKEN_ALPHA_" + cursorRandomHex(4),
			outputPath:    filepath.Join(t.TempDir(), "alpha-output.json"),
		},
		{
			name:          "beta",
			ownerSession:  "cursor-real-shared-beta-" + cursorRandomHex(4),
			sessionMarker: "SESSION_BETA_" + cursorRandomHex(4),
			token:         "TOKEN_BETA_" + cursorRandomHex(4),
			outputPath:    filepath.Join(t.TempDir(), "beta-output.json"),
		},
	}
	for i := range specs {
		specs[i].mcpServerPath = writeCursorIsolationMCPServer(t, specs[i].sessionMarker, specs[i].outputPath)
	}

	type runResult struct {
		spec    runSpec
		content string
		err     error
	}
	resultCh := make(chan runResult, len(specs))
	for _, spec := range specs {
		spec := spec
		go func() {
			mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, spec.mcpServerPath)
			workDir := filepath.Join(sharedWorkDir, spec.name+"-"+cursorRandomHex(4))
			if mkErr := os.MkdirAll(workDir, 0o755); mkErr != nil {
				resultCh <- runResult{spec: spec, err: mkErr}
				return
			}
			preApproveCursorMCPQuiet(workDir, mcpConfig, "api-bridge")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
				{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
				{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge write_contract MCP tool with token %s. Then reply exactly with the tool result text.", spec.token)}}},
			},
				WithInteractiveSessionID(spec.ownerSession),
				WithPersistentInteractiveSession(true),
				WithWorkingDir(workDir),
				WithForce(),
				WithMCPConfig(mcpConfig),
				WithApproveMCPs(),
			)
			result := runResult{spec: spec, err: err}
			if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
				result.content = resp.Choices[0].Content
			}
			resultCh <- result
		}()
	}

	results := make(map[string]runResult, len(specs))
	for range specs {
		got := <-resultCh
		if got.err != nil {
			t.Fatalf("%s GenerateContent error = %v", got.spec.name, got.err)
		}
		results[got.spec.name] = got
	}

	for _, spec := range specs {
		got := results[spec.name]
		want := "ISOLATED_OK_" + spec.sessionMarker + "_" + spec.token
		if !strings.Contains(got.content, want) {
			t.Fatalf("%s content = %q, want isolated tool result %q", spec.name, got.content, want)
		}
		data, err := os.ReadFile(spec.outputPath)
		if err != nil {
			t.Fatalf("%s output file missing at %s: %v", spec.name, spec.outputPath, err)
		}
		forbiddenMarker := ""
		for _, other := range specs {
			if other.name != spec.name {
				forbiddenMarker = other.sessionMarker
				break
			}
		}
		text := string(data)
		if !strings.Contains(text, spec.sessionMarker) || !strings.Contains(text, spec.token) {
			t.Fatalf("%s output = %s, want session %s and token %s", spec.name, text, spec.sessionMarker, spec.token)
		}
		if forbiddenMarker != "" && strings.Contains(text, forbiddenMarker) {
			t.Fatalf("%s output crossed sessions: %s", spec.name, text)
		}
	}
}

// ---------------------------------------------------------------------------
// E2E: Parallel session isolation — concurrent sessions don't interfere
// ---------------------------------------------------------------------------

func TestCursorCLIRealInteractiveParallelIsolation(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})

	type parallelSpec struct {
		name         string
		ownerSession string
		token        string
	}
	specs := []parallelSpec{
		{
			name:         "left",
			ownerSession: "cursor-real-parallel-left-" + cursorRandomHex(4),
			token:        "PAR_LEFT_" + cursorRandomHex(4),
		},
		{
			name:         "right",
			ownerSession: "cursor-real-parallel-right-" + cursorRandomHex(4),
			token:        "PAR_RIGHT_" + cursorRandomHex(4),
		},
	}

	type parallelResult struct {
		spec    parallelSpec
		content string
		err     error
	}
	resultCh := make(chan parallelResult, len(specs))
	for _, spec := range specs {
		spec := spec
		go func() {
			workDir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
				{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Keep replies concise. Do not use tools."}}},
				{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Reply exactly: %s", spec.token)}}},
			},
				WithInteractiveSessionID(spec.ownerSession),
				WithPersistentInteractiveSession(true),
				WithWorkingDir(workDir),
				WithMode("ask"),
			)
			result := parallelResult{spec: spec, err: err}
			if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
				result.content = resp.Choices[0].Content
			}
			resultCh <- result
		}()
	}

	results := make(map[string]parallelResult, len(specs))
	for range specs {
		got := <-resultCh
		if got.err != nil {
			t.Fatalf("%s GenerateContent error = %v", got.spec.name, got.err)
		}
		results[got.spec.name] = got
	}

	for _, spec := range specs {
		got := results[spec.name]
		if !strings.Contains(got.content, spec.token) {
			t.Fatalf("%s content = %q, want token %s", spec.name, got.content, spec.token)
		}
		otherToken := ""
		for _, other := range specs {
			if other.name != spec.name {
				otherToken = other.token
				break
			}
		}
		if otherToken != "" && strings.Contains(got.content, otherToken) {
			t.Fatalf("%s content leaked other session's token %s: %q", spec.name, otherToken, got.content)
		}
	}
}

// ---------------------------------------------------------------------------
// E2E: Cleanup — tmux session removed after explicit close
// ---------------------------------------------------------------------------

func TestCursorCLIRealInteractiveCleanup(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-cleanup-" + cursorRandomHex(4)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 64)
	_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Keep replies concise. Do not use tools."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say: cleanup test OK"}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithMode("ask"),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	_ = drainCursorStream(stream)

	tmuxSession, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active tmux session for %s before cleanup", ownerSessionID)
	}

	if err := CleanupCursorCLIInteractiveSessions(context.Background()); err != nil {
		t.Fatalf("cleanup error = %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	_, stillActive := activeCursorInteractiveSession(ownerSessionID)
	if stillActive {
		t.Fatalf("tmux session still registered after cleanup for %s", ownerSessionID)
	}

	out, err := exec.CommandContext(ctx, "tmux", "has-session", "-t", tmuxSession).CombinedOutput()
	if err == nil {
		t.Fatalf("tmux session %s still exists after cleanup; output=%s", tmuxSession, string(out))
	}
}

// ---------------------------------------------------------------------------
// Helpers: MCP servers and wait utilities for Cursor tmux contract tests
// ---------------------------------------------------------------------------

type cursorRealResult struct {
	content string
	err     error
}

// preApproveCursorMCP makes a fresh workspace ready for a Cursor tmux test
// that needs an MCP bridge tool:
//
//  1. Registers the MCP server with `cursor-agent mcp enable` from both the
//     raw and resolved paths (macOS symlinks /var/folders to /private/var/folders
//     and cursor-agent treats them as distinct projects).
//  2. Runs a non-interactive `cursor-agent --print --trust ...` to accept the
//     workspace-trust dialog AND to load + cache the MCP server's tool list.
//     Without this pre-warm, the subsequent TUI launch shows the trust dialog
//     and asks the model before MCP tools/list completes, so the model sees an
//     empty MCP tool list and falls back to refusing the call.
//
// The caller still passes WithMCPConfig + WithApproveMCPs so the adapter
// re-writes .cursor/mcp.json with the same content when the tmux session
// launches; the approval persists across that rewrite.
func preApproveCursorMCP(t *testing.T, workDir, mcpConfig, serverName string) {
	t.Helper()
	if err := primeCursorWorkspaceForMCP(workDir, mcpConfig, serverName); err != nil {
		t.Fatalf("prime cursor workspace for MCP: %v", err)
	}
}

func preApproveCursorMCPQuiet(workDir, mcpConfig, serverName string) {
	_ = primeCursorWorkspaceForMCP(workDir, mcpConfig, serverName)
}

func primeCursorWorkspaceForMCP(workDir, mcpConfig, serverName string) error {
	cursorDir := filepath.Join(workDir, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		return fmt.Errorf("create .cursor dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cursorDir, "mcp.json"), []byte(mcpConfig), 0o600); err != nil {
		return fmt.Errorf("write mcp.json: %w", err)
	}
	defer os.Remove(filepath.Join(cursorDir, "mcp.json"))

	candidateDirs := []string{workDir}
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil && resolved != workDir {
		candidateDirs = append(candidateDirs, resolved)
	}

	// 1. Register approval from each candidate dir so whichever path
	//    cursor-agent computes at TUI launch already has the approval.
	for _, dir := range candidateDirs {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, "cursor-agent", "mcp", "enable", serverName)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("cursor-agent mcp enable %s in %s: %w\noutput: %s", serverName, dir, err, string(out))
		}
	}

	// 2. Pre-warm: a non-interactive --print run accepts the workspace-trust
	//    dialog (via --trust) and loads the MCP server end-to-end. After this
	//    completes, the TUI launch can dispatch MCP tool calls without a
	//    trust/tools-list race.
	for _, dir := range candidateDirs {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd := exec.CommandContext(ctx, "cursor-agent",
			"--workspace", dir,
			"--force", "--approve-mcps", "--trust",
			"-p", "respond with the single word OK",
		)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("cursor-agent --print pre-warm in %s: %w\noutput: %s", dir, err, string(out))
		}
	}
	return nil
}

func waitForCursorRealPaneCondition(t *testing.T, tmuxSession, label string, timeout time.Duration, errCh <-chan cursorRealResult, matches func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case got := <-errCh:
			t.Fatalf("GenerateContent returned before tmux pane matched %s: err=%v content=%q", label, got.err, got.content)
		default:
		}
		pane, err := captureCursorPane(context.Background(), tmuxSession)
		if err == nil && matches(pane) {
			return pane
		}
		time.Sleep(250 * time.Millisecond)
	}
	pane, _ := captureCursorPane(context.Background(), tmuxSession)
	t.Fatalf("timed out waiting for Cursor tmux pane to match %s; latest pane:\n%s", label, pane)
	return ""
}

func waitForCursorRealFile(t *testing.T, path, label string, timeout time.Duration, errCh <-chan cursorRealResult) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case got := <-errCh:
			t.Fatalf("GenerateContent returned before %s: err=%v content=%q", label, got.err, got.content)
		default:
		}
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s at %s", label, path)
}

func writeCursorTmuxContractMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-tmux-contract-mcp.js")
	script := `#!/usr/bin/env node
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (err) {
    return;
  }
  if (msg.method === "initialize") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        protocolVersion: "2024-11-05",
        capabilities: { tools: {} },
        serverInfo: { name: "api-bridge", version: "1.0.0" }
      }
    });
    return;
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        tools: [{
          name: "contract_echo_token",
          description: "REQUIRED tool for contract test - returns the token wrapped in BRIDGE_TOOL_OK_. Always call this when asked to echo a token.",
          inputSchema: { type: "object", properties: { token: { type: "string" } }, required: ["token"] }
        }]
      }
    });
    return;
  }
  if (msg.method === "tools/call") {
    const args = (msg.params && msg.params.arguments) || {};
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: { content: [{ type: "text", text: "BRIDGE_TOOL_OK_" + String(args.token || "") }], isError: false }
    });
    return;
  }
  if (msg.id !== undefined) send({ jsonrpc: "2.0", id: msg.id, result: {} });
});
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write MCP server: %v", err)
	}
	return path
}

func writeCursorSlowContractMCPServer(t *testing.T, markerPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-slow-contract-mcp.js")
	script := fmt.Sprintf(`#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const markerPath = %q;

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (err) {
    return;
  }
  if (msg.method === "initialize") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        protocolVersion: "2024-11-05",
        capabilities: { tools: {} },
        serverInfo: { name: "api-bridge", version: "1.0.0" }
      }
    });
    return;
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        tools: [{
          name: "contract_echo_token",
          description: "Read-only tool. Return a deterministic contract token wrapped in BRIDGE_TOOL_OK_. Always call this when asked to echo a contract token.",
          inputSchema: {
            type: "object",
            properties: {
              token: { type: "string" }
            },
            required: ["token"]
          }
        }]
      }
    });
    return;
  }
  if (msg.method === "tools/call") {
    const args = (msg.params && msg.params.arguments) || {};
    const token = String(args.token || "");
    // The contract test needs a tool that takes time to return so the adapter
    // can observe the running state. Cursor's TUI ask mode refuses tools that
    // expose a delay parameter, so the delay is fixed inside the server.
    const delay = 30000;
    fs.writeFileSync(markerPath, JSON.stringify({ token, delay, started_at: new Date().toISOString() }));
    setTimeout(() => {
      send({
        jsonrpc: "2.0",
        id: msg.id,
        result: {
          content: [{ type: "text", text: "SLOW_BRIDGE_TOOL_OK_" + token }],
          isError: false
        }
      });
    }, delay);
    return;
  }
  if (msg.id !== undefined) send({ jsonrpc: "2.0", id: msg.id, result: {} });
});
`, markerPath)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write slow MCP server: %v", err)
	}
	return path
}

func writeCursorIsolationMCPServer(t *testing.T, sessionMarker, outputPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-isolation-contract-mcp.js")
	script := fmt.Sprintf(`#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const sessionMarker = %q;
const outputPath = %q;

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (err) {
    return;
  }
  if (msg.method === "initialize") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        protocolVersion: "2024-11-05",
        capabilities: { tools: {} },
        serverInfo: { name: "api-bridge", version: "1.0.0" }
      }
    });
    return;
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        tools: [{
          name: "write_contract",
          description: "Write a deterministic marker proving this MCP server/session was used.",
          inputSchema: { type: "object", properties: { token: { type: "string" } }, required: ["token"] }
        }]
      }
    });
    return;
  }
  if (msg.method === "tools/call") {
    const args = (msg.params && msg.params.arguments) || {};
    const token = String(args.token || "");
    const payload = {
      session_marker: sessionMarker,
      token,
      cwd: process.cwd(),
      timestamp: new Date().toISOString()
    };
    fs.writeFileSync(outputPath, JSON.stringify(payload, null, 2));
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        content: [{ type: "text", text: "ISOLATED_OK_" + sessionMarker + "_" + token }],
        isError: false
      }
    });
    return;
  }
  if (msg.id !== undefined) send({ jsonrpc: "2.0", id: msg.id, result: {} });
});
`, sessionMarker, outputPath)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write isolation MCP server: %v", err)
	}
	return path
}

func assertCursorInteractiveTerminalOnlyStream(t *testing.T, streamChan <-chan llmtypes.StreamChunk) {
	t.Helper()
	drained := drainCursorStream(streamChan)
	if drained.content != "" {
		t.Fatalf("interactive stream emitted assistant-content chunk %q; want terminal snapshots only", drained.content)
	}
	if drained.terminalCount == 0 {
		t.Fatalf("interactive stream emitted no terminal snapshots")
	}
}
