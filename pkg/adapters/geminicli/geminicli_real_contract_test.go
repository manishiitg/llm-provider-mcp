package geminicli

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

func TestGeminiCLIRealInteractiveTmuxFullContract(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ownerSessionID := "gemini-real-contract-" + geminiRandomHex(4)
	token := "REAL_GEMINI_TMUX_" + geminiRandomHex(4)

	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "*"
decision = "deny"
priority = 999
deny_message = "No tools are needed for this contract test."
`)

	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithProjectSettings(`{}`),
		WithAdminPolicyPath(policyPath),
		WithApprovalMode("yolo"),
	}

	largeSystemPrompt := strings.Repeat("You are testing the Gemini CLI tmux transport. Do not use tools. Keep exact-token replies concise.\n", 80)
	firstPrompt := fmt.Sprintf(`This is a real Gemini CLI tmux contract test.

Preserve input safely:

blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते

Take note of the word %s. Do not save it to memory.

Reply exactly:
noted %s`, token, token, token)

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
	assertGeminiInteractiveTerminalOnlyStream(t, firstStream)

	tmuxSession, ok := activeGeminiInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Gemini tmux session for %s", ownerSessionID)
	}
	pane, err := captureGeminiPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture Gemini pane: %v", err)
	}
	if !hasGeminiReadyPrompt(pane) {
		t.Fatalf("real Gemini TUI ready prompt not detected; pane:\n%s", pane)
	}
	if hasGeminiActivity(pane) {
		t.Fatalf("real Gemini TUI should be idle after first turn; pane:\n%s", pane)
	}

	secondStream := make(chan llmtypes.StreamChunk, 64)
	secondOptions := append([]llmtypes.CallOption{}, options...)
	secondOptions = append(secondOptions, llmtypes.WithStreamingChan(secondStream))
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: largeSystemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What exact word did I ask you to take note of? Reply with only that word."}}},
	}, secondOptions...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	secondContent := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(secondContent, token) {
		t.Fatalf("second content = %q, want remembered token %s from native tmux session", secondContent, token)
	}
	if strings.Contains(secondContent, "saved "+token) {
		t.Fatalf("second content replayed first assistant response: %q", secondContent)
	}
	assertGeminiInteractiveTerminalOnlyStream(t, secondStream)

	tmuxSessionAfter, ok := activeGeminiInteractiveSession(ownerSessionID)
	if !ok || tmuxSessionAfter != tmuxSession {
		t.Fatalf("expected same tmux session reused, before=%q after=%q ok=%v", tmuxSession, tmuxSessionAfter, ok)
	}
}

func TestGeminiCLIRealInteractiveMCPBridgeContract(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ownerSessionID := "gemini-real-mcp-" + geminiRandomHex(4)
	bridgeToken := "BRIDGE_REAL_" + geminiRandomHex(4)

	mcpServerPath := writeGeminiContractMCPServer(t)
	settings := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":%q}}}`, mcpServerPath)
	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "mcp_api-bridge_echo_contract"
decision = "allow"
priority = 999

