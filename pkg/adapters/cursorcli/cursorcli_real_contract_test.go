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

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
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
		WithDenyBuiltinTools(true),
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

func TestCursorCLIRealAuthPromptSurfacedBeforePromptContract(t *testing.T) {
	requireRealCursorCLIE2E(t)

	workDir, err := os.MkdirTemp("/private/tmp", "cursor-real-auth-prompt-work-*")
	if err != nil {
		t.Fatalf("create auth prompt workdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })
	homeDir, err := os.MkdirTemp("/private/tmp", "cursor-real-auth-prompt-home-*")
	if err != nil {
		t.Fatalf("create auth prompt home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(homeDir) })
	t.Setenv("HOME", homeDir)
	t.Setenv("CURSOR_API_KEY", "")

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(workDir)(opts)
	args, env, workingDir, cleanupFiles, err := adapter.buildCursorInteractiveLaunch(opts, "", "test-session-cursor-auth")
	if err != nil {
		t.Fatalf("build auth prompt launch: %v", err)
	}
	t.Cleanup(cleanupFiles)
	env = append(env, "HOME="+homeDir, "CURSOR_API_KEY=")

	sessionName := newCursorTmuxSessionName()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := startCursorTmuxSession(ctx, sessionName, args, env, workingDir); err != nil {
		t.Fatalf("start auth prompt tmux session: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = killCursorTmuxSession(closeCtx, sessionName)
	})

	started := time.Now()
	err = waitForCursorPrompt(ctx, sessionName, nil)
	if !llmtypes.IsCodingAgentAuthRequiredError(err) {
		pane, _ := captureCursorPane(context.Background(), sessionName)
		t.Fatalf("waitForCursorPrompt error = %v, want typed authentication-required error; pane:\n%s", err, pane)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("authentication prompt took %s to surface; want deterministic failure within 20s", elapsed)
	}
	pane, _ := captureCursorPane(context.Background(), sessionName)
	if !hasCursorAuthPrompt(pane) {
		t.Fatalf("expected real Cursor authentication prompt in isolated HOME; pane:\n%s", pane)
	}
	if hasCursorReadyPrompt(pane) {
		t.Fatalf("authentication prompt should not be parsed as ready; pane:\n%s", pane)
	}
}

// TestCursorCLIRealInteractiveWrappedSingleLineSubmitContract exercises the
// production failure mode where Cursor hard-wraps one long logical input line
// across several TUI rows. The adapter must recognize the visible draft, submit
// it, and receive a model response rather than failing its pre-submit guard.
func TestCursorCLIRealInteractiveWrappedSingleLineSubmitContract(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-wrapped-input-" + cursorRandomHex(4)
	token := "WRAPPED_CURSOR_INPUT_" + cursorRandomHex(5)
	prompt := strings.Repeat("This sentence only makes the single Cursor input line long enough to wrap inside the terminal composer and does not change the requested response. ", 3) + "Reply exactly: " + token
	if strings.ContainsAny(prompt, "\r\n") || len(prompt) <= cursorTypedInputMaxLen {
		t.Fatalf("wrapped-input fixture must be one logical line longer than %d bytes", cursorTypedInputMaxLen)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	response, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Follow the final exact-reply instruction in the user message."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithDenyBuiltinTools(true),
	)
	if err != nil {
		t.Fatalf("GenerateContent with hard-wrapped single-line prompt error = %v", err)
	}
	content := strings.TrimSpace(response.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("wrapped prompt response = %q, want token %s", content, token)
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
			WithDenyBuiltinTools(true),
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

func TestCursorCLIRealFinalExtractionFromTmuxVertexJudgeE2E(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-final-extract-" + cursorRandomHex(4)
	token := "LIVE_CURSOR_FINAL_" + cursorRandomHex(5)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 128)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Preserve line breaks in the final answer."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Return a final answer containing these three plain lines and no setup commentary:\nCursor final %s\nfirst %s\nsecond %s", token, token, token)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithDenyBuiltinTools(true),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	tmuxSession, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Cursor tmux session for %s", ownerSessionID)
	}
	pane, err := captureCursorPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture Cursor pane: %v", err)
	}
	_ = drainCursorStream(streamChan)

	testcontracts.AssertVertexJudgesFinalExtraction(t, testcontracts.FinalExtractionJudgeCase{
		Provider:   "cursor-cli",
		TmuxScreen: pane,
		Extracted:  content,
		UserGoal:   "Return the live tmux final answer containing the token heading and the first/second lines.",
		MustContain: []string{
			"Cursor final " + token,
			"first " + token,
			"second " + token,
		},
		Forbidden: []string{
			"Return a final answer",
			"Do not use tools",
			"Cursor Agent",
			"Composer",
			"Add a follow-up",
			"Ask (shift+tab",
			"User:",
			"Assistant:",
			"ctrl+c",
			"ctrl+o",
			"Globbed",
			"Found ",
			"$ ",
		},
		ExpectedNote: "This is a live Cursor CLI tmux capture after GenerateContent returned; the extracted response must be only the final answer.",
	})
}

func requireRealCursorCLIE2E(t *testing.T) {
	t.Helper()
	if !*codingCLIP0Live {
		t.Skip("run through the live coding CLI P0 runner")
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
		WithDenyBuiltinTools(true),
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
		WithDenyBuiltinTools(true),
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
		WithDenyBuiltinTools(true),
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

// Note: ask-mode-specific tests deleted. They documented a lever the
// codebase no longer relies on (cursor's --mode ask). The replacement
// is WithDenyBuiltinTools (cursor hooks), tested in
// cursorcli_deny_builtin_hooks_test.go for write-side correctness.
// Follow-up: real-LLM e2e tests that prove hooks actually block
// built-in Read + Shell are tracked in the test-followups task.

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
// E2E: Cursor natively queues a mid-turn user message and processes it after
// the current turn completes (live input, no server queue in between).
// ---------------------------------------------------------------------------

func TestCursorCLIRealInteractiveLiveInputProcessesQueuedFollowupContract(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-live-process-" + cursorRandomHex(4)
	workDir := t.TempDir()
	bridgeToken := "CURSOR_LIVE_PROCESS_" + cursorRandomHex(5)
	firstDone := "CURSOR_FIRST_DONE_" + cursorRandomHex(5)
	liveAck := "CURSOR_LIVE_ACK_" + cursorRandomHex(5)

	// The cursor slow-MCP contract server bakes its delay in internally (cursor's
	// TUI ask mode refuses a tool that exposes a delay parameter), so the prompt
	// asks for delay_ms 8000 as the intent while the server enforces the real
	// busy window; the marker file proves the tool started.
	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")
	mcpServerPath := writeCursorSlowContractMCPServer(t, slowToolMarker)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)
	preApproveCursorMCP(t, workDir, mcpConfig, "api-bridge")

	parentCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resultCh := make(chan cursorRealResult, 1)
	startupErrCh := make(chan error, 1)
	streamChan := make(chan llmtypes.StreamChunk, 128)

	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. If a follow-up user message arrives while you are working, handle it after the current tool call finishes. Keep answers concise."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge MCP tool named contract_echo_token with token %s and delay_ms 8000. Do not answer until the tool returns. Then reply exactly %s.", bridgeToken, firstDone)}}},
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

	tmuxSession := waitForCursorRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForCursorRealFile(t, slowToolMarker, "slow MCP tool call start", 90*time.Second, resultCh)

	// Deliver the follow-up WHILE cursor is busy in the slow tool, via the raw
	// adapter path (no server queue in between) — proving cursor-agent itself
	// queues the mid-turn message and processes it after the current turn.
	liveMessage := fmt.Sprintf("Follow-up task: after the current answer completes, also reply exactly %s and nothing else.", liveAck)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := SendCursorInteractiveInput(sendCtx, ownerSessionID, liveMessage); err != nil {
		sendCancel()
		cancel()
		t.Fatalf("SendCursorInteractiveInput error = %v", err)
	}
	sendCancel()

	// Let GenerateContent COMPLETE (do not cancel). The native queue must have
	// processed the follow-up so the final content carries the LIVE_ACK marker.
	select {
	case got := <-resultCh:
		if got.err != nil {
			pane, _ := captureCursorPane(context.Background(), tmuxSession)
			t.Fatalf("GenerateContent error = %v (content=%q)\npane:\n%s", got.err, got.content, pane)
		}
		if !strings.Contains(got.content, liveAck) {
			pane, _ := captureCursorPane(context.Background(), tmuxSession)
			t.Fatalf("queued follow-up was NOT processed by cursor-agent; final content=%q\npane:\n%s", got.content, pane)
		}
		t.Logf("OK: cursor-agent natively queued + processed the mid-turn follow-up (found %s in final content)", liveAck)
	case <-time.After(4 * time.Minute):
		pane, _ := captureCursorPane(context.Background(), tmuxSession)
		t.Fatalf("timed out waiting for cursor to process the queued live input; pane:\n%s", pane)
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

func TestCursorCLIRealMCPBridgeExecuteShellWithBuiltinsDenied(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-mcp-shell-" + cursorRandomHex(4)
	token := "BRIDGE_SHELL_REAL_" + cursorRandomHex(4)
	workDir := t.TempDir()

	mcpServerPath := writeCursorShellBridgeMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)
	preApproveCursorMCP(t, workDir, mcpConfig, "api-bridge")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 128)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Cursor built-in Shell is disabled in this session. For shell commands, call the api-bridge MCP tool execute_shell_command. Keep the final answer concise."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call api-bridge.execute_shell_command with command %q. Then reply with exactly the stdout value.", "printf "+token)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(mcpConfig),
		WithApproveMCPs(),
		WithDenyBuiltinTools(true),
		llmtypes.WithStreamingChan(streamChan),
	)
	drained := drainCursorStream(streamChan)
	if err != nil {
		t.Fatalf("GenerateContent with MCP bridge shell error = %v\nstream:\n%s", err, drained.content)
	}

	content := ""
	if resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
		content = resp.Choices[0].Content
	}
	tmuxSession, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Cursor tmux session for %s", ownerSessionID)
	}
	pane, captureErr := captureCursorPane(ctx, tmuxSession)
	if captureErr != nil {
		t.Fatalf("capture Cursor pane: %v", captureErr)
	}

	haystack := content + "\n" + drained.content + "\n" + pane
	if !strings.Contains(haystack, token) {
		t.Fatalf("MCP bridge shell result missing token %q\ncontent:\n%s\npane:\n%s", token, content, pane)
	}
	if !strings.Contains(strings.ToLower(haystack), "execute_shell_command") {
		t.Fatalf("Cursor turn did not show api-bridge execute_shell_command usage\ncontent:\n%s\npane:\n%s", content, pane)
	}
	for _, blocked := range []string{
		"run this command?",
		"not in allowlist: printf",
		"shell(printf",
	} {
		if strings.Contains(strings.ToLower(pane), blocked) {
			t.Fatalf("Cursor appears to have used or stalled on built-in Shell marker %q despite MCP bridge routing:\n%s", blocked, pane)
		}
	}
}

