package picli

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

func TestPiCLIRealMCPBridgeOnlyToolsContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	apiKey := firstNonEmptyPiTestEnv("GEMINI_API_KEY", "GOOGLE_API_KEY", "PI_API_KEY")

	workDir := t.TempDir()
	serverPath := filepath.Join(workDir, "pi-mcp-canary-server.js")
	logPath := filepath.Join(workDir, "pi-mcp-canary-calls.jsonl")
	if err := os.WriteFile(serverPath, []byte(piMCPBridgeCanaryServerSource()), 0o700); err != nil {
		t.Fatalf("write MCP canary server: %v", err)
	}
	mcpConfig := fmt.Sprintf(`{
  "mcpServers": {
    "api-bridge": {
      "command": "node",
      "args": [%q],
      "env": {"PI_MCP_CANARY_LOG": %q},
      "lifecycle": "keep-alive",
      "directTools": true
    }
  }
}`, serverPath, logPath)

	adapter := NewPiCLIAdapter(apiKey, piRealContractModel(), &mockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ownerSessionID := "pi-mcp-bridge-e2e-" + piRandomHex(6)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Use the MCP gateway only. Call the api-bridge MCP tool bridge_canary, then reply exactly with the tool output text. If direct api_bridge_bridge_canary is unavailable, use mcp({ search: \"bridge_canary\" }) and mcp({ tool: \"api_bridge_bridge_canary\", args: \"{}\" })."),
	}, WithWorkingDir(workDir), WithInteractiveSessionID(ownerSessionID), WithPersistentInteractiveSession(true), WithMCPConfig(mcpConfig), WithBridgeOnlyTools(true))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("GenerateContent() returned no choices")
	}
	if err := waitForPiMCPBridgeCanaryLog(ctx, logPath); err != nil {
		t.Fatalf("MCP canary tool was not called: %v\nresponse=%q", err, resp.Choices[0].Content)
	}
	if !strings.Contains(resp.Choices[0].Content, "PI_MCP_BRIDGE_OK") {
		t.Fatalf("response = %q, want PI_MCP_BRIDGE_OK", resp.Choices[0].Content)
	}
	ClosePiCLIInteractiveSessionForOwner(ownerSessionID, "test cleanup")
	if _, err := os.Stat(filepath.Join(workDir, ".pi", "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf(".pi/mcp.json should be removed after persistent cleanup, err=%v", err)
	}
}

// PLAT-186. pi-mcp-adapter (the third-party MCP extension pi-cli loads)
// only registers "directTools" -- native, individually-typed tools -- when
// its on-disk metadata cache is valid for the CURRENT launch's server
// config, judged by a hash of that config's entire declared shape,
// including env (confirmed by reading pi-mcp-adapter@2.27.0's
// computeServerHash/resolveDirectTools source directly). When that hash
// doesn't match, every tool call is forced through the generic,
// double-JSON-encoded "mcp(tool, args)" proxy wrapper instead -- fragile
// enough that a live incident happened from a model malforming that
// encoding. This proves the other half: that pi-mcp-adapter actually DOES
// activate direct tools when given a stable config across two real,
// separate launches -- not just that our own config stays byte-identical
// (that half is covered in mcpagent's
// TestBuildBridgeMCPConfigReadyFileIsStableAcrossRepeatedCallsForTheSameIdentity).
//
// The distinguishing signal has to be the RAW, pre-recovery tool args, not
// the tool name: PLAT-179's own recovery logic renames a successfully-
// recovered proxy call to look exactly like a real tool name, so a native
// direct call and a recovered proxy call are indistinguishable by name
// alone once recovery has run. A proxy call's raw args always carry the
// wrapper shape {"tool":"...","args":"..."}; a native call's raw args are
// the tool's own parameters directly. ToolArgs on tool_execution_start/end
// now carries those raw, pre-recovery args (this fix's own addition --
// every other adapter already populated it, pi's interactive adapter was
// the one gap) specifically so this can be checked from outside.
func TestPiCLIRealDirectToolsActivateOnRepeatedStableConfig(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	workDir := t.TempDir()
	serverPath := writePiReportCWDMCPServer(t)
	mcpConfig := fmt.Sprintf(`{
  "mcpServers": {
    "api-bridge": {
      "command": "node",
      "args": [%q],
      "lifecycle": "keep-alive",
      "directTools": true
    }
  }
}`, serverPath)

	prompt := "Call the api-bridge MCP tool report_cwd, then reply exactly with the tool output text. If direct api_bridge_report_cwd is unavailable, use mcp({ search: \"report_cwd\" }) and mcp({ tool: \"api_bridge_report_cwd\", args: \"{}\" })."

	runOnce := func(label string) []llmtypes.StreamChunk {
		adapter := newRealPiCLIAdapter(t)
		ownerSessionID := "pi-directtools-" + label + "-" + piRandomHex(6)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		stream := make(chan llmtypes.StreamChunk, 4096)
		_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
			llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, prompt),
		}, WithWorkingDir(workDir), WithInteractiveSessionID(ownerSessionID), WithPersistentInteractiveSession(true), WithMCPConfig(mcpConfig), llmtypes.WithStreamingChan(stream))
		if err != nil {
			t.Fatalf("GenerateContent() [%s] error = %v", label, err)
		}
		ClosePiCLIInteractiveSessionForOwner(ownerSessionID, "test cleanup")
		var chunks []llmtypes.StreamChunk
		for chunk := range stream {
			chunks = append(chunks, chunk)
		}
		return chunks
	}

	// First launch: cold cache for this config, if pi-mcp-adapter has never
	// seen it before on this machine. Not asserted on -- this call's only
	// job is to let the cache populate for the second call to build on.
	runOnce("warm")

	// Second launch: identical config (same command/args/directTools, and
	// critically no env changing between calls, matching what the mcpagent
	// fix now guarantees for a real workflow launch). If pi-mcp-adapter's
	// cache is honoring that stability, this call's report_cwd invocation
	// must be a native direct call, not a proxy call that recovery merely
	// renamed to look like one.
	chunks := runOnce("verify")

	sawReportCWDCall := false
	for _, chunk := range chunks {
		if chunk.Type != llmtypes.StreamChunkTypeToolCallStart {
			continue
		}
		if !strings.Contains(chunk.ToolName, "report_cwd") {
			continue
		}
		sawReportCWDCall = true
		args := strings.TrimSpace(chunk.ToolArgs)
		if strings.Contains(args, `"tool"`) && strings.Contains(args, `"args"`) {
			t.Fatalf("report_cwd's raw tool_execution_start args still carry the generic proxy wrapper shape "+
				"({\"tool\":...,\"args\":...}) on the SECOND launch with an identical, stable config: %s -- "+
				"directTools never actually activated despite the config being stable across launches", args)
		}
	}
	if !sawReportCWDCall {
		t.Fatal("expected a report_cwd tool_execution_start chunk on the second (cache-warm) launch")
	}
}

