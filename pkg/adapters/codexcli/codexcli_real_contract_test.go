package codexcli

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

func TestCodexCLIRealInteractiveTmuxFullContract(t *testing.T) {
	requireRealCodexCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ownerSessionID := "codex-real-contract-" + codexRandomHex(4)
	token := "REAL_CODEX_TMUX_" + codexRandomHex(4)

	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithDisableShellTool(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
	}

	largeSystemPrompt := strings.Repeat("You are testing the Codex CLI tmux transport. Do not use tools. Keep exact-token replies concise.\n", 80)
	firstPrompt := fmt.Sprintf(`This is a real Codex CLI tmux contract test.

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
	assertCodexInteractiveTerminalOnlyStream(t, firstStream)

	tmuxSession, ok := activeCodexInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Codex tmux session for %s", ownerSessionID)
	}
	pane, err := captureCodexPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture Codex pane: %v", err)
	}
	if !hasCodexReadyPrompt(pane) {
		t.Fatalf("real Codex TUI ready prompt not detected; pane:\n%s", pane)
	}
	if hasCodexActivity(pane) {
		t.Fatalf("real Codex TUI should be idle after first turn; pane:\n%s", pane)
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
	assertCodexInteractiveTerminalOnlyStream(t, secondStream)

	tmuxSessionAfter, ok := activeCodexInteractiveSession(ownerSessionID)
	if !ok || tmuxSessionAfter != tmuxSession {
		t.Fatalf("expected same tmux session reused, before=%q after=%q ok=%v", tmuxSession, tmuxSessionAfter, ok)
	}
}

func TestCodexCLIRealInteractiveMCPBridgeContract(t *testing.T) {
	requireRealCodexCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ownerSessionID := "codex-real-mcp-" + codexRandomHex(4)
	bridgeToken := "BRIDGE_REAL_" + codexRandomHex(4)

	mcpServerPath := writeCodexContractMCPServer(t)
	mcpCommandOverride, err := codexStringConfigOverride("mcp_servers.api-bridge.command", mcpServerPath)
	if err != nil {
		t.Fatalf("build MCP command override: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 128)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge echo_contract MCP tool with token %s. Then reply exactly with the tool result text.", bridgeToken)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithDisableShellTool(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
		WithConfigOverrides([]string{mcpCommandOverride}),
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
	assertCodexInteractiveTerminalOnlyStream(t, streamChan)
}

func TestCodexCLIRealInteractiveWorkspaceTrustPromptContract(t *testing.T) {
	requireRealCodexCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ownerSessionID := "codex-real-trust-" + codexRandomHex(4)
	token := "TRUST_REAL_" + codexRandomHex(4)
	streamChan := make(chan llmtypes.StreamChunk, 128)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Keep the response exact and concise."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: trusted " + token}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithProjectDirID(t.TempDir()),
		WithDisableShellTool(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent in fresh workspace error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("content = %q, want trust token %s", content, token)
	}
	assertCodexDoesNotContainAny(t, "trust response", content, "No, quit", "Press enter to continue", "Do you trust")
	assertCodexInteractiveTerminalOnlyStream(t, streamChan)
}

func TestCodexCLIRealInteractiveQueuedValidationDoesNotCompleteDuringMCPTool(t *testing.T) {
	requireRealCodexCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ownerSessionID := "codex-real-queued-validation-" + codexRandomHex(4)
	bridgeToken := "SLOW_BRIDGE_REAL_" + codexRandomHex(4)

	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")
	mcpServerPath := writeCodexSlowContractMCPServer(t, slowToolMarker)
	mcpCommandOverride, err := codexStringConfigOverride("mcp_servers.api-bridge.command", mcpServerPath)
	if err != nil {
		t.Fatalf("build MCP command override: %v", err)
	}

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan codexRealResult, 1)
	startupErrCh := make(chan error, 1)
	streamChan := make(chan llmtypes.StreamChunk, 128)

	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge slow_contract MCP tool with token %s and delay_ms 30000. Do not answer until the tool returns. Then reply exactly with the tool result text.", bridgeToken)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithDisableShellTool(),
			WithApprovalPolicy("never"),
			WithReasoningEffort("low"),
			WithConfigOverrides([]string{mcpCommandOverride}),
			llmtypes.WithStreamingChan(streamChan),
		)
		out := codexRealResult{err: err}
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

	tmuxSession := waitForCodexRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForCodexRealFile(t, slowToolMarker, "slow MCP tool call start", 90*time.Second, resultCh)

	activePane := waitForCodexRealPaneCondition(t, tmuxSession, "active slow MCP tool", 15*time.Second, resultCh, func(pane string) bool {
		return hasCodexActivity(pane)
	})
	if hasCodexReadyPrompt(activePane) {
		cancel()
		t.Fatalf("Codex pane looked ready while slow MCP tool was still active:\n%s", activePane)
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
	if err := SendCodexInteractiveInput(sendCtx, ownerSessionID, validationPrompt); err != nil {
		sendCancel()
		cancel()
		t.Fatalf("SendCodexInteractiveInput error = %v", err)
	}
	sendCancel()

	queuedPane := waitForCodexRealPaneCondition(t, tmuxSession, "queued validation input", 15*time.Second, resultCh, func(pane string) bool {
		return hasCodexQueuedInput(pane) || strings.Contains(pane, "Pre-validation failed")
	})
	if !hasCodexActivity(queuedPane) {
		cancel()
		t.Fatalf("Codex queued validation pane was not considered active:\n%s", queuedPane)
	}
	if hasCodexReadyPrompt(queuedPane) {
		cancel()
		t.Fatalf("Codex queued validation pane looked ready/completed:\n%s", queuedPane)
	}

	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while validation input was queued: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while validation input was queued; content=%q", got.content)
	case <-time.After(3 * time.Second):
		// The regression was an early false completion while the TUI still showed
		// queued input. Remaining active here proves the adapter did not parse the
		// queued validation prompt as the assistant's final response.
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
	_ = drainCodexStream(streamChan)
}

func TestCodexCLIRealInteractiveLiveInputAndEscapeContract(t *testing.T) {
	requireRealCodexCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ownerSessionID := "codex-real-live-" + codexRandomHex(4)
	token := "LIVE_REAL_" + codexRandomHex(4)

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
			WithDisableShellTool(),
			WithApprovalPolicy("never"),
			WithReasoningEffort("low"),
			llmtypes.WithStreamingChan(streamChan),
		)
		errCh <- err
	}()

	tmuxSession := waitForCodexRealActiveSession(t, ownerSessionID, 45*time.Second, errCh)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	liveMessage := "LIVE_FOLLOWUP_" + token
	liveErr := SendCodexInteractiveInput(sendCtx, ownerSessionID, liveMessage)
	sendCancel()
	if liveErr != nil {
		cancel()
		t.Fatalf("SendCodexInteractiveInput error = %v", liveErr)
	}

	pane, err := captureCodexPane(context.Background(), tmuxSession)
	if err != nil {
		cancel()
		t.Fatalf("capture Codex pane after live input: %v", err)
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
	_ = drainCodexStream(streamChan)
}

func requireRealCodexCLIE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_CODEX_CLI_REAL_E2E") == "" && os.Getenv("RUN_CODEX_CLI_INTERACTIVE_E2E") == "" {
		t.Skip("set RUN_CODEX_CLI_REAL_E2E=1 to run real Codex CLI tmux contract tests")
	}
	for _, bin := range []string{"codex", "tmux", "node"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("real Codex CLI tests require %s in PATH: %v", bin, err)
		}
	}
}

func waitForCodexRealActiveSession(t *testing.T, ownerSessionID string, timeout time.Duration, errCh <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before active session was available: %v", err)
		default:
		}
		if sessionName, ok := activeCodexInteractiveSession(ownerSessionID); ok {
			return sessionName
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for real Codex interactive session %q", ownerSessionID)
	return ""
}

func waitForCodexRealPaneCondition(t *testing.T, tmuxSession, label string, timeout time.Duration, errCh <-chan codexRealResult, matches func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case got := <-errCh:
			t.Fatalf("GenerateContent returned before tmux pane matched %s: err=%v content=%q", label, got.err, got.content)
		default:
		}
		pane, err := captureCodexPane(context.Background(), tmuxSession)
		if err == nil && matches(pane) {
			return pane
		}
		time.Sleep(250 * time.Millisecond)
	}
	pane, _ := captureCodexPane(context.Background(), tmuxSession)
	t.Fatalf("timed out waiting for Codex tmux pane to match %s; latest pane:\n%s", label, pane)
	return ""
}

func waitForCodexRealFile(t *testing.T, path, label string, timeout time.Duration, errCh <-chan codexRealResult) {
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

type codexRealResult struct {
	content string
	err     error
}

func writeCodexContractMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-contract-mcp.js")
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
        tools: [
          {
            name: "echo_contract",
            description: "Return a deterministic contract token.",
            inputSchema: {
              type: "object",
              properties: { token: { type: "string" } },
              required: ["token"]
            }
          },
          {
            name: "slow_contract",
            description: "Return a deterministic contract token after a caller-provided delay.",
            inputSchema: {
              type: "object",
              properties: {
                token: { type: "string" },
                delay_ms: { type: "number" }
              },
              required: ["token"]
            }
          }
        ]
      }
    });
    return;
  }
  if (msg.method === "tools/call") {
    const name = String((msg.params && msg.params.name) || "");
    const args = (msg.params && msg.params.arguments) || {};
    const token = String(args.token || "");
    if (name === "slow_contract") {
      const delay = Math.max(0, Math.min(Number(args.delay_ms || 30000), 60000));
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

func writeCodexSlowContractMCPServer(t *testing.T, markerPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-slow-contract-mcp.js")
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
  if (msg.method === "notifications/initialized") {
    return;
  }
  if (msg.method === "tools/list") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        tools: [{
          name: "slow_contract",
          description: "Return a deterministic contract token after a caller-provided delay.",
          inputSchema: {
            type: "object",
            properties: {
              token: { type: "string" },
              delay_ms: { type: "number" }
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
    const delay = Math.max(0, Math.min(Number(args.delay_ms || 30000), 60000));
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
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, result: {} });
  }
});
`, markerPath)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write slow MCP server: %v", err)
	}
	return path
}

func assertCodexDoesNotContainAny(t *testing.T, label, got string, forbidden ...string) {
	t.Helper()
	for _, item := range forbidden {
		if strings.Contains(got, item) {
			t.Fatalf("%s leaked %q in %q", label, item, got)
		}
	}
}