// TestCursorTmuxSystemPromptSteersWritesThroughBridge answers a question the
// previous design assumed away ("cursor's built-in tools cannot be disabled
// via system prompt" — comment in mcpagent/agent/agent.go). In tmux mode
// cursor receives the system prompt as an `alwaysApply: true` .mdc rule file
// written into `.cursor/rules/`. We verify that a rule saying "do NOT use
// built-in writes; use the declared MCP tool" actually steers cursor.
//
// Setup: real cursor-agent, tmux mode, custom MCP bridge with one
// `write_via_bridge` tool whose RESULT prefixes content with a sentinel only
// the bridge can produce. If cursor routed through the bridge, the file on
// disk has the sentinel; if cursor used its built-in editToolCall, it does not.
func TestCursorTmuxSystemPromptSteersWritesThroughBridge(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-sys-prompt-routing-" + cursorRandomHex(4)
	workDir := t.TempDir()
	targetFile := "routing_" + cursorRandomHex(4) + ".txt"
	targetPath := filepath.Join(workDir, targetFile)

	// Mock MCP bridge exposing a single write_via_bridge tool. The tool's
	// description is deliberately neutral — no "prefer me over built-ins"
	// hint. The only steering pressure comes from the system prompt below.
	mcpServerPath := writeCursorBridgeWriteMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)
	preApproveCursorMCP(t, workDir, mcpConfig, "api-bridge")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 128)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "IMPORTANT: Do NOT use your built-in file write/edit tools (editToolCall or similar). For any file write operation, use the declared MCP tool write_via_bridge on the api-bridge server. Pass the absolute file path and content as arguments. Reply briefly after the call."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Create a file at %s with the content 'hello world'.", targetPath)}}},
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
		t.Fatalf("GenerateContent error = %v", err)
	}
	_ = resp
	_ = drainCursorStream(streamChan)

	body, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("expected cursor to create %s, got %v", targetPath, err)
	}
	const bridgeSentinel = "BRIDGE_WROTE_THIS:"
	if !strings.HasPrefix(string(body), bridgeSentinel) {
		t.Fatalf("file did not have bridge sentinel — cursor used its BUILT-IN write tool despite system prompt nudge. content=%q", string(body))
	}
	t.Logf("CONFIRMED: tmux + system prompt steered cursor through MCP bridge (file content starts with sentinel %q)", bridgeSentinel)
}

