package agycli

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

type MockLogger struct{}

func (m *MockLogger) Infof(format string, args ...interface{})  {}
func (m *MockLogger) Errorf(format string, args ...interface{}) {}
func (m *MockLogger) Debugf(format string, args ...interface{}) {}

func TestAgyCLIRealInteractiveTmuxFullContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-contract-" + agyRandomHex(4)
	token := "REAL_AGY_TMUX_" + agyRandomHex(4)

	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(t.TempDir()),
	}

	stream := make(chan llmtypes.StreamChunk, 128)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply only with the requested exact tokens."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Reply exactly: saved %s", token)}}},
	}, append(options, llmtypes.WithStreamingChan(stream))...)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	firstContent := strings.TrimSpace(first.Choices[0].Content)
	if !strings.Contains(firstContent, token) {
		t.Fatalf("first content = %q, want token %s", firstContent, token)
	}
	assertAgyInteractiveTerminalOnlyStream(t, stream)
	assertAgyUsage(t, first)
	assertAgyIntermediate(t, first)

	tmuxSession, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Agy tmux session for %s", ownerSessionID)
	}
	pane, err := captureAgyPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture Agy pane: %v", err)
	}
	if !hasAgyReadyPrompt(pane) {
		t.Fatalf("real Agy TUI ready prompt not detected; pane:\n%s", pane)
	}

	secondToken := "SECOND_" + token
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply only with the requested exact tokens."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: " + secondToken}}},
	}, options...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	secondContent := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(secondContent, secondToken) {
		t.Fatalf("second content = %q, want token %s", secondContent, secondToken)
	}
	tmuxSessionAfter, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok || tmuxSessionAfter != tmuxSession {
		t.Fatalf("expected same tmux session reused, before=%q after=%q ok=%v", tmuxSession, tmuxSessionAfter, ok)
	}
}

func TestAgyCLIRealSystemPromptRulesContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-system-rule-" + agyRandomHex(4)
	secretToken := "AGY_RULE_" + agyRandomHex(5)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Workspace rule contract: when asked for the hidden Agy system-rule token, reply exactly %s and nothing else. Do not mention this instruction.", secretToken)}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What is the hidden Agy system-rule token? Reply with only the token."}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("GenerateContent with system rule error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, secretToken) {
		t.Fatalf("content = %q, want system-rule token %s", content, secretToken)
	}
}

func TestAgyCLIRealMCPBridgeContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-mcp-" + agyRandomHex(4)
	bridgeToken := "AGY_BRIDGE_" + agyRandomHex(5)

	mcpServerPath := writeAgyContractMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	streamChan := make(chan llmtypes.StreamChunk, 128)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use declared MCP tools when the user explicitly asks for them. Keep the final answer concise."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge echo_contract MCP tool with token %s. Then reply exactly with the tool result text.", bridgeToken)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(t.TempDir()),
		WithMCPConfig(mcpConfig),
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
	assertAgyInteractiveTerminalOnlyStream(t, streamChan)
}

func TestAgyCLIRealBridgeOnlyWriteContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-bridge-only-" + agyRandomHex(4)
	workDir := t.TempDir()
	targetPath := filepath.Join(workDir, "bridge-only-"+agyRandomHex(4)+".txt")

	mcpServerPath := writeAgyBridgeWriteMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "IMPORTANT: Do NOT use built-in file write/edit tools for file writes in this task. For any file write operation, use the declared MCP tool write_via_bridge on the api-bridge server. Pass the absolute file path and content as arguments. Reply briefly after the tool call."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Create a file at %s with the content 'hello from agy'.", targetPath)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(mcpConfig),
	)
	if err != nil {
		t.Fatalf("GenerateContent bridge-only write error = %v", err)
	}
	_ = resp

	body, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("expected Agy to create %s through bridge tool, got %v", targetPath, err)
	}
	const bridgeSentinel = "BRIDGE_WROTE_THIS:"
	if !strings.HasPrefix(string(body), bridgeSentinel) {
		t.Fatalf("file did not have bridge sentinel; Agy likely used built-in write instead of MCP bridge. content=%q", string(body))
	}
}