[[rule]]
toolName = "*"
decision = "deny"
priority = 998
deny_message = "Use only the api-bridge echo_contract MCP tool for this contract test."
`)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 128)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge echo_contract MCP tool with token %s. Then reply exactly with the tool result text.", bridgeToken)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithProjectSettings(settings),
		WithAdminPolicyPath(policyPath),
		WithApprovalMode("yolo"),
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
	assertGeminiInteractiveTerminalOnlyStream(t, streamChan)
}

func TestGeminiCLIRealInteractiveLiveInputContract(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ownerSessionID := "gemini-real-live-" + geminiRandomHex(4)
	token := "LIVE_REAL_" + geminiRandomHex(4)

	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "*"
decision = "deny"
priority = 999
deny_message = "No tools are needed for this live-input contract test."
`)

	parentCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	type liveResult struct {
		content string
		err     error
	}
	errCh := make(chan liveResult, 1)
	startupErrCh := make(chan error, 1)
	streamChan := make(chan llmtypes.StreamChunk, 128)

	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. This is a live input transport test."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Draft a detailed 500 word checklist about tmux transport testing. Include token %s near the end only.", token)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithProjectSettings(`{}`),
			WithAdminPolicyPath(policyPath),
			WithApprovalMode("yolo"),
			llmtypes.WithStreamingChan(streamChan),
		)
		result := liveResult{err: err}
		if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
			result.content = resp.Choices[0].Content
		} else if err != nil {
			startupErrCh <- err
		}
		errCh <- result
	}()

	tmuxSession := waitForGeminiRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	liveAck := "LIVE_ACK_" + token
	liveMessage := "Queued live follow-up: after finishing the current answer, reply exactly " + liveAck
	liveErr := SendGeminiInteractiveInput(sendCtx, ownerSessionID, liveMessage)
	sendCancel()
	if liveErr != nil {
		t.Fatalf("SendGeminiInteractiveInput error = %v", liveErr)
	}

	pane, err := captureGeminiPane(context.Background(), tmuxSession)
	if err != nil {
		t.Fatalf("capture Gemini pane after live input: %v", err)
	}
	pendingLiveInput := ""
	if session, ok := geminiPersistentSession(ownerSessionID); ok {
		session.liveMu.Lock()
		pendingLiveInput = strings.Join(session.pendingLiveInputs, "\n")
		session.liveMu.Unlock()
	}
	if !strings.Contains(pane, liveMessage) && !strings.Contains(pendingLiveInput, liveMessage) {
		t.Fatalf("live input was neither visible in tmux pane nor queued in adapter; pending=%q pane:\n%s", pendingLiveInput, pane)
	}

	select {
	case result := <-errCh:
		if result.err != nil {
			t.Fatalf("GenerateContent error = %v", result.err)
		}
		paneAfter, err := captureGeminiPane(context.Background(), tmuxSession)
		if err != nil {
			t.Fatalf("capture Gemini pane after live response: %v", err)
		}
		if !strings.Contains(result.content, liveAck) && !strings.Contains(paneAfter, liveAck) {
			t.Fatalf("live input was queued but not processed; content=%q pane:\n%s", result.content, paneAfter)
		}
	case <-time.After(4 * time.Minute):
		t.Fatalf("timed out waiting for Gemini to process live input")
	}
	_ = drainGeminiStream(streamChan)
}

func requireRealGeminiCLIE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_GEMINI_CLI_REAL_E2E") == "" && os.Getenv("RUN_GEMINI_CLI_INTERACTIVE_E2E") == "" {
		t.Skip("set RUN_GEMINI_CLI_REAL_E2E=1 to run real Gemini CLI tmux contract tests")
	}
	if strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) == "" {
		t.Fatal("real Gemini CLI tests require GEMINI_API_KEY")
	}
	for _, bin := range []string{"gemini", "tmux", "node"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("real Gemini CLI tests require %s in PATH: %v", bin, err)
		}
	}
}

func waitForGeminiRealActiveSession(t *testing.T, ownerSessionID string, timeout time.Duration, errCh <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before active session was available: %v", err)
		default:
		}
		if sessionName, ok := activeGeminiInteractiveSession(ownerSessionID); ok {
			return sessionName
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for real Gemini interactive session %q", ownerSessionID)
	return ""
}

func writeGeminiRealPolicy(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "restrict-tools.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write Gemini policy: %v", err)
	}
	return path
}

func writeGeminiContractMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gemini-contract-mcp.js")
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
  if (msg.method === "notifications/initialized") {
    return;
  }
  if (msg.method === "tools/list") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        tools: [{
          name: "echo_contract",
          description: "Return a deterministic contract token.",
          inputSchema: {
            type: "object",
            properties: { token: { type: "string" } },
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
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        content: [{ type: "text", text: "BRIDGE_TOOL_OK_" + token }],
        isError: false
      }
    });
    return;
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, result: {} });
  }
});
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write MCP server: %v", err)
	}
	return path
}

func assertGeminiDoesNotContainAny(t *testing.T, label, got string, forbidden ...string) {
	t.Helper()
	for _, item := range forbidden {
		if strings.Contains(got, item) {
			t.Fatalf("%s leaked %q in %q", label, item, got)
		}
	}
}
