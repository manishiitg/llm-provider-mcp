package cursorcli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/agentreview"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCursorCLITranscriptStreamingBridgeLive is the bridge-only streaming P0
// proof for Cursor, mirroring the Claude/Codex BridgeLive tests: a real
// authenticated Cursor tmux turn against a real MCP server (api-bridge /
// contract_echo_token) driving a multi-step task that forces narration
// interleaved with two MCP tool calls. With CURSOR_CLI_STREAM_TRANSCRIPT=1 it
// asserts the store.db tailer streamed structured assistant-text (Content) and
// MCP tool-call (ToolCallStart) chunks, and that the tools actually executed
// (both tokens returned). Cursor commits store.db asynchronously so streaming is
// laggier than the JSONL adapters; assertions require SOMETHING structured
// streamed rather than strict counts, matching the realworld test's tolerance.
//
// Gated behind -coding-cli-p0-live; requires a real cursor-agent CLI, node, tmux.
func TestCursorCLITranscriptStreamingBridgeLive(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Setenv(EnvCursorInteractiveStreamTranscript, "1")
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	workDir := t.TempDir()
	tokenA := "A_" + cursorRandomHex(4)
	tokenB := "B_" + cursorRandomHex(4)
	wantA := "BRIDGE_TOOL_OK_" + tokenA
	wantB := "BRIDGE_TOOL_OK_" + tokenB

	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"type":"stdio","command":"node","args":[%q]}}}`, writeCursorTmuxContractMCPServer(t))
	preApproveCursorMCP(t, workDir, mcpConfig, "api-bridge")

	streamChan := make(chan llmtypes.StreamChunk, 2048)
	captureDone := collectCursorTranscriptStream(streamChan)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	task := "You are verifying an MCP bridge. The 'api-bridge' MCP server exposes one tool, contract_echo_token. " +
		"Do these steps in order, writing one short sentence of narration BEFORE each tool call:\n" +
		"1. Narrate, then call contract_echo_token with token " + tokenA + ".\n" +
		"2. Narrate, then call contract_echo_token with token " + tokenB + ".\n" +
		"Finally, on one line, reply with both tool result strings exactly as returned."

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, task)},
		WithInteractiveSessionID("cursor-bridge-stream-"+cursorRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(mcpConfig),
		WithApproveMCPs(),
		WithForce(),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	capture := <-captureDone

	final := ""
	if len(resp.Choices) == 1 {
		final = strings.TrimSpace(resp.Choices[0].Content)
	}
	t.Logf("cursor bridge transcript stream: %d content, %d tool start(s) %v, order=%v, %d terminal; streamed=%q final=%q",
		capture.contentChunks, capture.toolStarts, capture.toolNames, capture.order, capture.terminalOnly,
		strings.TrimSpace(capture.content.String()), final)

	// End-to-end: the real MCP tool executed (both tokens came back in the reply).
	if !strings.Contains(final, wantA) || !strings.Contains(final, wantB) {
		t.Fatalf("final response missing bridge tool results (%q and %q); the real MCP tool did not both run. final=%q", wantA, wantB, final)
	}
	// Streaming: the store.db tailer emitted structured chunks. Cursor's async
	// commit is laggy, so require SOMETHING structured (content and/or a tool).
	if capture.contentChunks == 0 && capture.toolStarts == 0 {
		t.Fatalf("no transcript-sourced chunks streamed at all; order=%v", capture.order)
	}

	rec := agentreview.Write(t, "TestCursorCLITranscriptStreamingBridgeLive",
		"Cursor bridge-only: narrate + 2 interleaved MCP contract_echo_token calls, streamed from store.db",
		map[string]any{
			"content_chunks":   capture.contentTexts,
			"streamed_content": strings.TrimSpace(capture.content.String()),
			"stream_order":     capture.order,
			"tool_names":       capture.toolNames,
			"final":            final,
		},
		map[string]any{"distinct_tools": sortedKeys(distinctToolNames(capture.toolNames))},
	)
	agentreview.RequireReviewed(t, rec)
}

// TestCursorCLITranscriptStreamingDisabledControl is the control for the test
// above: the SAME real bridge turn with the feature OFF (env flag unset). It
// proves the structured content/tool stream is produced by THIS feature — with
// streaming disabled the store.db tailer emits nothing, so a no-terminal UI gets
// no structured chunks. The tool still runs (tokens come back), it just does not
// stream as structured chunks.
func TestCursorCLITranscriptStreamingDisabledControl(t *testing.T) {
	requireRealCursorCLIE2E(t)
	// Deliberately DO NOT set EnvCursorInteractiveStreamTranscript — feature OFF.
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	workDir := t.TempDir()
	tokenA := "A_" + cursorRandomHex(4)
	tokenB := "B_" + cursorRandomHex(4)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"type":"stdio","command":"node","args":[%q]}}}`, writeCursorTmuxContractMCPServer(t))
	preApproveCursorMCP(t, workDir, mcpConfig, "api-bridge")

	streamChan := make(chan llmtypes.StreamChunk, 2048)
	captureDone := collectCursorTranscriptStream(streamChan)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	task := "Call the api-bridge contract_echo_token tool with token " + tokenA + ", then again with token " + tokenB +
		", then reply with both returned tool result strings on one line."
	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, task)},
		WithInteractiveSessionID("cursor-bridge-stream-off-"+cursorRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(mcpConfig),
		WithApproveMCPs(),
		WithForce(),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	capture := <-captureDone
	final := ""
	if len(resp.Choices) == 1 {
		final = strings.TrimSpace(resp.Choices[0].Content)
	}
	t.Logf("CONTROL (feature OFF): %d transcript-content, %d transcript-tool, %d terminal; final=%q",
		capture.contentChunks, capture.toolStarts, capture.terminalOnly, final)

	// The tool still ran (proves it was a real, comparable turn)...
	if !strings.Contains(final, "BRIDGE_TOOL_OK_"+tokenA) || !strings.Contains(final, "BRIDGE_TOOL_OK_"+tokenB) {
		t.Fatalf("control turn did not actually run the tools; final=%q", final)
	}
	// ...but with the feature OFF, NO structured content/tool chunks are emitted.
	if capture.contentChunks != 0 || capture.toolStarts != 0 {
		t.Fatalf("feature OFF but transcript-sourced chunks appeared (%d content, %d tool) — the stream was NOT coming from this feature",
			capture.contentChunks, capture.toolStarts)
	}
}
