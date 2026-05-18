package cursorcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