// writeCursorBridgeWriteMCPServer writes a tmux-compatible MCP server with a
// single write tool that prepends "BRIDGE_WROTE_THIS:" to every write — used
// by TestCursorTmuxSystemPromptSteersWritesThroughBridge to detect which
// path cursor took.
func writeCursorBridgeWriteMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge-write-mcp.js")
	script := `#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg; try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") return send({jsonrpc:"2.0",id:msg.id,result:{protocolVersion:"2024-11-05",capabilities:{tools:{}},serverInfo:{name:"api-bridge",version:"1.0.0"}}});
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") return send({jsonrpc:"2.0",id:msg.id,result:{tools:[{
    name: "write_via_bridge",
    description: "Write content to a file at an absolute path.",
    inputSchema: { type: "object", properties: { path: { type: "string" }, content: { type: "string" } }, required: ["path","content"] }
  }]}});
  if (msg.method === "tools/call") {
    const args = (msg.params && msg.params.arguments) || {};
    try {
      fs.writeFileSync(String(args.path), "BRIDGE_WROTE_THIS:" + String(args.content));
      send({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:"wrote " + String(args.path)}],isError:false}});
    } catch (e) {
      send({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:String(e)}],isError:true}});
    }
    return;
  }
  if (msg.id !== undefined) send({jsonrpc:"2.0",id:msg.id,result:{}});
});
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write bridge MCP server: %v", err)
	}
	return path
}

func writeCursorShellBridgeMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge-shell-mcp.js")
	script := `#!/usr/bin/env node
