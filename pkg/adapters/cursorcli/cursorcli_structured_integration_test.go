package cursorcli

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func requireCursorCLIStructuredE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_CURSOR_CLI_STREAM_JSON_E2E") == "" && os.Getenv("RUN_CURSOR_CLI_REAL_E2E") == "" {
		t.Skip("set RUN_CURSOR_CLI_STREAM_JSON_E2E=1 to run Cursor CLI structured JSON e2e tests")
	}
	if _, err := exec.LookPath("cursor-agent"); err != nil {
		t.Fatalf("cursor-agent not found in PATH: %v", err)
	}
}

func TestCursorCLIStructuredBasicRun(t *testing.T) {
	requireCursorCLIStructuredE2E(t)

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What is the capital of Japan? Reply with just the city name."},
			},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("GenerateContent() returned no choices")
	}
	content := strings.ToLower(strings.TrimSpace(resp.Choices[0].Content))
	if !strings.Contains(content, "tokyo") {
		t.Fatalf("expected response to contain tokyo, got %q", content)
	}
}

func TestCursorCLIStructuredTokenUsage(t *testing.T) {
	requireCursorCLIStructuredE2E(t)

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Say hello."},
			},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if resp.Usage.InputTokens == 0 {
		t.Fatal("expected non-zero InputTokens")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Fatal("expected non-zero OutputTokens")
	}
	t.Logf("Usage: input=%d output=%d total=%d", resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens)
}

func TestCursorCLIStructuredSystemPrompt(t *testing.T) {
	requireCursorCLIStructuredE2E(t)

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	canary := "CANARY_" + cursorRandomHex(4)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Always include the exact string " + canary + " in your response."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What is 2+2?"},
			},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	content := resp.Choices[0].Content
	if !strings.Contains(content, canary) {
		t.Fatalf("expected system prompt canary %q in response, got %q", canary, content)
	}
}

func TestCursorCLIStructuredStreaming(t *testing.T) {
	requireCursorCLIStructuredE2E(t)

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 256)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Write a haiku about Go programming."},
			},
		},
	},
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	var contentChunks int
	for chunk := range stream {
		if chunk.Type == llmtypes.StreamChunkTypeContent {
			contentChunks++
		}
	}
	if contentChunks == 0 {
		t.Fatal("expected streaming content chunks")
	}
	t.Logf("received %d content chunks, final: %q", contentChunks, resp.Choices[0].Content)
}

func TestCursorCLIStructuredToolUse(t *testing.T) {
	requireCursorCLIStructuredE2E(t)

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 256)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "List files in the current directory using the shell tool. Then say done."},
			},
		},
	},
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("no choices returned")
	}

	var hasToolStart, hasToolEnd bool
	for chunk := range stream {
		switch chunk.Type {
		case llmtypes.StreamChunkTypeToolCallStart:
			hasToolStart = true
		case llmtypes.StreamChunkTypeToolCallEnd:
			hasToolEnd = true
		}
	}
	if !hasToolStart || !hasToolEnd {
		t.Logf("tool_call_start=%v tool_call_end=%v (tool may not have been used)", hasToolStart, hasToolEnd)
	}
	t.Logf("response: %q", resp.Choices[0].Content)
}

func TestCursorCLIStructuredSessionMetadata(t *testing.T) {
	requireCursorCLIStructuredE2E(t)

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Say hi."},
			},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	gen := resp.Choices[0].GenerationInfo
	if gen == nil || gen.Additional == nil {
		t.Fatal("expected GenerationInfo with Additional metadata")
	}
	sessionID, ok := gen.Additional["cursor_session_id"].(string)
	if !ok || sessionID == "" {
		t.Fatal("expected cursor_session_id in generation metadata")
	}
	mode, _ := gen.Additional["cursor_mode"].(string)
	if mode != "structured" {
		t.Fatalf("expected cursor_mode=structured, got %q", mode)
	}
	t.Logf("session_id=%s mode=%s", sessionID, mode)
}

func TestCursorCLIStructuredImagePath(t *testing.T) {
	requireCursorCLIStructuredE2E(t)

	workspaceDir := t.TempDir()
	imagePath := filepath.Join(workspaceDir, "red.png")
	writeSolidCursorTestPNG(t, imagePath, color.RGBA{R: 255, A: 255})

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	prompt := fmt.Sprintf("Inspect the local image file at this absolute path:\n%s\n\nQuestion: What is the dominant color? Reply with one lowercase English color word.", imagePath)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: prompt},
			},
		},
	},
		WithWorkingDir(workspaceDir),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	content := strings.ToLower(strings.TrimSpace(resp.Choices[0].Content))
	if !strings.Contains(content, "red") {
		t.Fatalf("expected image analysis to mention red, got %q", content)
	}
}

func TestCursorCLIStructuredSearchWeb(t *testing.T) {
	requireCursorCLIStructuredE2E(t)

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := adapter.SearchWeb(ctx,
		"What is the capital of France? Use web search and reply with the city and country only.",
	)
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(result), "paris") {
		t.Fatalf("expected result to mention Paris, got %q", result)
	}
}

func TestCursorCLIStructuredSearchWebLiveData(t *testing.T) {
	requireCursorCLIStructuredE2E(t)

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := adapter.SearchWeb(ctx,
		"Search the web for the latest Cursor CLI version number released in 2026. Reply with just the version string.",
	)
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	result = strings.TrimSpace(result)
	if result == "" {
		t.Fatal("SearchWeb returned empty result")
	}
	t.Logf("Live web search result: %s", result)
}
