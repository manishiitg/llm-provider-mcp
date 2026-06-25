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

	adapter := NewPiCLIAdapter(apiKey, "google/gemini-3.5-flash", &mockLogger{})
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

func firstNonEmptyPiTestEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func waitForPiMCPBridgeCanaryLog(ctx context.Context, path string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		body, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(body), `"bridge_canary"`) {
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