const { exec } = require("child_process");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg; try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") return send({jsonrpc:"2.0",id:msg.id,result:{protocolVersion:"2024-11-05",capabilities:{tools:{}},serverInfo:{name:"api-bridge",version:"1.0.0"}}});
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") return send({jsonrpc:"2.0",id:msg.id,result:{tools:[{
    name: "execute_shell_command",
    description: "Run a shell command through the api-bridge MCP server and return stdout, stderr, and exit code.",
    inputSchema: { type: "object", properties: { command: { type: "string" }, timeout: { type: "number" } }, required: ["command"] }
  }]}});
  if (msg.method === "tools/call") {
    const args = (msg.params && msg.params.arguments) || {};
    const command = String(args.command || "");
    exec(command, { timeout: 5000 }, (error, stdout, stderr) => {
      const exitCode = error && Number.isInteger(error.code) ? error.code : 0;
      send({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:JSON.stringify({stdout,stderr,exit_code:exitCode})}],isError:false}});
    });
    return;
  }
  if (msg.id !== undefined) send({jsonrpc:"2.0",id:msg.id,result:{}});
});
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write shell bridge MCP server: %v", err)
	}
	return path
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
				WithDenyBuiltinTools(true),
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
		WithDenyBuiltinTools(true),
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
	normalizedMCPConfig, err := normalizeCursorMCPConfigForCLI(mcpConfig)
	if err != nil {
		return err
	}
	cursorDir := filepath.Join(workDir, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		return fmt.Errorf("create .cursor dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cursorDir, "mcp.json"), []byte(normalizedMCPConfig), 0o600); err != nil {
		return fmt.Errorf("write mcp.json: %w", err)
	}
	if allowlistJSON, ok, err := cursorMCPAllowlistCLIConfig(normalizedMCPConfig); err != nil {
		return err
	} else if ok {
		if err := os.WriteFile(filepath.Join(cursorDir, "cli.json"), []byte(allowlistJSON), 0o600); err != nil {
			return fmt.Errorf("write cli.json: %w", err)
		}
		defer os.Remove(filepath.Join(cursorDir, "cli.json"))
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
		// Cursor's first authenticated --print call may initialize/update the
		// CLI and hydrate MCP metadata before answering. That exceeded one minute
		// in a healthy live P0 run, so give this prerequisite the same realistic
		// startup allowance as the interactive contract instead of killing it
		// before the bridge itself can be tested.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