func TestPiCLIRealMCPOutputGuardCompactsLongSingleLineResult(t *testing.T) {
	if os.Getenv("RUN_PI_CLI_MCP_BRIDGE_E2E") != "1" {
		t.Skip("set RUN_PI_CLI_MCP_BRIDGE_E2E=1 to run real Pi CLI MCP bridge test")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node not available: %v", err)
	}
	if _, _, err := piCommandPrefix(); err != nil {
		t.Skip(err)
	}
	apiKey := firstNonEmptyPiTestEnv("GEMINI_API_KEY", "GOOGLE_API_KEY", "PI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY, GOOGLE_API_KEY, or PI_API_KEY is required for real Pi CLI MCP bridge test")
	}

	t.Setenv(EnvPiMCPResultMaxChars, "5000")
	t.Setenv(EnvPiMCPResultMaxLines, "80")

	workDir := t.TempDir()
	serverPath := filepath.Join(workDir, "pi-mcp-long-line-server.js")
	logPath := filepath.Join(workDir, "pi-mcp-long-line-calls.jsonl")
	longPayload := "MCP_LONG_LINE_BEGIN_" + strings.Repeat("0123456789", 120) + "_MCP_LONG_LINE_TAIL"
	if err := os.WriteFile(serverPath, []byte(piMCPBridgeLongLineServerSource(longPayload)), 0o700); err != nil {
		t.Fatalf("write MCP long-line server: %v", err)
	}
	mcpConfig := fmt.Sprintf(`{
  "mcpServers": {
    "api-bridge": {
      "command": "node",
      "args": [%q],
      "env": {"PI_MCP_CANARY_LOG": %q},
      "lifecycle": "keep-alive",
      "directTools": true
    }
  }
}`, serverPath, logPath)

	adapter := NewPiCLIAdapter(apiKey, piRealContractModel(), &mockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ownerSessionID := "pi-mcp-guard-e2e-" + piRandomHex(6)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Use the MCP gateway only. Call the api-bridge MCP tool long_line once, then reply exactly with DONE. Do not repeat the tool output in your final answer. If direct api_bridge_long_line is unavailable, use mcp({ search: \"long_line\" }) and mcp({ tool: \"api_bridge_long_line\", args: \"{}\" })."),
	}, WithWorkingDir(workDir), WithInteractiveSessionID(ownerSessionID), WithPersistentInteractiveSession(true), WithMCPConfig(mcpConfig), WithBridgeOnlyTools(true))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("GenerateContent() returned no choices")
	}
	if err := waitForPiMCPBridgeToolLog(ctx, logPath, "long_line"); err != nil {
		t.Fatalf("MCP long-line tool was not called: %v\nresponse=%q", err, resp.Choices[0].Content)
	}
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok {
		t.Fatal("expected persistent Pi session to remain active for pane capture")
	}
	pane, err := capturePiPane(ctx, session.tmuxSessionName)
	if err != nil {
		t.Fatalf("capture Pi pane: %v", err)
	}
	if !strings.Contains(pane, "(Ctrl+O to expand)") {
		t.Fatalf("pane did not show compact MCP output hint; latest pane:\n%s", pane)
	}
	if strings.Contains(pane, "MCP_LONG_LINE_TAIL") {
		t.Fatalf("pane rendered the long MCP result tail instead of compacting it; latest pane:\n%s", pane)
	}
	ClosePiCLIInteractiveSessionForOwner(ownerSessionID, "test cleanup")
}

func firstNonEmptyPiTestEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func waitForPiMCPBridgeCanaryLog(ctx context.Context, path string) error {
	return waitForPiMCPBridgeToolLog(ctx, path, "bridge_canary")
}

func waitForPiMCPBridgeToolLog(ctx context.Context, path, toolName string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	want := fmt.Sprintf("%q", toolName)
	for {
		body, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(body), want) {
			return nil
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func piMCPBridgeLongLineServerSource(payload string) string {
	return fmt.Sprintf(`#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");

const logPath = process.env.PI_MCP_CANARY_LOG;
const payload = %q;
const rl = readline.createInterface({ input: process.stdin });

function write(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

function result(id, resultPayload) {
  write({ jsonrpc: "2.0", id, result: resultPayload });
}

function error(id, code, message) {
  write({ jsonrpc: "2.0", id, error: { code, message } });
}

rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch {
    return;
  }
  if (msg.id === undefined || msg.id === null) return;
  switch (msg.method) {
    case "initialize":
      result(msg.id, {
        protocolVersion: msg.params?.protocolVersion || "2025-06-18",
        capabilities: { tools: {} },
        serverInfo: { name: "pi-mcp-long-line", version: "0.1.0" }
      });
      break;
    case "ping":
      result(msg.id, {});
      break;
    case "tools/list":
      result(msg.id, {
        tools: [{
          name: "long_line",
          description: "Return a long single-line payload for terminal compaction testing.",
          inputSchema: { type: "object", properties: {}, additionalProperties: false }
        }]
      });
      break;
    case "tools/call":
      if (msg.params?.name !== "long_line") {
        error(msg.id, -32602, "unknown tool");
        break;
      }
      if (logPath) {
        fs.appendFileSync(logPath, JSON.stringify({ tool: msg.params.name, ts: Date.now() }) + "\n");
      }
      result(msg.id, {
        content: [{ type: "text", text: payload }],
        isError: false
      });
      break;
    default:
      error(msg.id, -32601, "method not found");
  }
});
`, payload)
}

func piMCPBridgeCanaryServerSource() string {
	return `#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");

const logPath = process.env.PI_MCP_CANARY_LOG;
const rl = readline.createInterface({ input: process.stdin });

function write(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

function result(id, payload) {
  write({ jsonrpc: "2.0", id, result: payload });
}

function error(id, code, message) {
  write({ jsonrpc: "2.0", id, error: { code, message } });
}

rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch {
    return;
  }
  if (msg.id === undefined || msg.id === null) return;
  switch (msg.method) {
    case "initialize":
      result(msg.id, {
        protocolVersion: msg.params?.protocolVersion || "2025-06-18",
        capabilities: { tools: {} },
        serverInfo: { name: "pi-mcp-canary", version: "0.1.0" }
      });
      break;
    case "ping":
      result(msg.id, {});
      break;
    case "tools/list":
      result(msg.id, {
        tools: [{
          name: "bridge_canary",
          description: "Return a fixed canary proving the Pi MCP bridge is mounted.",
          inputSchema: { type: "object", properties: {}, additionalProperties: false }
        }]
      });
      break;
    case "tools/call":
      if (msg.params?.name !== "bridge_canary") {
        error(msg.id, -32602, "unknown tool");
        break;
      }
      if (logPath) {
        fs.appendFileSync(logPath, JSON.stringify({ tool: msg.params.name, ts: Date.now() }) + "\n");
      }
      result(msg.id, {
        content: [{ type: "text", text: "PI_MCP_BRIDGE_OK" }],
        isError: false
      });
      break;
    default:
      error(msg.id, -32601, "method not found");
  }
});
`
}
