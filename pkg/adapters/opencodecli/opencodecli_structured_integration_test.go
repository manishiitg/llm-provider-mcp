package opencodecli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestOpenCodeCLIStructuredBasicRun(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	workspaceDir := t.TempDir()
	gitInit(t, workspaceDir)

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What is the capital of Japan? Reply with just the city name."},
			},
		},
	}, WithWorkingDir(workspaceDir))
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

func TestOpenCodeCLIStructuredTokenUsage(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	workspaceDir := t.TempDir()
	gitInit(t, workspaceDir)

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Say hello."},
			},
		},
	}, WithWorkingDir(workspaceDir))
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

func TestOpenCodeCLIStructuredSystemPrompt(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	workspaceDir := t.TempDir()
	gitInit(t, workspaceDir)

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	canary := "CANARY_" + opencodeRandomHex(4)
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
	}, WithWorkingDir(workspaceDir))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	content := resp.Choices[0].Content
	if !strings.Contains(content, canary) {
		t.Fatalf("expected system prompt canary %q in response, got %q", canary, content)
	}
}

func TestOpenCodeCLIStructuredStreaming(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	workspaceDir := t.TempDir()
	gitInit(t, workspaceDir)

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
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
		WithWorkingDir(workspaceDir),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	var streamedContent []string
	for chunk := range stream {
		if chunk.Type == llmtypes.StreamChunkTypeContent {
			streamedContent = append(streamedContent, chunk.Content)
		}
	}
	if len(streamedContent) == 0 {
		t.Fatal("expected streaming content chunks")
	}

	streamed := strings.Join(streamedContent, "")
	if strings.TrimSpace(streamed) != strings.TrimSpace(resp.Choices[0].Content) {
		t.Logf("streamed: %q", streamed)
		t.Logf("final:    %q", resp.Choices[0].Content)
	}
}

func TestOpenCodeCLIStructuredToolUseProducesToolChunks(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	workspaceDir := t.TempDir()
	gitInit(t, workspaceDir)

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	stream := make(chan llmtypes.StreamChunk, 256)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "List the files in the current directory using the shell. Then say done."},
			},
		},
	},
		WithWorkingDir(workspaceDir),
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
	if !hasToolStart {
		t.Log("warning: no tool_call_start stream chunk (tool may not have been used)")
	}
	if !hasToolEnd {
		t.Log("warning: no tool_call_end stream chunk (tool may not have been used)")
	}
}

func TestOpenCodeCLIStructuredSessionIDInMetadata(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	workspaceDir := t.TempDir()
	gitInit(t, workspaceDir)

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Say hi."},
			},
		},
	}, WithWorkingDir(workspaceDir))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	gen := resp.Choices[0].GenerationInfo
	if gen == nil || gen.Additional == nil {
		t.Fatal("expected GenerationInfo with Additional metadata")
	}
	sessionID, ok := gen.Additional["opencode_session_id"].(string)
	if !ok || sessionID == "" {
		t.Fatal("expected opencode_session_id in generation metadata")
	}
	mode, _ := gen.Additional["opencode_mode"].(string)
	if mode != "structured" {
		t.Fatalf("expected opencode_mode=structured, got %q", mode)
	}
	t.Logf("session_id=%s mode=%s", sessionID, mode)
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}