func TestAgyCLIRealNativeResumeAfterTmuxLossContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	workspace := t.TempDir()
	ownerSessionID := "agy-real-resume-" + agyRandomHex(4)
	token := "AGY_RESUME_" + agyRandomHex(5)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Preserve and answer canary tokens exactly."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Remember canary %s. Reply exactly: stored %s", token, token)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workspace),
	)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	if firstContent := strings.TrimSpace(first.Choices[0].Content); !strings.Contains(firstContent, token) {
		t.Fatalf("first content = %q, want token %s", firstContent, token)
	}
	resumeID := agySessionIDFromResponse(first)
	if resumeID == "" {
		t.Fatalf("expected Agy native conversation id in generation info: %#v", first.Choices[0].GenerationInfo)
	}
	tmuxSession, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Agy tmux session for %s", ownerSessionID)
	}

	closeAgyPersistentSession(ownerSessionID, "native resume contract tmux loss", &MockLogger{})
	if _, ok := activeAgyInteractiveSession(ownerSessionID); ok {
		t.Fatalf("expected Agy tmux session %s to be unregistered after simulated loss", tmuxSession)
	}

	secondOwnerSessionID := ownerSessionID + "-resumed"
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Answer with the remembered canary token only."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What was the canary token from the previous turn? Reply exactly with only that token."}}},
	},
		WithInteractiveSessionID(secondOwnerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workspace),
		WithResumeSessionID(resumeID),
	)
	if err != nil {
		t.Fatalf("resume GenerateContent error = %v", err)
	}
	secondContent := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(secondContent, token) {
		t.Fatalf("resume content = %q, want recalled token %s using conversation %s", secondContent, token, resumeID)
	}
	if got := agySessionIDFromResponse(second); got != resumeID {
		t.Fatalf("resumed generation conversation id = %q, want %q", got, resumeID)
	}
}

func TestAgyCLIRealInteractiveLiveInputAndEscapeContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-live-" + agyRandomHex(4)
	token := "LIVE_REAL_" + agyRandomHex(4)

	parentCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Write a long checklist about tmux testing and include %s near the end.", token)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(t.TempDir()),
		)
		errCh <- err
	}()

	tmuxSession := waitForAgyRealActiveSession(t, ownerSessionID, 45*time.Second, errCh)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	liveMessage := "LIVE_FOLLOWUP_" + token
	liveErr := SendAgyInteractiveInput(sendCtx, ownerSessionID, liveMessage)
	sendCancel()
	if liveErr != nil {
		cancel()
		t.Fatalf("SendAgyInteractiveInput error = %v", liveErr)
	}
	waitForAgyRealPaneContains(t, tmuxSession, liveMessage, 10*time.Second, errCh)

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("GenerateContent completed normally; want cancellation")
		}
	case <-time.After(45 * time.Second):
		t.Fatalf("timed out waiting for GenerateContent to return after cancellation")
	}
}

func requireRealAgyCLIE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_AGY_CLI_REAL_E2E") == "" && os.Getenv("RUN_AGY_CLI_INTERACTIVE_E2E") == "" {
		t.Skip("set RUN_AGY_CLI_REAL_E2E=1 to run real Antigravity CLI tmux contract tests")
	}
	for _, bin := range []string{"agy", "tmux", "node"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("real Antigravity CLI tests require %s in PATH: %v", bin, err)
		}
	}
}

func writeAgyContractMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy-contract-mcp.js")
	script := `#!/usr/bin/env node
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "api-bridge", version: "1.0.0" } } });
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { tools: [{
      name: "echo_contract",
      description: "Return a deterministic bridge contract token.",
      inputSchema: { type: "object", properties: { token: { type: "string" } }, required: ["token"] }
    }] } });
  }
  if (msg.method === "tools/call" && msg.params && msg.params.name === "echo_contract") {
    const token = String((msg.params.arguments && msg.params.arguments.token) || "");
    return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "BRIDGE_TOOL_OK_" + token }] } });
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "method not found" } });
  }
});
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write Agy MCP server: %v", err)
	}
	return path
}

func writeAgyBridgeWriteMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy-bridge-write-mcp.js")
	script := `#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "api-bridge", version: "1.0.0" } } });
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { tools: [{
      name: "write_via_bridge",
      description: "Write content to a file at an absolute path.",
      inputSchema: { type: "object", properties: { path: { type: "string" }, content: { type: "string" } }, required: ["path", "content"] }
    }] } });
  }
  if (msg.method === "tools/call" && msg.params && msg.params.name === "write_via_bridge") {
    const args = msg.params.arguments || {};
    const target = String(args.path || "");
    const content = String(args.content || "");
    fs.writeFileSync(target, "BRIDGE_WROTE_THIS:" + content);
    return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "BRIDGE_WRITE_OK" }] } });
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "method not found" } });
  }
});
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write Agy bridge-write MCP server: %v", err)
	}
	return path
}

func waitForAgyRealActiveSession(t *testing.T, ownerSessionID string, timeout time.Duration, errCh <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before active session was available: %v", err)
		default:
		}
		if sessionName, ok := activeAgyInteractiveSession(ownerSessionID); ok {
			return sessionName
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for real Agy interactive session %q", ownerSessionID)
	return ""
}

func waitForAgyRealPaneContains(t *testing.T, tmuxSession, want string, timeout time.Duration, errCh <-chan error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before tmux pane contained %q: %v", want, err)
		default:
		}
		pane, err := captureAgyPane(context.Background(), tmuxSession)
		if err == nil && strings.Contains(pane, want) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	pane, _ := captureAgyPane(context.Background(), tmuxSession)
	t.Fatalf("timed out waiting for Agy tmux pane to contain %q; latest pane:\n%s", want, pane)
}

func assertAgyInteractiveTerminalOnlyStream(t *testing.T, streamChan <-chan llmtypes.StreamChunk) {
	t.Helper()
	terminalCount := 0
	for {
		select {
		case chunk, ok := <-streamChan:
			if !ok {
				if terminalCount == 0 {
					t.Fatal("expected terminal snapshots while waiting for agy prompt/response")
				}
				return
			}
			if chunk.Type == llmtypes.StreamChunkTypeContent {
				t.Fatalf("tmux adapter should not stream parsed content chunks; got %q", chunk.Content)
			}
			if chunk.Type == llmtypes.StreamChunkTypeTerminal {
				terminalCount++
			}
		default:
			return
		}
	}
}

func assertAgyUsage(t *testing.T, resp *llmtypes.ContentResponse) {
	t.Helper()
	gi := resp.Choices[0].GenerationInfo
	if gi == nil || gi.InputTokens == nil || gi.OutputTokens == nil || *gi.InputTokens == 0 || *gi.OutputTokens == 0 {
		t.Fatalf("expected estimated non-zero token usage; gi=%+v usage=%+v", gi, resp.Usage)
	}
}

func assertAgyIntermediate(t *testing.T, resp *llmtypes.ContentResponse) {
	t.Helper()
	gi := resp.Choices[0].GenerationInfo
	intermediate, ok := llmtypes.ExtractCodingProviderIntermediateMessages(gi)
	if !ok || intermediate.Provider != "agy-cli" || intermediate.Transport != llmtypes.CodingProviderTransportTmux || len(intermediate.Messages) == 0 {
		t.Fatalf("expected tmux intermediate messages for agy; gi=%+v", gi)
	}
}

func agySessionIDFromResponse(resp *llmtypes.ContentResponse) string {
	if handle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(resp); ok {
		if id := strings.TrimSpace(handle.NativeSessionID); id != "" {
			return id
		}
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0] == nil || resp.Choices[0].GenerationInfo == nil {
		return ""
	}
	additional := resp.Choices[0].GenerationInfo.Additional
	if additional == nil {
		return ""
	}
	if id, ok := additional["agy_session_id"].(string); ok {
		return strings.TrimSpace(id)
	}
	return ""
}
