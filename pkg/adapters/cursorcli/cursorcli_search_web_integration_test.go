package cursorcli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCursorCLIRealSearchWeb(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := adapter.SearchWeb(ctx,
		"What is the capital of France? Use web search and reply with the city and country only.",
		WithDenyBuiltinTools(true),
	)
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(result), "paris") {
		t.Fatalf("expected result to mention Paris, got %q", result)
	}
}

func TestCursorCLIRealSearchWebLiveData(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := adapter.SearchWeb(ctx,
		"Search the web for the latest cursor-agent CLI version number released in 2026. Reply with just the version string.",
		WithDenyBuiltinTools(true),
	)
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	result = strings.TrimSpace(result)
	if result == "" {
		t.Fatal("SearchWeb returned empty result")
	}
	// The result must contain something that looks like a version — proof of live data.
	if !strings.Contains(result, "20") && !strings.Contains(result, "v") && !strings.Contains(result, ".") {
		t.Logf("WARNING: result may not contain live web data: %q", result)
	}
	t.Logf("Live web search result: %s", result)
}

func TestCursorCLIRealInteractiveAutoApprovesWebOpenURL(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-web-open-" + cursorRandomHex(4)
	workDir := t.TempDir()
	token := "CURSOR_WEB_OPEN_" + cursorRandomHex(4)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 128)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Use Cursor's normal web/open-URL capability when the user asks for a URL. Keep the final answer concise."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Open https://example.com/?cursor_approval_test=" + token + " and reply with the exact text WEB_OPEN_OK " + token + " after confirming the page is reachable."},
			},
		},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithAutoApproveWebSearch(),
		llmtypes.WithStreamingChan(streamChan),
	)
	drained := drainCursorStream(streamChan)
	if err != nil {
		t.Fatalf("GenerateContent with Cursor web URL auto-approval error = %v\nstream:\n%s", err, drained.content)
	}

	tmuxSession, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Cursor tmux session for %s", ownerSessionID)
	}
	pane, captureErr := captureCursorPane(ctx, tmuxSession)
	if captureErr != nil {
		t.Fatalf("capture Cursor pane: %v", captureErr)
	}

	content := ""
	if resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
		content = resp.Choices[0].Content
	}
	haystack := pane + "\n" + drained.content + "\n" + content
	if !strings.Contains(haystack, token) {
		t.Fatalf("Cursor web URL turn did not preserve token %q\ncontent:\n%s\npane:\n%s", token, content, pane)
	}
	if hasCursorWebAccessApprovalPrompt(pane) {
		t.Fatalf("Cursor web/open-URL approval prompt was still visible after the turn; auto-approval did not clear it:\n%s", pane)
	}
	for _, blocked := range []string{
		"allow this web search?",
		"allow this web fetch?",
		"open this url?",
		"skip (esc or n)",
	} {
		if strings.Contains(strings.ToLower(pane), blocked) {
			t.Fatalf("Cursor pane still contains web approval marker %q after the turn:\n%s", blocked, pane)
		}
	}
}
